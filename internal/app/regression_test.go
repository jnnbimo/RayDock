package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"raydock/internal/node"
	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/xray"
)

type regressionProxy struct {
	cur     sysproxy.Settings
	applied int
	fail    error // Apply fails with it, changing nothing
}

func (p *regressionProxy) Current() (sysproxy.Settings, error) { return p.cur, nil }
func (p *regressionProxy) Apply(s sysproxy.Settings) error {
	if p.fail != nil {
		return p.fail
	}
	p.cur = s
	p.applied++
	return nil
}
func newRegressionApp(t *testing.T) (*App, chan func(), *regressionProxy) {
	t.Helper()
	posts := make(chan func(), 1024)
	p := &regressionProxy{}
	a := New(Deps{DataDir: t.TempDir(), Proxy: p, Post: func(f func()) { posts <- f }}, store.State{Settings: store.DefaultSettings()})
	t.Cleanup(a.Shutdown)
	return a, posts, p
}
func waitRegression(t *testing.T, posts chan func(), cond func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for !cond() {
		select {
		case f := <-posts:
			f()
		case <-timer.C:
			t.Fatal("background result timed out")
		}
	}
}
func drainRegression(posts chan func()) {
	for {
		select {
		case f := <-posts:
			f()
		default:
			return
		}
	}
}

// coresRunning reports whether a core of the connection runs.
func coresRunning(a *App) bool {
	a.procs.mu.Lock()
	defer a.procs.mu.Unlock()
	return slices.ContainsFunc(a.procs.cores, (*xray.Core).Running)
}

// coreConfigWritten reports a config written for the core, as starting one does.
func coreConfigWritten(a *App) error {
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		return fmt.Errorf("%s: %v", filepath.Base(a.configPath()), err)
	}
	return nil
}

