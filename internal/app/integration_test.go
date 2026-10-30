//go:build integration

package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"raydock/internal/probe"
	"raydock/internal/xray"
)

// The core restarts for every change of what it serves (another node, the routing mode, another local
// port, new rule data, an edited node, the dedicated ports, a new core): the connections it carried end,
// new ones go through the change. Without a connection it serves the dedicated ports alone. A core that
// exits ends the connection.
func TestIntegrationRestartOnChange(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.ReadCoreVersion = xray.Version
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("state=%v connection=%q core=%q hold=%v wait=%v logs:\n%s", a.Conn, a.ConnErr, a.CoreErr, a.coreHold, a.WaitCore, strings.Join(logTexts(a), "\n"))
		}
	})
	// The nodes: SOCKS inbounds of a local server core, which may reach local targets.
	var servers [3]int
	var inbounds []string
	for i := range servers {
		servers[i], _ = freePort()
		inbounds = append(inbounds, fmt.Sprintf(`{"listen":"127.0.0.1","port":%d,"protocol":"socks"}`, servers[i]))
	}
	server := xray.Core{}
	if err := server.Start(context.Background(), filepath.Join(a.CoreDir(), xray.ExeName()), filepath.Join(t.TempDir(), "server.json"), []byte(`{"log":{"loglevel":"warning"},
		"inbounds":[`+strings.Join(inbounds, ",")+`],"outbounds":[{"protocol":"freedom"}]}`), nil); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	port, _ := freePort()
	a.State.Settings.Port = port
	a.State.Settings.SystemProxy = false
	a.State.Settings.ProxyRules = "127.0.0.1/32" // local targets go through the nodes
	a.ImportLinks(fmt.Sprintf("socks://127.0.0.1:%d#a\nsocks://127.0.0.1:%d#b", servers[0], servers[1]), "")
	first, second := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	listening := func(port int) bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	// running counts the core processes.
	running := func() int {
		a.procs.mu.Lock()
		defer a.procs.mu.Unlock()
		n := 0
		for _, c := range a.procs.cores {
			if c.Running() {
				n++
			}
		}
		return n
	}

	// Include the overlap, not just the steady state, in the process-count assertion.
	var peak atomic.Int32
	stopCount, countDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(countDone)
		for {
			n := int32(running())
			if n > peak.Load() {
				peak.Store(n)
			}
			select {
			case <-stopCount:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() {
		close(stopCount)
		<-countDone
		want := int32(1)
		if xray.Overlap {
			want = 2
		}
		if peak.Load() != want {
			t.Errorf("peak running client cores = %d, want %d", peak.Load(), want)
		}
	})

	// Hold the response after its first KB until the assertion releases it: however long a real core
	// takes to start (especially under -race), a transfer cannot finish before retirement by itself.
	var gates sync.Map
	stream := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gate, ok := gates.Load(r.URL.Path)
		if !ok {
			http.Error(w, "unknown transfer", http.StatusBadRequest)
			return
		}
		defer gates.Delete(r.URL.Path)
		w.Write(make([]byte, 1024))
		w.(http.Flusher).Flush()
		select {
		case <-gate.(chan struct{}):
			w.Write(make([]byte, 9*1024))
		case <-r.Context().Done():
		}
	})}
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go stream.Serve(sl)
	defer stream.Close()
	type inFlight struct {
		resp    *http.Response
		release func()
	}
	var transferID int
	transfer := func(port int) inFlight { // through the core and the node
		t.Helper()
		transferID++
		path, gate := fmt.Sprintf("/%d", transferID), make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		t.Cleanup(release)
		gates.Store(path, gate)
		resp, err := probe.ProxyClient(port).Get("http://" + sl.Addr().String() + path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		if _, err := io.ReadFull(resp.Body, make([]byte, 1024)); err != nil {
			t.Fatal(err)
		}
		return inFlight{resp, release}
	}
	finishes := func(f inFlight) bool {
		f.release()
		defer f.resp.Body.Close()
		n, err := io.Copy(io.Discard, f.resp.Body)
		return err == nil && n == 9*1024
	}
	core := func() *xray.Core {
		if r := a.running.core(); r != nil {
			return r.core
		}
		return nil
	}
	start := time.Now()
	a.Connect(first, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	if a.Conn != StateOn || core().PID() == 0 || running() != 1 {
		t.Fatalf("connect: %v %q, %d cores", a.Conn, a.ConnErr, running())
	}
	t.Logf("connect: %v", time.Since(start).Round(time.Millisecond))
	// restarted carries out a change made with a transfer under way: the core restarts, the new one beside
	// the old one until it runs, and the transfer ends.
	restarted := func(what string, change func()) {
		t.Helper()
		resp, old := transfer(a.ProxyPort()), core()
		start := time.Now()
		change()
		waitRegression(t, posts, func() bool { return a.ConnErr != "" || a.Conn == StateOn && core() != nil && core() != old })
		if a.Conn != StateOn || a.ConnErr != "" || core() == old || old.Running() || running() != 1 {
			t.Fatalf("%s: state %v err %q, new core %v", what, a.Conn, a.ConnErr, core() != old)
		}
		t.Logf("%s: %v", what, time.Since(start).Round(time.Millisecond))
		if finishes(resp) {
			t.Fatalf("%s: a transfer outlived the core restarted", what)
		}
		if !finishes(transfer(a.ProxyPort())) {
			t.Fatalf("%s: a transfer after the restart ended early", what)
		}
	}

	since := a.Since
	restarted("another node", func() { a.Connect(second, nil) })
	if a.ActiveID != second || !a.Since.After(since) {
		t.Fatal("another node: connected since it was switched to")
	}
	since, key := a.Since, a.OverviewKey()
	restarted("a routing mode", func() { a.SetMode(xray.ModeGlobal) })
	if a.Since != since || a.OverviewKey() == key {
		t.Fatalf("a routing mode: since %v → %v, key %s → %s", since, a.Since, key, a.OverviewKey())
	}
	if logs := strings.Join(logTexts(a), "\n"); !strings.Contains(logs, "切换到 b") || !strings.Contains(logs, "应用变更，重新启动核心，已建立的连接会断开") {
		t.Fatalf("logs %q", logs)
	}

	oldPort := port
	a.State.Settings.Port, _ = freePort()
	a.SettingChanged()
	if !a.SettingsDirty {
		t.Fatal("a new port waits for a reconnect")
	}
	restarted("another port", a.Reconnect)
	if a.ProxyPort() != a.State.Settings.Port || !listening(a.ProxyPort()) || listening(oldPort) || a.SettingsDirty {
		t.Fatalf("another port: %d (listening %v), old %d still %v", a.ProxyPort(), listening(a.ProxyPort()), oldPort, listening(oldPort))
	}
	restarted("new rule data", func() { a.RenewCores(change{geo: true}) })
	restarted("a node edit", func() {
		n := *a.NodeByID(second)
		n.Port = servers[2]
		if err := a.EditNode(n); err != nil {
			t.Fatal(err)
		}
	})

	// A dedicated port: the core restarts serving it beside the connection.
	dedicated, _ := freePort()
	restarted("a dedicated port", func() {
		if err := a.SetNodePort(first, dedicated); err != nil {
			t.Fatal(err)
		}
	})
	if !listening(dedicated) || running() != 1 || !finishes(transfer(dedicated)) {
		t.Fatalf("the dedicated port: %d cores", running())
	}
	// Another node restarts the core, the dedicated port's transfers with it.
	resp := transfer(dedicated)
	restarted("back to the first node", func() { a.Connect(first, nil) })
	if finishes(resp) || running() != 1 {
		t.Fatal("a transfer through the dedicated port outlived the core restarted")
	}
	restarted("a new core", func() {
		a.coreJob("install", "安装", func(*http.Client) (string, error) { return root, nil })
	})
	if a.CoreErr != "" || running() != 1 {
		t.Fatalf("a new core: %q, %d cores", a.CoreErr, running())
	}

	// Disconnected: the core restarts serving the dedicated port alone.
	local := a.ProxyPort()
	a.Disconnect()
	eventually(t, posts, func() bool { return a.running != nil && a.running.id == "" && core() != nil && listening(dedicated) })
	if a.Conn != StateOff || listening(local) || running() != 1 || !finishes(transfer(dedicated)) {
		t.Fatalf("disconnected: state %v local %v, %d cores", a.Conn, listening(local), running())
	}
	// Connected again beside it, then without it.
	a.Connect(first, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	if a.Conn != StateOn || !listening(dedicated) || running() != 1 {
		t.Fatalf("connected beside the dedicated port: %v %q", a.Conn, a.ConnErr)
	}
	restarted("no dedicated port", func() {
		if err := a.SetNodePort(first, 0); err != nil {
			t.Fatal(err)
		}
	})
	if listening(dedicated) || running() != 1 {
		t.Fatal("the dedicated port should be gone")
	} // The core exiting ends the connection.
	proc, err := os.FindProcess(core().PID())
	if err != nil {
		t.Fatal(err)
	}
	proc.Kill()
	waitRegression(t, posts, func() bool { return a.Conn == StateOff })
	waitOperation(t, a.coreDo(func() {}))
	drainRegression(posts)
	if !strings.Contains(a.ConnErr, "核心") || coresRunning(a) {
		t.Fatalf("after the core exited: err %q running %v", a.ConnErr, coresRunning(a))
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
		if a.Conn != StateOn || a.ActiveID != second || !coresRunning(a) {
			t.Fatalf("latest connection not committed: state=%v id=%s error=%q", a.Conn, a.ActiveID, a.ConnErr)
		}
		a.Disconnect()
		a.Connect(first, nil)
		waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
		if a.Conn != StateOn || a.ActiveID != first || !coresRunning(a) {
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

// A restart the core fails ends the connection with the reason, the old core already stopped: a node Xray
// refuses, a local port another program took. The next change starts afresh.
func TestIntegrationFailedRestartEnds(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	for _, taken := range []bool{false, true} {
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
		if taken {
			a.State.Settings.Port, _ = freePort()
			blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.State.Settings.Port))
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
			a.Reconnect()
		} else {
			a.Connect(broken, nil)
		}
		waitRegression(t, posts, func() bool { return a.Conn == StateOff })
		waitOperation(t, a.coreDo(func() {}))
		drainRegression(posts)
		if a.ConnErr == "" || coresRunning(a) {
			t.Fatalf("taken %v: err %q, running %v", taken, a.ConnErr, coresRunning(a))
		}
		// What failed is not left in the way of the next connection.
		a.State.Settings.Port, _ = freePort()
		a.Connect(first, nil)
		waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
		if a.Conn != StateOn || a.ConnErr != "" {
			t.Fatalf("taken %v: the next connection: state %v err %q", taken, a.Conn, a.ConnErr)
		}
	}
}
