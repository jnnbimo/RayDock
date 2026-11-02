package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"raydock/internal/xray"
)

const bundledGeo = "202610282204"

// bundleWithGeo is an installer's core directory whose files read "bundled", its rule data of release
// bundledGeo.
func bundleWithGeo(t *testing.T) string {
	t.Helper()
	dir := coreFilesIn(t, "bundled")
	if err := os.WriteFile(filepath.Join(dir, xray.GeoTagFile), []byte(bundledGeo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An upgrade brings the installer's rule data to an installation whose rule data is older or of unknown
// release, and leaves newer rule data alone. The core, of the same version, stays.
func TestBundledRuleData(t *testing.T) {
	for _, c := range []struct {
		installed string
		seeded    bool
	}{{"", true}, {"202610212206", true}, {bundledGeo, false}, {"202610302205", false}} {
		t.Run(c.installed, func(t *testing.T) {
			a, posts, _ := newTestApp(t)
			placeholderCore(t, a)
			a.State.GeoTag = c.installed
			a.BundledCore = bundleWithGeo(t)
			a.ReadCoreVersion = func(string) (string, error) { return "26.10.21", nil }
			a.EnsureCore()
			waitFor(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy })
			geo, _ := os.ReadFile(filepath.Join(a.CoreDir(), "geosite.dat"))
			exe, _ := os.ReadFile(a.coreExe())
			want := c.installed
			if c.seeded {
				want = bundledGeo
			}
			if (string(geo) == "bundled") != c.seeded || string(exe) == "bundled" || a.State.GeoTag != want || a.GeoErr != "" {
				t.Fatalf("rule data %q core %q tag %q err %q", geo, exe, a.State.GeoTag, a.GeoErr)
			}
		})
	}
}

// A first start installs the installer's core with its rule data, whose release it records: the rule data
// is not installed a second time.
func TestBundledCoreRecordsRuleData(t *testing.T) {
	a, posts, _ := newTestApp(t)
	a.BundledCore = bundleWithGeo(t)
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.21", nil }
	a.EnsureCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy })
	geo, _ := os.ReadFile(filepath.Join(a.CoreDir(), "geosite.dat"))
	if string(geo) != "bundled" || a.State.GeoTag != bundledGeo || a.GeoMsg != "" || a.CoreErr != "" {
		t.Fatalf("rule data %q tag %q geo %q core err %q", geo, a.State.GeoTag, a.GeoMsg, a.CoreErr)
	}
}

// officialPackage installs a core whose files read "official", its rule data the package's own: of no
// release RayDock knows, since 26.10.21 with a geoip.dat of a few categories.
type officialPackage struct{ OfficialCore }

func (officialPackage) Latest(context.Context, *http.Client) (xray.Release, error) {
	return xray.Release{Tag: "v26.10.29"}, nil
}

func (officialPackage) Install(_ context.Context, _ *http.Client, _ xray.Release, dir string, _ func(int64, int64)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("official"), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// A core downloaded where the rule data is missing brings the package's own, which the installer's then
// replaces before the connection waiting starts: the release recorded is that of the files installed,
// never one of the files gone.
func TestDownloadedCoreTakesBundledRuleData(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	if err := os.Remove(filepath.Join(a.CoreDir(), "geoip.dat")); err != nil {
		t.Fatal(err)
	}
	a.State.GeoTag = bundledGeo // of the files before, one of them gone
	a.BundledCore = bundleWithGeo(t)
	a.CoreSource = officialPackage{}
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.29", nil }
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	a.Connect(a.State.Nodes[0].ID, nil) // waits for the core
	if !a.WaitCore {
		t.Fatal("connected without rule data")
	}
	waitFor(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy && !a.WaitCore })
	waitOperation(t, a.coreDo(func() {}))
	geo, _ := os.ReadFile(filepath.Join(a.CoreDir(), "geoip.dat"))
	exe, _ := os.ReadFile(a.coreExe())
	if string(exe) != "official" || string(geo) != "bundled" || a.State.GeoTag != bundledGeo || a.GeoErr != "" || a.CoreErr != "" {
		t.Fatalf("core %q rule data %q tag %q errs %q %q", exe, geo, a.State.GeoTag, a.GeoErr, a.CoreErr)
	}
	// The connection starts once the installer's rule data is in.
	a.FlushLogs()
	geoAt, connectAt := -1, -1
	for i, l := range a.Logs {
		if strings.Contains(l.Text, "已更新规则数据 "+bundledGeo) {
			geoAt = i
		}
		if strings.Contains(l.Text, "[RayDock] 连接 a") && connectAt < 0 {
			connectAt = i
		}
	}
	if geoAt < 0 || connectAt < geoAt {
		t.Fatalf("rule data at log %d, the connection at %d", geoAt, connectAt)
	}
}