func placeholderCore(t *testing.T, a *App) {
	t.Helper()
	if err := os.MkdirAll(a.CoreDir(), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(a.CoreDir(), name), []byte("inert test placeholder"), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

// coreFilesIn returns a directory holding core files that read content, as a download or bundle would.
func coreFilesIn(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func waitOperation(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("core operation timed out")
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
				a, posts, p := newRegressionApp(t)
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
				waitRegression(t, posts, func() bool { return !a.CoreBusy })
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
	a, posts, p := newRegressionApp(t)
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
	waitRegression(t, posts, func() bool { return a.coreHold })
	if a.TestLatency([]string{id}) == nil {
		t.Fatal("a test started on files being replaced")
	}
	if a.planned != running || a.Conn != StateOn {
		t.Fatal("the core restarted before its files were replaced")
	}
	release()
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
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

// While connecting, a node change starts again only a start that uses the node, e.g. when certificates are
// pinned at startup.
func TestNodeChangeWhileConnecting(t *testing.T) {
	a, _, _ := newRegressionApp(t)
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

// runningFake makes a look connected to node id through its core, as a start leaves it, without a
// process but with core files, which a restart needs; core operations wait until the returned func is
// called.
func runningFake(t *testing.T, a *App, id string) (release func()) {
	t.Helper()
	if !a.CoreReady() {
		placeholderCore(t, a)
	}
	chain, err := a.Chain(a.NodeByID(id))
	if err != nil {
		t.Fatal(err)
	}
	opts := a.xrayOptions()
	local, err := a.tunLocal(opts)
	if err != nil {
		t.Skip("no physical network for TUN: ", err)
	}
	plan, err := a.planTarget(coreSpec{opts: opts, run: xray.Run{Chain: copyChain(chain), Local: local}}, change{})
	if err != nil {
		t.Fatal(err)
	}
	plan.proc.core = a.newCore()
	plan.id, plan.opts, plan.seq, plan.connSeq, plan.ctx, plan.fresh, plan.switched = id, opts, a.coreSeq, a.connSeq, a.coreCtx, true, true
	a.Conn, a.ActiveID, a.runningPort, a.Since = StateOn, id, opts.Port, time.Now()
	a.connCtx, a.connCancel = context.WithCancel(a.workCtx)
	a.planned, a.running = plan, plan
	gate := make(chan struct{})
	a.coreDo(func() { <-gate })
	return sync.OnceFunc(func() { close(gate) })
}

// Turning the TUN on or off while connected restarts the core with it. Neither may end in a config error.
func TestTUNToggleRestarts(t *testing.T) {
	for _, on := range []bool{true, false} {
		a, _, _ := newRegressionApp(t)
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
	a, posts, _ := newRegressionApp(t)
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
	a, _, _ := newRegressionApp(t)
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

func TestCoreUpdateDisconnectCancelsResume(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://a.example:1080#a", "")
	a.Conn, a.ActiveID = StateOn, a.State.Nodes[0].ID
	gate := make(chan struct{})
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 1), gate: gate}
	a.InstallCore()
	a.Disconnect()
	close(gate)
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
	waitOperation(t, a.coreDo(func() {}))
	drainRegression(posts)
	if a.Conn != StateOff {
		t.Fatalf("disconnect during update resumed connection: %v", a.Conn)
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatalf("cancelled resume started a core: %v", err)
	}
}
func TestProxyRequiresPersistedBackup(t *testing.T) {
	a, _, p := newRegressionApp(t)
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
	a, _, p := newRegressionApp(t)
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
	move := func(t *testing.T) (*App, *regressionProxy) {
		a, _, p := newRegressionApp(t)
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
			next, _, _ := newRegressionApp(t)
			next.State, next.Proxy = saved, p
			next.CleanStaleProxy()
			if p.cur != c.want || next.State.ProxyBackup != nil {
				t.Fatalf("system %+v backup %+v", p.cur, next.State.ProxyBackup)
			}
		})
	}
}

// A subscription past the size read is refused, not cut to the nodes that fit: they would replace the
// group's. One of that size exactly is whole.
func TestOversizedSubscription(t *testing.T) {
	const head = "socks://127.0.0.1:1080#first\n"
	body := head + strings.Repeat(" ", maxSubscription-len(head))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/over" {
			w.Write([]byte(body + "\nsocks://127.0.0.1:1081#second\n"))
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	if nodes, _, err := fetchSubscription(context.Background(), srv.Client(), srv.URL+"/over"); err == nil || !strings.Contains(err.Error(), "16 MB") {
		t.Fatalf("oversized: %d nodes, %v", len(nodes), err)
	}
	if nodes, _, err := fetchSubscription(context.Background(), srv.Client(), srv.URL+"/full"); err != nil || len(nodes) != 1 {
		t.Fatalf("at the limit: %d nodes, %v", len(nodes), err)
	}
}

func TestProxyUsesCommittedPort(t *testing.T) {
	a, _, p := newRegressionApp(t)
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
func TestDeletedHopInvalidatesChain(t *testing.T) {
	for _, deleteGroup := range []bool{false, true} {
		t.Run(map[bool]string{false: "node", true: "group"}[deleteGroup], func(t *testing.T) {
			a, _, _ := newRegressionApp(t)
			a.ImportLinks("trojan://pw@a.example:443#a", "")
			home, id := a.State.Groups[0].ID, a.State.Nodes[0].ID
			relay, err := a.AddGroup(GroupInput{Name: "relay"})
			if err != nil {
				t.Fatal(err)
			}
			a.ImportLinks("socks://b.example:1080#b", relay)
			front := a.State.Nodes[1].ID
			if err := a.EditGroup(home, GroupInput{Name: "home", Front: front}); err != nil {
				t.Fatal(err)
			}
			a.Conn, a.ActiveID = StateOn, id
			if deleteGroup {
				a.DeleteGroup(relay)
			} else {
				a.DeleteNode(front)
			}
			if _, err := a.Chain(a.NodeByID(id)); err == nil {
				t.Fatal("required hop deletion silently bypassed chain")
			}
			if a.Conn != StateOff || a.GroupByID(home).Front != front {
				t.Fatal("invalid connection/reference survived deletion")
			}
		})
	}
}
func TestSubscriptionURLGeneration(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			close(started)
			<-release
			_, _ = w.Write([]byte("trojan://pw@old.example:443#old"))
		} else {
			_, _ = w.Write([]byte("trojan://pw@new.example:443#new"))
		}
	}))
	defer srv.Close()
	id, err := a.AddGroup(GroupInput{Name: "sub", Subscription: true, URL: srv.URL + "/old"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("old request not started")
	}
	err = a.EditGroup(id, GroupInput{Name: "sub", URL: srv.URL + "/new"})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	waitRegression(t, posts, func() bool { return !a.SubBusy[id] })
	drainRegression(posts)
	if len(a.State.Nodes) != 1 || a.State.Nodes[0].Host != "new.example" {
		t.Fatalf("old result replaced new URL nodes: %+v", a.State.Nodes)
	}
}
func TestImportKeepsDifferentSecurity(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	base := "vless://11111111-2222-3333-4444-555555555555@a.example:443?type=ws&path=%2Fws&security="
	msg := a.ImportLinks(base+"none#plain\n"+base+"tls#tls", "").String()
	if len(a.State.Nodes) != 2 {
		t.Fatalf("distinct connection configs deduplicated: %s", msg)
	}
}
func TestCancelledTUNStartDoesNotRun(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
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
	drainRegression(posts)
	if a.Conn != StateOff || coresRunning(a) {
		t.Fatal("cancelled start left a live connection")
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatalf("cancelled start wrote config: %v", err)
	}
}
func TestQueuedConnectionsCancelBeforeSideEffects(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://a.example:1080#a\nsocks://b.example:1080#b", "")
	gate := make(chan struct{})
	a.coreDo(func() { <-gate })
	a.Connect(a.State.Nodes[0].ID, nil)
	a.Connect(a.State.Nodes[1].ID, nil)
	a.Disconnect()
	close(gate)
	waitOperation(t, a.coreDo(func() {}))
	drainRegression(posts)
	if err := coreConfigWritten(a); err != nil {
		t.Fatalf("obsolete queued connection wrote config: %v", err)
	}
}
func TestConnectionExitGeneration(t *testing.T) {
	a, _, _ := newRegressionApp(t)
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
	a, _, _ := newRegressionApp(t)
	old, next := &runningCore{core: a.newCore()}, &runningCore{core: a.newCore()}
	a.Conn = StateConnecting
	a.planned = &coreTarget{proc: next}
	a.live.Store(old)
	a.coreExited(old, errors.New("old exit"))
	if a.Conn != StateConnecting || a.ConnErr != "" || !old.exited || a.planned.proc != next {
		t.Fatal("the old core's exit cancelled its replacement")
	}
}

// The core serving the dedicated ports alone restarts when it exits, unless it ran only briefly, after
// which the next change starts it again.
func TestPortsCoreExit(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	gate := make(chan struct{})
	defer close(gate)
	a.coreDo(func() { <-gate })
	port, _ := freePort()
	if err := a.SetNodePort(a.State.Nodes[0].ID, port); err != nil {
		t.Fatal(err)
	}
	commit := func() *runningCore {
		liveFake(a, a.planned)
		a.running = a.planned
		return a.planned.core()
	}
	ports := commit()
	ports.since = time.Now()
	a.coreExited(ports, errors.New("crash"))
	if a.planned.core() != ports || !strings.Contains(a.PortErrs[a.State.Nodes[0].ID], "crash") {
		t.Fatalf("a brief core: errors %v", a.PortErrs)
	}
	a.SyncCores() // the next change
	again := a.planned.core()
	if again == ports || again == nil {
		t.Fatal("the next change should start the core again")
	}
	commit()
	again.since = time.Now().Add(-2 * portsRestartAfter)
	a.coreExited(again, errors.New("crash"))
	if p := a.planned.core(); p == again || p == nil || a.Conn != StateOff {
		t.Fatal("a core that ran a while restarts at once")
	}
}

func TestMalformedOutboundReturnsError(t *testing.T) {
	a, _, _ := newRegressionApp(t)
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
	a, posts, _ := newRegressionApp(t)
	called := false
	a.Post(func() { called = true })
	a.Shutdown()
	drainRegression(posts)
	if called {
		t.Fatal("queued background result mutated a closed app")
	}
	a.Connect("missing", nil)
	a.Disconnect()
	a.SyncCores()
	a.Shutdown() // no closed-queue sends, idempotent release
}

func TestSubscriptionVariantsKeepIDsAndPorts(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	base := "vless://11111111-2222-3333-4444-555555555555@a.example:443?type=ws&security="
	a.ImportLinks(base+"none#plain\n"+base+"tls#tls", "")
	home := a.State.Groups[0].ID
	plain, tls := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	a.State.Ports = map[string]int{plain: 20001, tls: 20002}
	fresh, errs := node.ParseMany(base + "tls#renamed TLS\n" + base + "none#renamed plain")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	a.replaceGroupNodes(home, fresh)
	if a.NodeByID(plain).Security != "none" || a.NodeByID(tls).Security != "tls" || a.State.Ports[plain] != 20001 || a.State.Ports[tls] != 20002 {
		t.Fatal("reordered variants exchanged their identities/ports")
	}
	// A changed setting of one server retains its identity (the connection takes it: SyncCores).
	a.Conn, a.ActiveID = StateOn, tls
	fresh, _ = node.ParseMany(base + "tls&sni=new.example#renamed TLS\n" + base + "none#renamed plain")
	a.replaceGroupNodes(home, fresh)
	if a.NodeByID(tls) == nil || a.NodeByID(tls).SNI != "new.example" || a.Conn != StateOn {
		t.Fatal("subscription setting change lost identity or the connection")
	}
}

// A node in a group with a front proxy is measured through its whole chain, never by connecting to its
// own server: the latency core gets the chain (the placeholder core then fails to run, which fails the
// node instead of reporting the front proxy's reachability). Every node gets a route there, to look its
// exit IP up through; a node Xray refuses is marked unusable and left out, so it cannot fail the others.
func TestLatencyThroughChain(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://front.example:1080#front", "")
	front := a.State.Nodes[0].ID
	g, err := a.AddGroup(GroupInput{Name: "chained", Front: front})
	if err != nil {
		t.Fatal(err)
	}
	a.ImportLinks("socks://member.example:3333#member", g)
	member := a.State.Nodes[1].ID
	a.ImportLinks("socks://plain.example:1080#plain", "")
	plain := a.State.Nodes[2].ID
	// VLESS in plain text to a public address: Xray refuses to start with it.
	a.State.Nodes = append(a.State.Nodes, node.Node{ID: "refused", GroupID: a.State.Nodes[2].GroupID, Name: "refused",
		Protocol: node.VLESS, Host: "203.0.113.1.nip.io", Port: 80, UUID: "u", Network: "ws"})
	if err := a.TestLatency([]string{member, plain, "refused"}); err != nil {
		t.Fatal(err)
	}
	if l := a.Latency["refused"]; l.Err != "不可用" || l.Testing {
		t.Fatalf("refused node: %+v", l)
	}
	waitRegression(t, posts, func() bool { return !a.LatencyBusy })
	cfg, err := os.ReadFile(filepath.Join(a.DataDir, "latency.json"))
	if err != nil {
		t.Fatalf("not measured through the chain: %v", err)
	}
	for _, want := range []string{`"front.example"`, `"member.example"`, `"dialerProxy"`, `"plain.example"`} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("latency config lacks %s:\n%s", want, cfg)
		}
	}
	if strings.Contains(string(cfg), "nip.io") {
		t.Errorf("the refused node reached the latency core:\n%s", cfg)
	}
	if l := a.Latency[member]; l.Err != "失败" || l.Duration != 0 || l.IP != "" {
		t.Fatalf("latency %+v", l)
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
