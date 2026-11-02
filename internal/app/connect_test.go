package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/xray"
)

// A plan is not a serving connection: checks wait for the core of the plan to run, after a restart of
// the same node's core (another routing mode) as after another node.
func TestConnectionChecksWaitForCommit(t *testing.T) {
	for _, routing := range []bool{false, true} {
		t.Run(fmt.Sprint("routing=", routing), func(t *testing.T) {
			a, posts, _ := newTestApp(t)
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
			liveFake(a, a.planned)
			a.TestReal()
			waitFor(t, posts, func() bool { return !a.RealTest.Testing })
			if a.RealTest.Err == "" || hits.Load() != 1 {
				t.Fatal("the committed old listener was not checked")
			}
			if routing {
				a.SetMode(xray.ModeGlobal)
			} else {
				a.Connect(a.State.Nodes[1].ID, nil)
			}
			target := a.planned
			if a.connCore() != nil || a.Checkable() || a.OverviewCurrent() {
				t.Fatal("pending plan was published as a serving connection")
			}
			a.TestReal()
			a.CheckOverview(false)
			if a.RealTest.Testing || a.Overview.Testing() || hits.Load() != 1 {
				t.Fatal("checks were started against the old listener under a new plan")
			}
			// Model the start; the queue stays gated so no process runs.
			commitFake(a, target)
			if a.RealTest != (Latency{}) || a.connCore() != target.core() || !a.Checkable() {
				t.Fatal("the old listener's result survived the switch")
			}
			a.TestReal()
			waitFor(t, posts, func() bool { return !a.RealTest.Testing })
			if a.RealTest.Err == "" || hits.Load() != 2 {
				t.Fatal("the newly committed connection could not be checked")
			}
		})
	}
}

// A start replaced by a later one before its turn publishes nothing: neither its port nor a route.
func TestReplacedStartCannotPublish(t *testing.T) {
	a, _, _ := newTestApp(t)
	connectedFake(t, a)
	port, since, key := a.runningPort, a.Since, a.OverviewKey()
	a.State.Settings.Port, _ = freePort()
	a.Reconnect() // another local port
	first := a.planned
	a.SetMode(xray.ModeGlobal) // replaces it
	next := a.planned
	a.started(first, nil)
	if a.Conn != StateConnecting || a.runningPort != port || a.OverviewKey() != key {
		t.Fatal("a start replaced published a listener that does not serve")
	}
	commitFake(a, next)
	if a.Conn != StateOn || a.runningPort != a.State.Settings.Port || a.OverviewKey() == key {
		t.Fatal("the core started was not published")
	}
	if a.Since != since {
		t.Fatal("the same node stays connected since it connected")
	}
}

// The connected time counts from connecting to the node: a restart of its core for a change (a dedicated
// port, a setting) keeps it, another node starts it again. Either restart has the home page check again.
func TestConnectedSinceTheNode(t *testing.T) {
	a, _, _ := newTestApp(t)
	connectedFake(t, a)
	a.ImportLinks("trojan://p@127.0.0.1:2#other", "")
	since, key := a.Since, a.OverviewKey()
	a.RenewCores(change{geo: true})
	commitFake(a, a.planned)
	if a.Since != since || a.OverviewKey() == key {
		t.Fatal("a restart changed the connection's time, or kept its check")
	}
	time.Sleep(time.Millisecond)
	a.Connect(a.State.Nodes[1].ID, nil)
	commitFake(a, a.planned)
	if !a.Since.After(since) {
		t.Fatal("another node should start the connected time again")
	}
}

