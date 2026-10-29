package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"raydock/internal/probe"
	"raydock/internal/store"
	"raydock/internal/xray"
)

// A plan is not a serving connection. Check both consumers of the committed
// target, including a routing-only change that reuses the backend process.
func TestConnectionChecksWaitForCommit(t *testing.T) {
	for _, routing := range []bool{false, true} {
		t.Run(fmt.Sprint("routing=", routing), func(t *testing.T) {
			a, posts, _ := newRegressionApp(t)
			var hits atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer proxy.Close()
			a.State.Settings.Port = proxy.Listener.Addr().(*net.TCPAddr).Port
			a.ImportLinks("socks://127.0.0.1:9#old\nsocks://127.0.0.1:10#new", "")
			release := runningFake(t, a, a.State.Nodes[0].ID)
			defer release()
			a.live.Store(a.planned)
			a.TestReal()
			waitRegression(t, posts, func() bool { return !a.RealTest.Testing })
			if a.RealTest.Err == "" || hits.Load() != 1 {
				t.Fatal("the committed old listener was not checked")
			}
			if routing {
				a.SetMode(xray.ModeGlobal)
			} else {
				a.Connect(a.State.Nodes[1].ID, nil)
			}
			target := a.planned
			if a.connectionTarget() != nil || a.Checkable() || a.OverviewCurrent() {
				t.Fatal("pending plan was published as a serving connection")
			}
			a.TestReal()
			a.CheckOverview(false)
			if a.RealTest.Testing || a.Overview.Testing() || hits.Load() != 1 {
				t.Fatal("checks were started against the old listener under a new plan")
			}
			// Model the physical commit; the queue stays gated so no fake process runs.
			a.live.Store(target)
			a.applied(target)
			if a.RealTest != (Latency{}) || a.connectionTarget() != target || !a.Checkable() {
				t.Fatal("the old listener's result survived the switch")
			}
			a.TestReal()
			waitRegression(t, posts, func() bool { return !a.RealTest.Testing })
			if a.RealTest.Err == "" || hits.Load() != 2 {
				t.Fatal("the newly committed connection could not be checked")
			}
		})
	}
}

func TestAppliedResultCannotPublishOlderTarget(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	connectedFake(t, a)
	old := a.live.Load()
	next := *old
	run := *old.run
	run.opts.Port, _ = freePort()
	front := *old.front
	var err error
	front.cfg, err = xray.BuildFront(run.opts, front.f)
	if err != nil {
		t.Fatal(err)
	}
	next.run, next.front = &run, &front
	next.reroute = true // another local port: apply plans it so
	a.run, a.frontRun, a.planned = next.run, next.front, &next
	a.live.Store(&next)
	port, since, key := a.runningPort, a.Since, a.OverviewKey()
	a.applied(old)
	if a.runningPort != port || a.OverviewKey() != key {
		t.Fatal("an older UI completion published a listener that no longer serves")
	}
	a.applied(&next)
	if a.runningPort != run.opts.Port || a.OverviewKey() == key {
		t.Fatal("the actual committed listener was not published")
	}
	if a.Since != since {
		t.Fatal("the same node stays connected since it connected")
	}
}

// The connected time counts from connecting to the node: a change made beside the running cores (a
// dedicated port, a setting) keeps it, another node starts it again.
func TestConnectedSinceTheNode(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	connectedFake(t, a)
	since, key := a.Since, a.OverviewKey()
	ports := *a.live.Load()
	a.live.Store(&ports)
	a.applied(&ports)
	if a.Since != since || a.OverviewKey() != key {
		t.Fatal("a dedicated port changed the connection's time or route")
	}
	time.Sleep(time.Millisecond)
	switched := *a.live.Load()
	switched.switched, switched.reroute = true, true
	a.live.Store(&switched)
	a.applied(&switched)
	if !a.Since.After(since) || a.OverviewKey() == key {
		t.Fatal("another node should start the connected time and the route again")
	}
}

