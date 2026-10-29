//go:build integration

package app

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raydock/internal/probe"
	"raydock/internal/xray"
)

// While connected, nothing but the TUN restarts the front core, which programs reach: another node, new
// rule data, an edited node, a new core or a dedicated port start a new backend core beside the old one,
// which stops at once for another node and otherwise once it carries no connection; the routing mode is
// switched into the backend core, another local port into the front core. Two cores run, besides those
// finishing their connections. Disconnected, they serve the dedicated ports alone. A core that exits ends
// the connection.
func TestIntegrationSwitchInPlace(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.ReadCoreVersion = xray.Version
	// The nodes: SOCKS inbounds of a local server core, which may reach local targets.
	var servers [3]int
	var inbounds []string
	for i := range servers {
		servers[i], _ = freePort()
		inbounds = append(inbounds, fmt.Sprintf(`{"listen":"127.0.0.1","port":%d,"protocol":"socks"}`, servers[i]))
	}
	server := xray.Core{}
	if err := server.Start(filepath.Join(a.CoreDir(), xray.ExeName()), filepath.Join(t.TempDir(), "server.json"), []byte(`{"log":{"loglevel":"warning"},
		"inbounds":[`+strings.Join(inbounds, ",")+`],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}]}`)); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	port, _ := freePort()
	a.State.Settings.Port = port
	a.State.Settings.SystemProxy = false
	a.State.Settings.ProxyRules = "127.0.0.1/32" // local targets go through the nodes
	a.ImportLinks(fmt.Sprintf("socks://127.0.0.1:%d#a\nsocks://127.0.0.1:%d#b", servers[0], servers[1]), "")
	first, second := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	settle := func() {
		waitOperation(t, a.coreDo(func() {}))
		drainRegression(posts)
	}
	listening := func(port int) bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	// running counts the core processes: the front core and the backend cores.
	running := func() int {
		a.procs.mu.Lock()
		defer a.procs.mu.Unlock()
		n := 0
		for _, c := range append([]*xray.Core{a.front}, a.procs.cores...) {
			if c.Running() {
				n++
			}
		}
		return n
	}
	start := time.Now()
	a.Connect(first, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	t.Logf("connect: %v", time.Since(start).Round(time.Millisecond))
	front, backend := a.front.PID(), a.run.core
	if a.Conn != StateOn || front == 0 || backend.PID() == 0 || running() != 2 {
		t.Fatalf("connect: %v %q, %d cores", a.Conn, a.ConnErr, running())
	}
	cores := func(what string, wantBackend bool) {
		t.Helper()
		if a.Conn != StateOn || a.front.PID() != front {
			t.Fatalf("%s: state %v front %d→%d %q", what, a.Conn, front, a.front.PID(), a.ConnErr)
		}
		if (a.run.core != backend) != wantBackend || !a.run.core.Running() {
			t.Fatalf("%s: new backend %v, running %v", what, a.run.core != backend, a.run.core.Running())
		}
		backend = a.run.core
	}

	old, connectedSince := backend, a.Since
	start = time.Now()
	a.Connect(second, nil)
	settle()
	if !a.Since.After(connectedSince) {
		t.Fatal("another node: connected since it was switched to")
	}
	t.Logf("another node: %v", time.Since(start).Round(time.Millisecond))
	cores("another node", true)
	if a.ActiveID != second || old.Running() || running() != 2 {
		t.Fatalf("another node: id %s; the old backend core stops at once: %v; %d cores", a.ActiveID, old.Running(), running())
	}
	since, key := a.Since, a.OverviewKey()
	a.SetMode(xray.ModeGlobal)
	settle()
	cores("a routing mode", false)
	// The same node stays connected since it connected; the route its requests take changed.
	if a.run.switches != 1 || a.Since != since || a.OverviewKey() == key {
		t.Fatalf("a routing mode: run %+v since %v → %v, key %s → %s", a.run, since, a.Since, key, a.OverviewKey())
	}
	logs := strings.Join(logTexts(a), "\n")
	if !strings.Contains(logs, "经原节点的连接已断开") || !strings.Contains(logs, "已建立的连接不中断") || strings.Contains(logs, "无法在运行中") {
		t.Fatalf("logs %q", logs)
	}

	// Another local port: the front core opens it, then closes the old one.
	oldPort := port
	a.State.Settings.Port, _ = freePort()
	a.SettingChanged()
	if !a.SettingsDirty {
		t.Fatal("a new port waits for a reconnect")
	}
	a.Reconnect()
	settle()
	cores("another port", false)
	if a.ProxyPort() != a.State.Settings.Port || !listening(a.ProxyPort()) || listening(oldPort) || a.SettingsDirty {
		t.Fatalf("another port: %d (listening %v), old %d still %v", a.ProxyPort(), listening(a.ProxyPort()), oldPort, listening(oldPort))
	}

	// New rule data: a new backend core; the old one carries a transfer begun before to its end, then stops.
	// /hold sends its second part only once resume closes.
	resume := make(chan struct{})
	stream := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			for range 2 {
				w.Write(make([]byte, 1024))
				w.(http.Flusher).Flush()
				select {
				case <-resume:
				case <-r.Context().Done():
					return
				}
			}
			return
		}
		for range 10 {
			w.Write(make([]byte, 1024))
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	})}
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go stream.Serve(sl)
	defer stream.Close()
	transfer := func(port int, path ...string) *http.Response { // through the cores and the node
		t.Helper()
		resp, err := probe.ProxyClient(port).Get("http://" + sl.Addr().String() + "/" + strings.Join(path, ""))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(resp.Body, make([]byte, 1024)); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	finishes := func(what string, resp *http.Response) {
		t.Helper()
		defer resp.Body.Close()
		if n, err := io.Copy(io.Discard, resp.Body); err != nil || n != 9*1024 {
			t.Fatalf("%s: the transfer ended after %d bytes: %v", what, n, err)
		}
	}
	resp := transfer(a.ProxyPort())
	old = a.run.core
	a.RenewCores()
	settle()
	cores("new rule data", true)
	if !old.Running() || running() != 3 {
		t.Fatalf("the old backend core stopped with a transfer: %d cores", running())
	}
	finishes("new rule data", resp)
	eventually(t, posts, func() bool { return !old.Running() && running() == 2 })

	// An edit of the node in use: a new backend core; the old one stops once it carries nothing.
	old = backend
	n := *a.NodeByID(second)
	n.Port = servers[2]
	if err := a.EditNode(n); err != nil {
		t.Fatal(err)
	}
	settle()
	cores("node edit", true)
	eventually(t, posts, func() bool { return !old.Running() })

	// A dedicated port joins: a new backend core; the transfer on the local port goes on.
	resp = transfer(a.ProxyPort())
	old = backend
	dedicated, _ := freePort()
	if err := a.SetNodePort(first, dedicated); err != nil {
		t.Fatal(err)
	}
	settle()
	cores("a dedicated port", true)
	finishes("a dedicated port", transfer(dedicated))
	finishes("the local port beside a dedicated port", resp)
	eventually(t, posts, func() bool { return !old.Running() && running() == 2 })

	// A new core: its files are replaced under the running cores; a new backend core starts from them,
	// the front core goes on.
	resp = transfer(a.ProxyPort())
	old = a.run.core
	a.coreJob("install", "安装", func(*http.Client) (string, error) { return root, nil })
	if a.Conn != StateOn {
		t.Fatal("preparing the core touched the connection")
	}
	waitRegression(t, posts, func() bool { return !a.CoreBusy && a.run.core != old })
	settle()
	if a.CoreErr != "" {
		t.Fatal(a.CoreErr)
	}
	cores("a new core", true)
	finishes("a new core", resp)
	eventually(t, posts, func() bool { return !old.Running() && running() == 2 })
	if !listening(a.ProxyPort()) {
		t.Fatal("the local port closed")
	}

	// Another node ends the connections made before, those on a backend core still finishing them too.
	resp = transfer(a.ProxyPort(), "hold")
	draining := a.run.core
	a.RenewCores()
	settle()
	cores("new rule data again", true)
	if !draining.Running() || running() != 3 {
		t.Fatalf("the old backend core stopped with a transfer: %d cores", running())
	}
	a.Connect(first, nil)
	settle()
	cores("another node after new rule data", true)
	if a.ActiveID != first || draining.Running() || running() != 2 {
		t.Fatalf("another node: id %s; the draining backend core stops at once: %v; %d cores", a.ActiveID, draining.Running(), running())
	}
	close(resume)
	if n, err := io.Copy(io.Discard, resp.Body); err == nil && n == 1024 {
		t.Fatal("a transfer through the node before outlived another node")
	}
	resp.Body.Close()

	// Disconnected: the cores serve the dedicated port alone, the local port closed. What the backend core
	// carried ends with the connection, the dedicated port's transfers too, which then start again.
	resp = transfer(dedicated)
	local := a.ProxyPort()
	a.Disconnect()
	settle()
	if n, err := io.Copy(io.Discard, resp.Body); err == nil && n == 9*1024 {
		t.Fatal("a transfer outlived the connection's backend core")
	}
	resp.Body.Close()
	if a.Conn != StateOff || a.front.PID() != front || listening(local) || !listening(dedicated) {
		t.Fatalf("disconnected: state %v front %d→%d local %v", a.Conn, front, a.front.PID(), listening(local))
	}
	eventually(t, posts, func() bool { return running() == 2 })
	finishes("the dedicated port disconnected", transfer(dedicated))
	// Connected again beside it, then without it the cores stop with the connection.
	a.Connect(first, nil)
	settle()
	if a.Conn != StateOn || a.front.PID() != front {
		t.Fatalf("connected beside the dedicated port: %v %q", a.Conn, a.ConnErr)
	}
	if err := a.SetNodePort(first, 0); err != nil {
		t.Fatal(err)
	}
	settle()
	if listening(dedicated) {
		t.Fatal("the dedicated port removed still listens")
	}

	// The backend core exiting ends the connection, every core included.
	proc, err := os.FindProcess(a.run.core.PID())
	if err != nil {
		t.Fatal(err)
	}
	proc.Kill()
	waitRegression(t, posts, func() bool { return a.Conn == StateOff })
	settle()
	if !strings.Contains(a.ConnErr, "核心") || coresRunning(a) {
		t.Fatalf("after the backend core exited: err %q running %v", a.ConnErr, coresRunning(a))
	}
	a.Shutdown()
	drainRegression(posts)
}

