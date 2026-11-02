package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raydock/internal/sysproxy"
	"raydock/internal/xray"
)

// The bundled core offered as an update is installed from the bundle, offline.
func TestUpdateToBundledCore(t *testing.T) {
	a, posts, _ := newTestApp(t)
	src := &countingSource{}
	a.CoreSource = src
	placeholderCore(t, a)
	if err := os.WriteFile(a.coreExe(), []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.BundledCore = t.TempDir()
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(a.BundledCore, name), []byte("new"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a.ReadCoreVersion = func(exe string) (string, error) {
		if b, _ := os.ReadFile(exe); string(b) == "old" {
			return "26.9.30", nil
		}
		return "26.10.21", nil
	}
	// Connected, the newer bundle is offered rather than installed at start.
	a.Conn = StateOn
	a.EnsureCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if a.CoreLatest != "26.10.21" || !a.latestBundled {
		t.Fatalf("offered %q bundled %v", a.CoreLatest, a.latestBundled)
	}
	a.Conn = StateOff
	a.UpdateCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if src.latest != 0 || a.CoreVersion != "26.10.21" || a.CoreLatest != "" || a.latestBundled {
		t.Fatalf("lookups %d version %q offered %q bundled %v err %q", src.latest, a.CoreVersion, a.CoreLatest, a.latestBundled, a.CoreErr)
	}
	// An offer found online is downloaded.
	a.CoreLatest = "v99.0.0"
	a.UpdateCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if src.latest != 1 {
		t.Fatalf("lookups %d", src.latest)
	}
}

type countingSource struct {
	OfficialCore
	latest int
}

func (s *countingSource) Latest(context.Context, *http.Client) (xray.Release, error) {
	s.latest++
	return xray.Release{}, errors.New("offline")
}

func TestTUNWaitsForVersionBeforeStarting(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.Elevated = true
	a.State.Settings.TUN.Enabled = true
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 1)}
	gate := make(chan struct{})
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

type gatedReleaseSource struct {
	OfficialCore
	started, release chan struct{}
}

func (s gatedReleaseSource) Latest(ctx context.Context, _ *http.Client) (xray.Release, error) {
	close(s.started)
	select {
	case <-s.release:
		return xray.Release{Tag: "v99.1.1"}, nil
	case <-ctx.Done():
		return xray.Release{}, ctx.Err()
	}
}

func TestStaleReleaseCannotOverrideInstallation(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.CoreVersion = "26.10.10"
	source := gatedReleaseSource{started: make(chan struct{}), release: make(chan struct{})}
	a.CoreSource = source
	a.CheckCoreUpdate()
	select {
	case <-source.started:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup not started")
	}
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 4)}
	a.InstallCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	revision, installErr := a.coreRevision, a.CoreErr
	close(source.release)
	select {
	case f := <-posts:
		f()
	case <-time.After(5 * time.Second):
		t.Fatal("no old release result")
	}
	if a.CoreBusy || a.coreRevision != revision || a.CoreLatest != "" || a.CoreErr != installErr {
		t.Fatal("stale release changed newer installation state")
	}
}

func TestInstalledVersionMustBeVerified(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.CoreVersion = "26.10.10"
	a.ReadCoreVersion = func(string) (string, error) { return "99.1.1", errors.New("invalid executable") }
	src := coreFilesIn(t, "new")
	a.coreJob("install", "安装", func(*http.Client) (string, error) { return src, nil })
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if a.CoreVersion != "" || a.coreUsable(true) || !strings.Contains(a.CoreErr, "invalid executable") {
		t.Fatalf("unverified version trusted: %q %q", a.CoreVersion, a.CoreErr)
	}
}

func TestLocalReadinessDoesNotWaitForReleaseLookup(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	gate := make(chan struct{})
	defer close(gate)
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 1), gate: gate}
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

type inspectingCoreSource struct {
	OfficialCore
	observed chan string
	gate     <-chan struct{}
}

func (s inspectingCoreSource) Latest(ctx context.Context, c *http.Client) (xray.Release, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return xray.Release{}, ctx.Err()
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/", nil)
	host := ""
	if proxy := c.Transport.(*http.Transport).Proxy; proxy != nil {
		u, _ := proxy(req)
		if u != nil {
			host = u.Host
		}
	}
	s.observed <- host
	return xray.Release{}, errors.New("test: no download")
}

// A core download goes through the connection, which stays up meanwhile; without one, through another
// program's system proxy, else direct.
func TestCoreUpdateDownloadRoute(t *testing.T) {
	for _, connected := range []bool{false, true} {
		for _, previous := range []string{"", "127.0.0.1:3128"} {
			t.Run(fmt.Sprint(connected, previous), func(t *testing.T) {
				a, posts, p := newTestApp(t)
				p.cur = sysproxy.Settings{Enabled: previous != "", Server: previous}
				want := previous
				if connected {
					a.Conn, a.runningPort = StateOn, a.State.Settings.Port
					a.SyncProxy()
					want = a.ProxyAddr()
				}
				observed := make(chan string, 1)
				a.CoreSource = inspectingCoreSource{observed: observed}
				a.InstallCore()
				if connected && (a.Conn != StateOn || a.State.ProxyBackup == nil) {
					t.Fatalf("a download disconnected: %v", a.Conn)
				}
				select {
				case host := <-observed:
					if host != want {
						t.Fatalf("download route=%q, want %q", host, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("no release request")
				}
				waitFor(t, posts, func() bool { return !a.CoreBusy })
				if connected && a.Conn != StateOn {
					t.Fatalf("a failed download disconnected: %v", a.Conn)
				}
			})
		}
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
	src := coreFilesIn(t, "new")
	a.coreJob("install", "安装", func(*http.Client) (string, error) { return src, nil })
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

func TestCoreUpdateDisconnectCancelsResume(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://a.example:1080#a", "")
	a.Conn, a.ActiveID = StateOn, a.State.Nodes[0].ID
	gate := make(chan struct{})
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 1), gate: gate}
	a.InstallCore()
	a.Disconnect()
	close(gate)
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	waitOperation(t, a.coreDo(func() {}))
	drainPosts(posts)
	if a.Conn != StateOff {
		t.Fatalf("disconnect during update resumed connection: %v", a.Conn)
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
