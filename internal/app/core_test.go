package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raydock/internal/xray"
)

// A newer bundled core, as a RayDock upgrade brings, is installed at start, offline; one no newer is not.
func TestNewerBundledCoreInstalled(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	if err := os.WriteFile(a.coreExe(), []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.BundledCore = coreFilesIn(t, "new")
	version := map[string]string{"old": "26.9.30", "new": "26.10.21"}
	a.ReadCoreVersion = func(exe string) (string, error) {
		b, _ := os.ReadFile(exe)
		return version[string(b)], nil
	}
	a.EnsureCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "new" || a.CoreVersion != "26.10.21" || a.CoreErr != "" {
		t.Fatalf("core %q version %q err %q", b, a.CoreVersion, a.CoreErr)
	}
	// The same version again: nothing is copied.
	if err := os.WriteFile(a.coreExe(), []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	version["old"] = "26.10.21"
	a.EnsureCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "old" {
		t.Fatalf("an equal bundle was installed: %q", b)
	}
}

// A missing core is installed from the bundle; without one, only reinstalling RayDock helps, and a
// connection waiting for the core ends with that.
func TestMissingCore(t *testing.T) {
	a, posts, _ := newTestApp(t)
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.Connect(a.State.Nodes[0].ID, nil)
	if a.Conn != StateOff || !strings.Contains(a.ConnErr, "请重新安装 RayDock") || a.CoreErr != a.ConnErr {
		t.Fatalf("no bundle: conn %v err %q core err %q", a.Conn, a.ConnErr, a.CoreErr)
	}
	a.BundledCore = coreFilesIn(t, "bundled")
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.21", nil }
	a.Connect(a.State.Nodes[0].ID, nil)
	if !a.WaitCore || !a.CoreBusy || a.CoreErr != "" {
		t.Fatalf("bundle: wait %v busy %v err %q", a.WaitCore, a.CoreBusy, a.CoreErr)
	}
	// Placeholders are not executables: the start error proves the connect reached starting the core.
	waitFor(t, posts, func() bool { return !a.CoreBusy && a.Conn == StateOff })
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "bundled" || !strings.Contains(a.ConnErr, "核心") {
		t.Fatalf("core %q err %q", b, a.ConnErr)
	}
}

// A core that does not run is installed again from the bundle.
func TestBrokenCoreReinstalled(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.BundledCore = coreFilesIn(t, "bundled")
	a.ReadCoreVersion = func(exe string) (string, error) {
		if b, _ := os.ReadFile(exe); string(b) != "bundled" {
			return "", errors.New("not an executable")
		}
		return "26.10.21", nil
	}
	a.EnsureCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "bundled" || a.CoreVersion != "26.10.21" || a.CoreErr != "" {
		t.Fatalf("core %q version %q err %q", b, a.CoreVersion, a.CoreErr)
	}
}

// TUN needs a core new enough; with none, the connection ends asking for a newer RayDock.
func TestTUNCoreTooOld(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.CoreVersion = ""
	a.ReadCoreVersion = func(string) (string, error) { return "26.3.27", nil }
	a.Elevated = true
	a.State.Settings.TUN.Enabled = true
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.Connect(a.State.Nodes[0].ID, nil)
	waitFor(t, posts, func() bool { return a.Conn == StateOff })
	if !strings.Contains(a.ConnErr, xray.MinTUNVersion) || !strings.Contains(a.ConnErr, "请更新 RayDock") {
		t.Fatalf("err %q", a.ConnErr)
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatal("the old core started", err)
	}
}

func TestTUNWaitsForVersionBeforeStarting(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.Elevated = true
	a.State.Settings.TUN.Enabled = true
	gate := make(chan struct{})
	a.CoreVersion = ""
	a.ReadCoreVersion = func(string) (string, error) { <-gate; return xray.MinTUNVersion, nil }
	a.ImportLinks("socks://127.0.0.1:9", "")
	if a.coreUsable(true) {
		t.Fatal("unknown version passed gate")
	}
	a.EnsureCore()
	a.Connect(a.State.Nodes[0].ID, nil)
	if !a.CoreBusy || !a.WaitCore {
		t.Fatal("did not wait for version")
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatal("core started before verification", err)
	}
	close(gate)
	waitFor(t, posts, func() bool { return !a.CoreBusy && a.Conn == StateOff }) // placeholder fails only after verification
	if a.CoreVersion != xray.MinTUNVersion || a.WaitCore {
		t.Fatalf("version=%q wait=%v", a.CoreVersion, a.WaitCore)
	}
}

func TestTUNStartupUsesNewerBundle(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	if err := os.WriteFile(a.coreExe(), []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	a.BundledCore = t.TempDir()
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(a.BundledCore, name), []byte("new"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	a.ReadCoreVersion = func(exe string) (string, error) {
		b, err := os.ReadFile(exe)
		if err != nil {
			return "", err
		}
		if string(b) == "old" {
			return "26.3.27", nil
		}
		return "26.10.10", nil
	}
	a.Elevated = true
	a.State.Settings.TUN.Enabled = true
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.EnsureCore()
	a.Connect(a.State.Nodes[0].ID, nil)
	// The first callback must replace, not start, the unsupported old core.
	select {
	case f := <-posts:
		f()
	case <-time.After(time.Second):
		t.Fatal("no local version result")
	}
	if !a.WaitCore || !a.CoreBusy {
		t.Fatal("old core escaped pending installation")
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatal("old core started", err)
	}
	waitFor(t, posts, func() bool { return !a.CoreBusy && a.Conn == StateOff })
	if a.CoreVersion != "26.10.10" {
		t.Fatal("wrong installed version", a.CoreVersion)
	}
	b, err := os.ReadFile(a.coreExe())
	if err != nil || string(b) != "new" {
		t.Fatalf("bundle not installed: %q %v", b, err)
	}
}

func TestInstalledVersionMustBeVerified(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.CoreVersion = "26.10.10"
	a.ReadCoreVersion = func(string) (string, error) { return "99.1.1", errors.New("invalid executable") }
	a.BundledCore = coreFilesIn(t, "new")
	a.seedCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if a.CoreVersion != "26.10.10" || !a.coreUsable(true) || !strings.Contains(a.CoreErr, "invalid executable") {
		t.Fatalf("known-good version not preserved: %q %q", a.CoreVersion, a.CoreErr)
	}
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "inert test placeholder" {
		t.Fatalf("invalid candidate replaced the installed file: %q", b)
	}
	a.CoreVersion = ""
	if a.coreUsable(false) {
		t.Fatal("unverified files allowed a non-TUN core start")
	}
}

// A verified core is used at once.
func TestConnectUsesVerifiedCore(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.10", nil }
	a.EnsureCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if a.CoreVersion != "26.10.10" {
		t.Fatal("version not published")
	}
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.Connect(a.State.Nodes[0].ID, nil)
	waitFor(t, posts, func() bool { return a.Conn == StateOff })
	if !strings.Contains(a.ConnErr, "核心") {
		t.Fatal("did not try verified core", a.ConnErr)
	}
}

// Replacing the core while connected keeps the connection: the files are replaced under the running core,
// then it restarts from them (RenewCores), on the same node, the system proxy kept; only tests wait.
func TestCoreReplacementKeepsConnection(t *testing.T) {
	a, posts, p := newTestApp(t)
	placeholderCore(t, a)
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.10", nil }
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	id := a.State.Nodes[0].ID
	release := runningFake(t, a, id)
	defer release()
	a.SyncProxy()
	applied, running, since := p.cur, a.planned, a.Since
	a.BundledCore = coreFilesIn(t, "new")
	a.seedCore()
	waitFor(t, posts, func() bool { return a.coreHold })
	if a.TestLatency([]string{id}) == nil {
		t.Fatal("a test started on files being replaced")
	}
	if a.planned != running || a.Conn != StateOn {
		t.Fatal("the core restarted before its files were replaced")
	}
	release()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "new" || a.CoreErr != "" {
		t.Fatalf("core %q error %q", b, a.CoreErr)
	}
	if a.ActiveID != id || p.cur != applied || a.State.ProxyBackup == nil {
		t.Fatalf("the connection was touched: state %v proxy %+v", a.Conn, p.cur)
	}
	if a.planned == running || a.planned.id != id || a.planned.switched || a.Since != since {
		t.Fatalf("the core should restart for the same connection: %+v", a.planned)
	}
}

// Disconnecting while the core is replaced is not undone when the replacement ends.
func TestCoreReplacementDisconnectCancelsResume(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://a.example:1080#a", "")
	a.Conn, a.ActiveID = StateOn, a.State.Nodes[0].ID
	a.BundledCore = coreFilesIn(t, "new")
	a.seedCore()
	a.Disconnect()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	waitOperation(t, a.coreDo(func() {}))
	drainPosts(posts)
	if a.Conn != StateOff {
		t.Fatalf("disconnect during the replacement resumed the connection: %v", a.Conn)
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatalf("cancelled resume started a core: %v", err)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		tag, cur string
		want     bool
	}{
		{"v26.10.10", "26.3.27", true},
		{"v26.3.27", "26.3.27", false},
		{"v26.3.27", "26.10.10", false},
		{"v27.1.1", "26.12.31", true},
		{"nightly", "26.3.27", false},
	} {
		if got := newerVersion(c.tag, c.cur); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.tag, c.cur, got)
		}
	}
	if !tunTooOld("26.3.27") || tunTooOld("26.10.10") || tunTooOld(xray.MinTUNVersion) || !tunTooOld("") || !tunTooOld("nightly") {
		t.Error("tunTooOld is wrong")
	}
}