// eventually waits for cond, which no posted result need change, running those posted meanwhile.
func eventually(t *testing.T, posts chan func(), cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		select {
		case f := <-posts:
			f()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func logTexts(a *App) []string {
	a.FlushLogs()
	var out []string
	for _, l := range a.Logs {
		out = append(out, l.Text)
	}
	return out
}

// Uses only local processes/listeners: no system proxy, TUN adapter or external requests.
func TestIntegrationConnectionOperationOrder(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.State.Settings.Port = l.Addr().(*net.TCPAddr).Port
	l.Close()
	a.State.Settings.SystemProxy = false
	a.ImportLinks("socks://127.0.0.1:9#a\nsocks://127.0.0.1:10#b", "")
	first, second := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	for range 5 {
		a.Connect(first, nil)
		a.Connect(second, nil)
		waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
		if a.Conn != StateOn || a.ActiveID != second || !a.front.Running() {
			t.Fatalf("latest connection not committed: state=%v id=%s error=%q", a.Conn, a.ActiveID, a.ConnErr)
		}
		a.Disconnect()
		a.Connect(first, nil)
		waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
		if a.Conn != StateOn || a.ActiveID != first || !a.front.Running() {
			t.Fatalf("older Stop killed latest Start: %q", a.ConnErr)
		}
		a.Disconnect()
		waitOperation(t, a.coreDo(func() {}))
		drainRegression(posts)
		if coresRunning(a) {
			t.Fatal("disconnected core still running")
		}
		l, err = net.Listen("tcp", a.ProxyAddr())
		if err != nil {
			t.Fatalf("listener not released: %v", err)
		}
		l.Close()
	}
	// Shutdown must drain queued starts as well, without a late core after return.
	a.Connect(first, nil)
	a.Shutdown()
	drainRegression(posts)
	if coresRunning(a) {
		t.Fatal("core started after shutdown")
	}
	if _, err := os.Stat(filepath.Join(a.DataDir, "config.json")); err != nil {
		t.Fatal("real core config was never written")
	}
}

// A change the cores fail to reach leaves the connection on what runs, the failure shown, however many
// changes were planned on it meanwhile: a node Xray refuses, then another local port; a new backend core
// whose port is taken, then another local port.
func TestIntegrationFailedChangeKeepsWhatRuns(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	for _, backend := range []bool{false, true} {
		a, posts, _ := newRegressionApp(t)
		if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
			t.Fatal(err)
		}
		a.State.Settings.Port, _ = freePort()
		a.State.Settings.SystemProxy = false
		a.ImportLinks("socks://127.0.0.1:9#working", "")
		first := a.State.Nodes[0].ID
		a.ImportLinks(`{"protocol":"socks","settings":{"address":"127.0.0.1","port":10},"streamSettings":{"network":"no-such-transport"}}`, "")
		broken := a.State.Nodes[1].ID
		a.Connect(first, nil)
		waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
		if a.Conn != StateOn {
			t.Fatal(a.ConnErr)
		}
		live, port := a.planned, a.runningPort
		gate, entered := make(chan struct{}), make(chan struct{})
		a.coreDo(func() { close(entered); <-gate })
		<-entered
		if backend {
			a.State.Settings.DirectDNS = "1.1.1.1" // a new backend core
			a.Reconnect()
			blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.run.b.Mixed))
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
		} else {
			a.Connect(broken, nil)
		}
		a.State.Settings.Port, _ = freePort()
		a.Reconnect()
		close(gate)
		waitOperation(t, a.coreDo(func() {}))
		drainRegression(posts)
		if a.Conn != StateOn || a.ConnErr == "" || a.ActiveID != first || a.State.LastID != first || a.runningPort != port {
			t.Fatalf("backend %v: state %v active %q last %q port %d→%d err %q", backend, a.Conn, a.ActiveID, a.State.LastID, port, a.runningPort, a.ConnErr)
		}
		if a.planned != live || !live.run.core.Running() || !a.front.Running() || !a.SettingsDirty {
			t.Fatalf("backend %v: not back on the cores that run, or the settings not left pending", backend)
		}
		a.procs.mu.Lock()
		n := len(a.procs.cores)
		a.procs.mu.Unlock()
		if n != 1 {
			t.Fatalf("backend %v: %d backend cores, want the one that runs", backend, n)
		}
		// What failed is not left in the way of the next change.
		a.State.Settings.DirectDNS = "223.5.5.5"
		a.Reconnect()
		waitOperation(t, a.coreDo(func() {}))
		drainRegression(posts)
		if a.Conn != StateOn || a.ConnErr != "" || a.runningPort != a.State.Settings.Port {
			t.Fatalf("backend %v: the next change: state %v port %d err %q", backend, a.Conn, a.runningPort, a.ConnErr)
		}
	}
}