func TestGeoTags(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]string{"202610282204\n": "202610282204", "latest": "", "2026102822045": ""} {
		if err := os.WriteFile(filepath.Join(dir, xray.GeoTagFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := xray.ReadGeoTag(dir); got != want {
			t.Errorf("%q: %q", content, got)
		}
	}
	if xray.NewerGeo("", "") || !xray.NewerGeo(bundledGeo, "") || xray.NewerGeo(bundledGeo, bundledGeo) || xray.NewerGeo(bundledGeo, "202611010000") {
		t.Error("NewerGeo")
	}
}

func TestGeoCommitFailureKeepsFilesAndCancellation(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.State.GeoTag = "previous"
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.CoreSource = lifecycleGeoSource{install: func(_ context.Context, dir string) error {
		return os.WriteFile(filepath.Join(dir, xray.GeoFiles[0]), []byte("incomplete"), 0o600)
	}}
	a.tests.Add(1)
	done := sync.OnceFunc(a.tests.Done)
	defer done()
	a.UpdateGeo()
	waitFor(t, posts, func() bool { return a.coreHold })
	a.Connect(a.State.Nodes[0].ID, nil)
	if !a.WaitCore {
		t.Fatal("connect did not wait")
	}
	a.Disconnect()
	done()
	waitFor(t, posts, func() bool { return !a.GeoBusy })
	if a.coreHold || a.WaitCore || a.Conn != StateOff || a.GeoErr == "" || a.GeoMsg != "" || a.State.GeoTag != "previous" {
		t.Fatalf("failed commit did not release/respect cancellation: hold=%v wait=%v state=%v error=%q tag=%q", a.coreHold, a.WaitCore, a.Conn, a.GeoErr, a.State.GeoTag)
	}
	for _, name := range xray.GeoFiles {
		if data, _ := os.ReadFile(filepath.Join(a.CoreDir(), name)); string(data) != "inert test placeholder" {
			t.Fatalf("failed rule commit changed %s: %q", name, data)
		}
	}
}

func TestShutdownCancelsPendingGeoCommit(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.CoreSource = lifecycleGeoSource{install: func(_ context.Context, dir string) error {
		for _, name := range xray.GeoFiles {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("new"), 0o600); err != nil {
				return err
			}
		}
		return nil
	}}
	a.tests.Add(1)
	done := sync.OnceFunc(a.tests.Done)
	defer done()
	a.UpdateGeo()
	waitFor(t, posts, func() bool { return a.coreHold })
	go func() { <-a.workCtx.Done(); done() }()
	a.Shutdown()
	drainPosts(posts) // queued commits must be rejected after closing the operation queue
	if _, err := os.Stat(a.geoDownloadDir()); !os.IsNotExist(err) {
		t.Fatalf("shutdown leaked staging files: %v", err)
	}
	for _, name := range xray.GeoFiles {
		if data, _ := os.ReadFile(filepath.Join(a.CoreDir(), name)); string(data) != "inert test placeholder" {
			t.Fatalf("a late commit changed %s after shutdown: %q", name, data)
		}
	}
}

type lifecycleGeoSource struct {
	OfficialCore
	install func(context.Context, string) error
}

func (s lifecycleGeoSource) LatestGeo(context.Context, *http.Client) (string, error) {
	return "202610291200", nil
}

func (s lifecycleGeoSource) InstallGeo(ctx context.Context, _ *http.Client, _, dir string, _ func(int64, int64)) error {
	return s.install(ctx, dir)
}

func TestGeoCommitWaitsForWorkersAndResumesConnect(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.CoreSource = lifecycleGeoSource{install: func(_ context.Context, dir string) error {
		if dir == a.CoreDir() {
			return fmt.Errorf("download must not modify live rule files")
		}
		for _, name := range xray.GeoFiles {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("new rules"), 0o600); err != nil {
				return err
			}
		}
		return nil
	}}
	a.tests.Add(1)
	done := sync.OnceFunc(a.tests.Done)
	defer done()
	a.UpdateGeo()
	waitFor(t, posts, func() bool { return a.coreHold })
	a.Connect(a.State.Nodes[0].ID, nil)
	if !a.WaitCore || a.TestLatency([]string{a.ActiveID}) == nil {
		done()
		t.Fatal("new users of the core files did not wait for the commit")
	}
	if data, _ := os.ReadFile(filepath.Join(a.CoreDir(), xray.GeoFiles[0])); string(data) == "new rules" {
		done()
		t.Fatal("rule files changed while an existing worker still owned them")
	}
	done()
	waitFor(t, posts, func() bool { return !a.GeoBusy && a.Conn == StateOff }) // inert placeholder fails only after resume
	if a.coreHold || a.WaitCore || a.ConnErr == "" || a.GeoErr != "" {
		t.Fatalf("commit/resume did not close: hold=%v wait=%v conn=%q geo=%q", a.coreHold, a.WaitCore, a.ConnErr, a.GeoErr)
	}
	for _, name := range xray.GeoFiles {
		if data, _ := os.ReadFile(filepath.Join(a.CoreDir(), name)); string(data) != "new rules" {
			t.Fatalf("%s not committed: %q", name, data)
		}
	}
	if _, err := os.Stat(a.geoDownloadDir()); !os.IsNotExist(err) {
		t.Fatalf("staged download was not released: %v", err)
	}
}