// While connecting, a node change starts again only a start that uses the node, e.g. when certificates are
// pinned at startup.
func TestNodeChangeWhileConnecting(t *testing.T) {
	a, _, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://127.0.0.1:9#a\nsocks://127.0.0.1:10#b", "")
	id, other := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	chain, _ := a.Chain(a.NodeByID(id))
	start, err := a.planTarget(coreSpec{opts: a.xrayOptions(), run: xray.Run{Chain: copyChain(chain)}}, change{})
	if err != nil {
		t.Fatal(err)
	}
	start.id, start.opts = id, a.xrayOptions()
	a.Conn, a.ActiveID, a.planned = StateConnecting, id, start
	seq := a.connSeq
	n := *a.NodeByID(other)
	n.Port = 11
	if err := a.EditNode(n); err != nil || a.connSeq != seq {
		t.Fatalf("an edit of another node started again: %v", err)
	}
	n = *a.NodeByID(id)
	n.Port = 12
	if err := a.EditNode(n); err != nil || a.connSeq == seq || a.planned == start {
		t.Fatalf("the start did not take the edit: %v", err)
	}
}

// Turning the TUN on or off while connected restarts the core with it. Neither may end in a config error.
func TestTUNToggleRestarts(t *testing.T) {
	for _, on := range []bool{true, false} {
		a, _, _ := newTestApp(t)
		a.Elevated = true
		placeholderCore(t, a)
		a.CoreVersion = xray.MinTUNVersion
		a.ImportLinks("socks://127.0.0.1:9#a", "")
		id := a.State.Nodes[0].ID
		a.State.Settings.TUN.Enabled = !on
		t.Cleanup(runningFake(t, a, id)) // before a.Shutdown, which waits for the core operations
		seq := a.connSeq
		a.State.Settings.TUN.Enabled = on
		a.SettingChanged()
		a.Reconnect()
		if a.ConnErr != "" || a.Conn != StateConnecting || a.connSeq == seq || a.planned.opts.TUN.Enabled != on ||
			a.planned.core().tun() != on || a.planned.switched {
			t.Fatalf("TUN %v: state %v err %q, restarted %v", on, a.Conn, a.ConnErr, a.connSeq != seq)
		}
		a.Disconnect()
	}
}

// Every change of what the core serves (another node, its node or chain edited, the routing
// mode, the settings applied, new rule data) restarts it: the connection goes on, on the same node unless
// switched, connected since it was made. A change of nothing it serves (a name) restarts nothing. Settings
// the change does not apply stay pending until a reconnect.
func TestChangesRestartCore(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	relay, _ := a.AddGroup(GroupInput{Name: "relay"})
	a.ImportLinks("socks://127.0.0.1:10#front", relay)
	id, front, home := a.State.Nodes[0].ID, a.State.Nodes[1].ID, a.State.Groups[0].ID
	release := runningFake(t, a, id)
	defer release()
	plan := a.planned
	restarted := func(what string, want bool) {
		t.Helper()
		if got := a.planned.core() != plan.core(); got != want || a.ConnErr != "" {
			t.Fatalf("%s: restarted %v, err %q", what, got, a.ConnErr)
		}
		if want && (a.Conn != StateConnecting || a.connCore() != nil || a.planned.id != a.ActiveID || a.planned.fresh) {
			t.Fatalf("%s: state %v target %+v", what, a.Conn, a.planned)
		}
		plan = a.planned
	}

	n := *a.NodeByID(id)
	n.Name = "renamed"
	if err := a.EditNode(n); err != nil {
		t.Fatal(err)
	}
	restarted("a rename", false)
	n.Port = 19
	if err := a.EditNode(n); err != nil {
		t.Fatal(err)
	}
	restarted("an edit", true)
	if a.planned.switched {
		t.Fatal("an edit keeps the connection's time")
	}
	if err := a.EditGroup(home, GroupInput{Name: "home", Front: front}); err != nil {
		t.Fatal(err)
	}
	restarted("a front proxy", true)

	a.State.Settings.Port++
	a.SettingChanged()
	a.SetMode(xray.ModeGlobal)
	restarted("a mode", true)
	if o := a.planned.opts; o.Mode != xray.ModeGlobal || o.Port == a.State.Settings.Port {
		t.Fatalf("the mode alone applies: %+v", o)
	}
	a.Connect(front, nil) // another node, with the settings that run
	restarted("another node", true)
	if a.ActiveID != front || a.planned.opts.Port == a.State.Settings.Port || !a.planned.switched {
		t.Fatalf("another node alone: %s %+v", a.ActiveID, a.planned)
	}
	a.Connect(id, nil)
	restarted("back", true)
	a.Reconnect() // applies the settings pending
	restarted("reconnect", true)
	if a.planned.opts.Port != a.State.Settings.Port {
		t.Fatalf("reconnecting applies the new port: %+v", a.planned.opts)
	}
	a.Connect(id, nil) // nothing changed
	restarted("no change", false)
	a.RenewCores(change{geo: true}) // new rule data: the same config, a new core
	restarted("new rule data", true)
	if len(posts) != 0 {
		t.Fatalf("%d results posted before any core operation ran", len(posts))
	}
}

