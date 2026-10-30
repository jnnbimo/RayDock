package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"raydock/internal/xray"
)

// Test fixtures must not wait on or reuse an adapter of a real RayDock running on the developer's PC.
// These tests use placeholder executables; the opt-in elevated adapter tests live in package xray.
func TestMain(m *testing.M) {
	xray.TUNNames = [2]string{fmt.Sprintf("RayDockTest-%d-A", os.Getpid()), fmt.Sprintf("RayDockTest-%d-B", os.Getpid())}
	os.Exit(m.Run())
}

// connectedFake connects the first node as runningFake does, with the core committed: checks may run
// through its local port, where nothing listens, so they fail fast without external traffic.
func connectedFake(t *testing.T, a *App) {
	t.Helper()
	a.ImportLinks("trojan://p@127.0.0.1:1#n", "")
	a.State.Settings.Port, _ = freePort()
	t.Cleanup(runningFake(t, a, a.State.Nodes[0].ID))
	liveFake(a, a.planned)
}

// commitFake makes start t of the connection's core done, as its process started, without it.
func commitFake(a *App, t *coreTarget) {
	liveFake(a, t)
	a.running = t
	a.started(t, nil)
}

// liveFake publishes the core of t as running.
func liveFake(a *App, t *coreTarget) { a.live.Store(t.proc) }

// A plan that changes nothing the connection runs (a pin, a subscription, a dedicated port of nothing)
// leaves the core: the end-to-end test in flight still reports. A test overtaken by a restart ends without
// a result rather than testing forever.
func TestRealTestOutlivesReplanning(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	connectedFake(t, a)
	plan := a.planned
	a.TestReal()
	a.SyncCores()
	if a.planned != plan || a.Conn != StateOn {
		t.Fatal("the no-op plan should leave the core")
	}
	waitRegression(t, posts, func() bool { return !a.RealTest.Testing })
	if a.RealTest.Err == "" {
		t.Fatalf("result lost: %+v", a.RealTest)
	}

	a.TestReal()
	a.SetMode(xray.ModeGlobal) // a restart
	waitRegression(t, posts, func() bool { return !a.RealTest.Testing })
	if a.RealTest != (Latency{}) {
		t.Fatalf("a result of the core replaced must not show: %+v", a.RealTest)
	}

	// The restart has now committed; only the latest test sets its result.
	commitFake(a, a.planned)
	a.TestReal()
	a.TestReal()
	waitRegression(t, posts, func() bool { return !a.RealTest.Testing })
	time.Sleep(100 * time.Millisecond)
	drainRegression(posts)
	if a.RealTest.Testing || a.RealTest.Err == "" {
		t.Fatalf("latest test: %+v", a.RealTest)
	}
}

// Results measured with settings changed during the test are dropped, and their nodes stop testing.
func TestLatencyStaleResultEndsTesting(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.ImportLinks("trojan://p@127.0.0.1:1#n", "")
	id := a.State.Nodes[0].ID
	if err := a.TestLatency([]string{id}); err != nil {
		t.Fatal(err)
	}
	if !a.Latency[id].Testing {
		t.Fatal("testing")
	}
	a.State.Settings.DNSStrategy = "UseIP"
	a.SettingChanged()
	waitRegression(t, posts, func() bool { return !a.LatencyBusy })
	if l, ok := a.Latency[id]; ok {
		t.Fatalf("a result of other settings, or none, must not stay: %+v", l)
	}
}

// The bundled core offered as an update is installed from the bundle, offline.
func TestUpdateToBundledCore(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
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
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
	if a.CoreLatest != "26.10.21" || !a.latestBundled {
		t.Fatalf("offered %q bundled %v", a.CoreLatest, a.latestBundled)
	}
	a.Conn = StateOff
	a.UpdateCore()
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
	if src.latest != 0 || a.CoreVersion != "26.10.21" || a.CoreLatest != "" || a.latestBundled {
		t.Fatalf("lookups %d version %q offered %q bundled %v err %q", src.latest, a.CoreVersion, a.CoreLatest, a.latestBundled, a.CoreErr)
	}
	// An offer found online is downloaded.
	a.CoreLatest = "v99.0.0"
	a.UpdateCore()
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
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
