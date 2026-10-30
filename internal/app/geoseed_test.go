package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
			a, posts, _ := newRegressionApp(t)
			placeholderCore(t, a)
			a.State.GeoTag = c.installed
			a.BundledCore = bundleWithGeo(t)
			a.ReadCoreVersion = func(string) (string, error) { return "26.10.21", nil }
			a.EnsureCore()
			waitRegression(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy })
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
	a, posts, _ := newRegressionApp(t)
	a.BundledCore = bundleWithGeo(t)
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.21", nil }
	a.EnsureCore()
	waitRegression(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy })
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
	a, posts, _ := newRegressionApp(t)
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
	waitRegression(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy && !a.WaitCore })
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