// Under TUN the core serves the dedicated ports too: their chains' first hops are bound to the physical
// interface, as the connection's is, and their names resolved by its DNS servers. Another node or another
// dedicated port restarts it.
func TestSwitchUnderTUN(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.Elevated = true
	placeholderCore(t, a)
	a.CoreVersion = xray.MinTUNVersion
	a.State.Settings.TUN.Enabled = true
	a.ImportLinks("socks://a.example.com:1080#a\nsocks://B.example.com:1080#b\nsocks://c.example.com:1080#c", "")
	id, other, third := a.State.Nodes[0].ID, a.State.Nodes[1].ID, a.State.Nodes[2].ID
	port, _ := freePort()
	if err := a.SetNodePort(third, port); err != nil {
		t.Fatal(err)
	}
	release := runningFake(t, a, id)
	defer release()
	plan := a.planned
	if !plan.core().tun() || len(plan.core().run.Ports) != 1 || plan.core().run.Local == nil {
		t.Fatalf("core %+v", plan.core())
	}
	a.Connect(other, nil)
	if a.ConnErr != "" || a.planned.core() == plan.core() {
		t.Fatalf("another node: err %q", a.ConnErr)
	}
	plan = a.planned
	commitFake(a, plan)
	if err := a.SetNodePort(id, port+1); err != nil {
		t.Fatal(err)
	}
	if a.planned.core() == plan.core() || a.Conn != StateConnecting {
		t.Fatalf("a dedicated port should restart the core: %v", a.Conn)
	}
	cfg := string(a.planned.core().cfg)
	for _, want := range []string{"full:a.example.com", "full:b.example.com", "full:c.example.com"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("no %s: the first hops are resolved by the local DNS: %s", want, cfg)
		}
	}
	if strings.Count(cfg, `"interface": "`) < 4 { // three first hops, direct
		t.Fatalf("the first hops are bound to the physical interface: %s", cfg)
	}
}