// A check whose route changed while it ran is dropped, and the page checks again; one across a change of
// a dedicated port alone stands: the route is the same.
func TestOverviewDropsResultsAcrossCommit(t *testing.T) {
	for _, reroute := range []bool{true, false} {
		t.Run(fmt.Sprint("reroute=", reroute), func(t *testing.T) {
			a, posts, _ := newRegressionApp(t)
			oldURL, oldServices := probe.IPInfoURL, probe.Services
			t.Cleanup(func() { probe.IPInfoURL, probe.Services = oldURL, oldServices })
			probe.IPInfoURL, probe.Services = "http://ip.example/info", nil
			entered, release := make(chan struct{}, 1), make(chan struct{})
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				<-release
				w.Write([]byte(`{"ip":"203.0.113.1","fraudScore":12}`))
			}))
			defer proxy.Close()
			defer close(release)
			connectedFake(t, a)
			a.run.opts.Port = proxy.Listener.Addr().(*net.TCPAddr).Port
			a.CheckOverview(false)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("overview did not start")
			}
			oldKey := a.Overview.Key
			next := *a.live.Load()
			next.reroute = reroute
			a.live.Store(&next) // a fresh committed configuration, with the same node and process
			a.applied(&next)
			if a.OverviewCurrent() == reroute || (oldKey == a.OverviewKey()) == reroute {
				t.Fatal("overview did not follow the committed route")
			}
			// End the request and consume its completion, without starting another check.
			release <- struct{}{}
			select {
			case post := <-posts:
				post()
			case <-time.After(time.Second):
				t.Fatal("overview did not finish")
			}
			if (a.Overview.IP.IP != "") == reroute {
				t.Fatalf("result %q across a commit, reroute %v", a.Overview.IP.IP, reroute)
			}
		})
	}
}

func TestTrafficSaveRetriesAfterFailure(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	a.addTraffic(time.Now(), time.Second, xray.Traffic{ProxyDown: 123})
	if err := os.Mkdir(a.trafficPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	a.saveTraffic()
	if !a.trafficDue || a.trafficTimer == nil {
		t.Fatal("failed save consumed the pending data or its retry")
	}
	if err := os.Remove(a.trafficPath()); err != nil {
		t.Fatal(err)
	}
	a.Shutdown() // no new traffic: final save still has to retry
	saved, err := store.LoadTraffic(a.trafficPath())
	if err != nil || saved.Days[time.Now().Format(time.DateOnly)].ProxyDown != 123 || a.trafficDue {
		t.Fatalf("retry did not persist the pending bytes: %+v %v", saved, err)
	}
}

func TestTrafficSettlesRetiredCores(t *testing.T) {
	for _, take := range []bool{false, true} {
		t.Run(fmt.Sprint("take=", take), func(t *testing.T) {
			a, posts, _ := newRegressionApp(t)
			var down atomic.Int64
			c := &xray.Core{}
			a.procs.add(c, statsAPI(t, down.Load))
			down.Store(500)
			a.StartTraffic()
			a.StartTraffic() // must not create a second sampler
			waitRegression(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 500 })
			down.Store(800)
			if take {
				a.procs.take()
			} else {
				a.procs.remove(c)
			}
			waitRegression(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 800 })
			a.Shutdown()
			if a.TrafficOn(time.Now()).ProxyDown != 800 {
				t.Fatal("final reading was lost or double-counted")
			}
		})
	}
}

func TestShutdownSavesQueuedAndFinalTraffic(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	var down atomic.Int64
	c := &xray.Core{}
	a.procs.add(c, statsAPI(t, down.Load))
	down.Store(500)
	a.procs.sampleTraffic(a.workCtx) // intentionally not yet delivered to the UI
	down.Store(800)
	a.Shutdown() // canceled workCtx must not cancel the final reading
	saved, err := store.LoadTraffic(a.trafficPath())
	if err != nil || saved.Days[time.Now().Format(time.DateOnly)].ProxyDown != 800 {
		t.Fatalf("shutdown lost queued or final bytes: %+v %v", saved, err)
	}
}

func TestGeoCommitFailureKeepsFilesAndCancellation(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
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
	waitRegression(t, posts, func() bool { return a.coreHold })
	a.Connect(a.State.Nodes[0].ID, nil)
	if !a.WaitCore {
		t.Fatal("connect did not wait")
	}
	a.Disconnect()
	done()
	waitRegression(t, posts, func() bool { return !a.GeoBusy })
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
	a, posts, _ := newRegressionApp(t)
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
	waitRegression(t, posts, func() bool { return a.coreHold })
	go func() { <-a.workCtx.Done(); done() }()
	a.Shutdown()
	drainRegression(posts) // queued commits must be rejected after closing the operation queue
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
	a, posts, _ := newRegressionApp(t)
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
	waitRegression(t, posts, func() bool { return a.coreHold })
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
	waitRegression(t, posts, func() bool { return !a.GeoBusy && a.Conn == StateOff }) // inert placeholder fails only after resume
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