func TestProxyRequiresPersistedBackup(t *testing.T) {
	a, _, p := newTestApp(t)
	if err := os.Mkdir(a.statePath()+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	a.Conn, a.runningPort = StateOn, a.State.Settings.Port
	a.SyncProxy()
	if p.applied != 0 || a.State.ProxyBackup != nil || a.ProxyErr == "" {
		t.Fatalf("backup failure changed proxy: applied=%d backup=%+v err=%q", p.applied, a.State.ProxyBackup, a.ProxyErr)
	}
}

func TestProxyBackupFailureKeepsPreviousBackup(t *testing.T) {
	a, _, p := newTestApp(t)
	a.Conn, a.runningPort = StateOn, a.State.Settings.Port
	a.SyncProxy()
	backup := *a.State.ProxyBackup
	applied := p.applied
	if err := os.Mkdir(a.statePath()+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	a.runningPort++
	a.SyncProxy()
	if *a.State.ProxyBackup != backup || p.applied != applied {
		t.Fatal("failed backup overwrote previous recovery information")
	}
}

// Moving the system proxy to another port fails, or RayDock ends before or after writing it: either
// address is RayDock's, so disconnecting, or the next start, restores the proxy from before; one another
// program set stays.
func TestProxyPortMoveRecovers(t *testing.T) {
	original := sysproxy.Settings{Enabled: true, Server: "127.0.0.1:3128", Override: "<local>"}
	move := func(t *testing.T) (*App, *fakeProxy) {
		a, _, p := newTestApp(t)
		p.cur = original
		a.Conn, a.runningPort = StateOn, 10818
		a.SyncProxy()
		p.fail = errors.New("injected")
		a.runningPort = 10819
		a.SyncProxy()
		if a.ProxyErr == "" || p.cur.Server != "127.0.0.1:10818" {
			t.Fatalf("err %q, system %+v", a.ProxyErr, p.cur)
		}
		p.fail = nil
		return a, p
	}
	t.Run("disconnect", func(t *testing.T) {
		a, p := move(t)
		a.Conn = StateOff
		a.SyncProxy()
		if p.cur != original || a.State.ProxyBackup != nil {
			t.Fatalf("system %+v backup %+v", p.cur, a.State.ProxyBackup)
		}
	})
	t.Run("retry", func(t *testing.T) {
		a, p := move(t)
		a.SyncProxy()
		if b := a.State.ProxyBackup; p.cur.Server != "127.0.0.1:10819" || b.Applied != "127.0.0.1:10819" || b.Pending != "" {
			t.Fatalf("system %+v backup %+v", p.cur, b)
		}
	})
	for _, c := range []struct {
		name   string
		system string // what the system proxy is at the next start
		want   sysproxy.Settings
	}{
		{"crash before writing", "127.0.0.1:10818", original},
		{"crash after writing", "127.0.0.1:10819", original},
		{"another program", "127.0.0.1:7890", sysproxy.Settings{Enabled: true, Server: "127.0.0.1:7890"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, p := move(t)
			saved, err := store.Load(a.statePath())
			if err != nil {
				t.Fatal(err)
			}
			p.cur = sysproxy.Settings{Enabled: true, Server: c.system}
			next, _, _ := newTestApp(t)
			next.State, next.Proxy = saved, p
			next.CleanStaleProxy()
			if p.cur != c.want || next.State.ProxyBackup != nil {
				t.Fatalf("system %+v backup %+v", p.cur, next.State.ProxyBackup)
			}
		})
	}
}

func TestProxyUsesCommittedPort(t *testing.T) {
	a, _, p := newTestApp(t)
	a.Conn, a.runningPort = StateOn, a.State.Settings.Port
	a.SyncProxy()
	running := p.cur.Server
	a.State.Settings.Port++
	a.SettingChanged()
	a.SyncProxy()
	if p.cur.Server != running || a.ProxyPort() != a.runningPort || !a.SettingsDirty {
		t.Fatalf("pending settings changed running proxy: %s -> %s", running, p.cur.Server)
	}
}

func TestCancelledTUNStartDoesNotRun(t *testing.T) {
	a, posts, _ := newTestApp(t)
	a.Elevated = true
	a.State.Settings.TUN.Enabled = true
	placeholderCore(t, a)
	a.CoreVersion = xray.MinTUNVersion // this test exercises cancellation after version verification
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	gated := a.coreDo(func() { <-gate })
	a.Connect(a.State.Nodes[0].ID, nil)
	a.Disconnect()
	release()
	waitOperation(t, gated)
	// A queue barrier proves all core actions completed, without relying on callbacks that a cancelled start must not post.
	waitOperation(t, a.coreDo(func() {}))
	drainPosts(posts)
	if a.Conn != StateOff || coresRunning(a) {
		t.Fatal("cancelled start left a live connection")
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatalf("cancelled start wrote config: %v", err)
	}
}

func TestQueuedConnectionsCancelBeforeSideEffects(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://a.example:1080#a\nsocks://b.example:1080#b", "")
	gate := make(chan struct{})
	a.coreDo(func() { <-gate })
	a.Connect(a.State.Nodes[0].ID, nil)
	a.Connect(a.State.Nodes[1].ID, nil)
	a.Disconnect()
	close(gate)
	waitOperation(t, a.coreDo(func() {}))
	drainPosts(posts)
	if err := coreConfigWritten(a); err != nil {
		t.Fatalf("obsolete queued connection wrote config: %v", err)
	}
}

func TestConnectionExitGeneration(t *testing.T) {
	a, _, _ := newTestApp(t)
	old, cur := &runningCore{core: a.newCore()}, &runningCore{core: a.newCore()}
	a.Conn = StateConnecting
	a.planned = &coreTarget{proc: cur}
	a.live.Store(cur)
	a.coreExited(old, errors.New("old exit"))
	if a.Conn != StateConnecting || old.exited {
		t.Fatal("old process exit invalidated new connection")
	}
	a.coreExited(cur, errors.New("current exit"))
	if a.Conn != StateOff || a.ConnErr != "current exit" || !cur.exited {
		t.Fatal("exit before success was lost")
	}
}

// An old core may exit while the replacement is still starting: that exit must not cancel its start.
func TestOldCoreExitDuringHandover(t *testing.T) {
	a, _, _ := newTestApp(t)
	old, next := &runningCore{core: a.newCore()}, &runningCore{core: a.newCore()}
	a.Conn = StateConnecting
	a.planned = &coreTarget{proc: next}
	a.live.Store(old)
	a.coreExited(old, errors.New("old exit"))
	if a.Conn != StateConnecting || a.ConnErr != "" || !old.exited || a.planned.proc != next {
		t.Fatal("the old core's exit cancelled its replacement")
	}
}

func TestMalformedOutboundReturnsError(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.ImportLinks("socks://a.example:1080#a", "")
	for _, raw := range []string{"null", "[]", "{}", `{"protocol":null}`} {
		a.State.Nodes[0].Outbound = raw
		a.Connect(a.State.Nodes[0].ID, nil)
		if a.Conn != StateOff || !strings.Contains(a.ConnErr, "JSON") {
			t.Fatalf("bad outbound %q not rejected: %q", raw, a.ConnErr)
		}
	}
}

func TestShutdownRejectsQueuedResults(t *testing.T) {
	a, posts, _ := newTestApp(t)
	called := false
	a.Post(func() { called = true })
	a.Shutdown()
	drainPosts(posts)
	if called {
		t.Fatal("queued background result mutated a closed app")
	}
	a.Connect("missing", nil)
	a.Disconnect()
	a.SyncCores()
	a.Shutdown() // no closed-queue sends, idempotent release
}

// Another node with the same connection settings is another node: the connection moves to it, its core
// restarted as for any other.
func TestEquivalentNodeIsConnected(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.ImportLinks("socks://127.0.0.1:9#first", "")
	other, err := a.AddGroup(GroupInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	a.ImportLinks("socks://127.0.0.1:9#second", other)
	first, second := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	t.Cleanup(runningFake(t, a, first))
	a.Connect(second, nil)
	if a.ActiveID != second || a.State.LastID != second || a.ConnErr != "" {
		t.Fatalf("active %q last %q, err %q", a.ActiveID, a.State.LastID, a.ConnErr)
	}
	if a.planned == nil || a.planned.id != second || !a.planned.switched {
		t.Fatal("the core's target keeps the node switched from")
	}
}

// Typing in a settings field saves once the typing pauses, not on every key.
func TestSettingChangedSavesOnce(t *testing.T) {
	a, posts, _ := newTestApp(t)
	for i := range 20 {
		a.State.Settings.ProxyRules = strings.Repeat("x", i) + ".com"
		a.SettingChanged()
	}
	if _, err := os.Stat(a.statePath()); err == nil {
		t.Fatal("saved on a key")
	}
	waitFor(t, posts, func() bool { return !a.saveDue })
	if raw, err := os.ReadFile(a.statePath()); err != nil || !strings.Contains(string(raw), strings.Repeat("x", 19)+".com") {
		t.Fatalf("the last edit was not saved: %v", err)
	}
}
