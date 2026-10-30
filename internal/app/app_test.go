package app_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
	"raydock/internal/node"
	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/system"
	"raydock/internal/xray"
)

func TestImportAndCoreFailure(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	h.Clip.Text = apptest.LinkA + "\n" + apptest.LinkB + "\nnot-a-link\n"
	if msg := a.ImportLinks(h.Clip.Text, "").String(); len(a.State.Nodes) != 2 || !strings.Contains(msg, "1 条无法识别") {
		t.Fatalf("import: %s, %d nodes", msg, len(a.State.Nodes))
	}
	// Importing again adds nothing.
	if msg := a.ImportLinks(apptest.LinkA, "").String(); !strings.Contains(msg, "已存在") || len(a.State.Nodes) != 2 {
		t.Fatalf("reimport: %s, %d nodes", msg, len(a.State.Nodes))
	}
	a.State.SelectedID = a.State.Nodes[1].ID
	a.Save()
	// Without a core, connecting downloads it first; a failed download ends the connect with the reason.
	h.Core.Err = errors.New("HTTP 403")
	a.Connect(a.State.SelectedID, nil)
	if a.Conn != app.StateConnecting || !a.WaitCore || !a.CoreBusy {
		t.Fatalf("should wait for the core: conn %v waitCore %v busy %v", a.Conn, a.WaitCore, a.CoreBusy)
	}
	h.Wait(t, func() bool { return !a.CoreBusy })
	if a.Conn != app.StateOff || !strings.Contains(a.ConnErr, "HTTP 403") || a.CoreErr == "" {
		t.Fatalf("conn %v err %q", a.Conn, a.ConnErr)
	}
	st, err := store.Load(a.StatePath())
	if err != nil || len(st.Nodes) != 2 || st.SelectedID != a.State.SelectedID {
		t.Fatalf("saved state %+v %v", st, err)
	}
}

// Connecting without picking a node uses the selected one; with none selected, or once it is gone, the
// one last connected to.
func TestConnectTargetIsSelected(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	first, second := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	a.State.SelectedID = second
	if got := a.ConnectTarget(); got != second {
		t.Fatalf("before any connection: %q, want the selected %q", got, second)
	}
	h.Core.Err = errors.New("HTTP 403")
	a.Connect(first, nil)
	h.Wait(t, func() bool { return !a.CoreBusy })
	a.State.SelectedID = second // picked in the list, not connected
	a.Save()
	if got := a.ConnectTarget(); got != second {
		t.Fatalf("after selecting another row: %q, want the selected %q", got, second)
	}
	if st, err := store.Load(a.StatePath()); err != nil || st.LastID != first || st.SelectedID != second {
		t.Fatalf("saved last %q selected %q %v", st.LastID, st.SelectedID, err)
	}
	a.State.SelectedID = ""
	if got := a.ConnectTarget(); got != first {
		t.Fatalf("none selected: %q, want the last connected %q", got, first)
	}
	a.State.SelectedID = "gone"
	if got := a.ConnectTarget(); got != first {
		t.Fatalf("selected node gone: %q, want the last connected %q", got, first)
	}
}

func TestTUNNeedsElevation(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	a.State.SelectedID = a.State.Nodes[0].ID
	a.SetTUN(true)
	if !a.State.Settings.TUN.Enabled || !a.AdminPrompt {
		t.Fatalf("TUN %v prompt %v", a.State.Settings.TUN.Enabled, a.AdminPrompt)
	}
	// Connecting with TUN: the core does not start, elevation is offered.
	a.AdminPrompt = false
	a.Connect(a.State.SelectedID, nil)
	if a.Conn != app.StateOff || !a.AdminPrompt || a.ConnErr == "" {
		t.Fatalf("conn %v prompt %v err %q", a.Conn, a.AdminPrompt, a.ConnErr)
	}
	// Already elevated: connecting proceeds.
	a.AdminPrompt, a.Elevated = false, true
	a.Connect(a.State.SelectedID, nil)
	if a.Conn != app.StateConnecting || a.AdminPrompt {
		t.Fatalf("conn %v prompt %v", a.Conn, a.AdminPrompt)
	}
	a.Disconnect()
	h.Wait(t, func() bool { return !a.CoreBusy }) // let the download finish before the temp dir is removed
}

func TestAutostart(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	h.Start.Mode = system.StartUser
	a.LoadAutostart()
	h.Wait(t, func() bool { return !a.StartBusy })
	if a.StartMode != system.StartUser {
		t.Fatalf("mode %v", a.StartMode)
	}
	a.SetAutostart(system.StartAdmin)
	h.Wait(t, func() bool { return !a.StartBusy })
	if a.StartMode != system.StartAdmin || len(h.Start.Sets) != 1 {
		t.Fatalf("mode %v sets %v", a.StartMode, h.Start.Sets)
	}
	// A declined UAC prompt keeps the mode the system reports.
	h.Start.Err = system.ErrUACCancelled
	a.SetAutostart(system.StartOff)
	h.Wait(t, func() bool { return !a.StartBusy })
	if a.StartMode != system.StartAdmin || !strings.Contains(a.StartErr, "UAC") {
		t.Fatalf("mode %v err %q", a.StartMode, a.StartErr)
	}
}

func TestSystemProxyRestore(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	prev := sysproxy.Settings{Enabled: true, Server: "10.0.0.1:3128", Override: "<local>"}
	h.Proxy.Cur = prev
	a.Conn = app.StateOn
	a.SyncProxy()
	ours := a.ProxyAddr()
	if !h.Proxy.Cur.Enabled || h.Proxy.Cur.Server != ours || !a.ProxyActive() {
		t.Fatalf("after enabling %+v", h.Proxy.Cur)
	}
	// The previous settings reach the disk before writing.
	if st, _ := store.Load(a.StatePath()); st.ProxyBackup == nil || st.ProxyBackup.Previous != prev {
		t.Fatalf("backup on disk %+v", st.ProxyBackup)
	}
	a.State.Settings.SystemProxy = false
	a.SyncProxy()
	if h.Proxy.Cur != prev || a.ProxyActive() {
		t.Fatalf("disabling should restore the previous settings, got %+v", h.Proxy.Cur)
	}

	// Simulated crash: the backup is saved and the proxy still points at us; the next start restores it.
	a.State.Settings.SystemProxy = true
	a.SyncProxy()
	st, _ := store.Load(a.StatePath())
	h2 := apptest.New(t)
	h2.Proxy.Cur = h.Proxy.Cur
	h2.A.State = st
	h2.A.CleanStaleProxy()
	if h2.Proxy.Cur != prev || h2.A.ProxyActive() {
		t.Fatalf("not restored at start: %+v", h2.Proxy.Cur)
	}

	// Another program changed the proxy after us: disconnecting leaves it alone.
	other := sysproxy.Settings{Enabled: true, Server: "127.0.0.1:7890"}
	h.Proxy.Cur = other
	a.Disconnect()
	if h.Proxy.Cur != other || a.ProxyActive() {
		t.Fatalf("another program's proxy must not change: %+v", h.Proxy.Cur)
	}
}

func TestSubscriptionRefreshKeepsIDs(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte(apptest.LinkA + "\n" + apptest.LinkB))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "v2rayN") {
			t.Errorf("User-Agent %q", r.UserAgent())
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	h := apptest.New(t)
	a := h.A
	id, err := a.AddGroup(app.GroupInput{URL: srv.URL + "/sub", Subscription: true})
	if err != nil {
		t.Fatal(err)
	}
	h.Wait(t, func() bool { return !a.SubBusy[id] })
	s := a.GroupByID(id)
	if s.Count != 2 || s.LastError != "" || !strings.HasPrefix(s.Name, "127.0.0.1:") || len(a.State.Nodes) != 2 || a.State.Nodes[0].GroupID != id {
		t.Fatalf("subscription %+v, %d nodes", s, len(a.State.Nodes))
	}
	if msg := a.ImportLinks(apptest.LinkA, id).String(); !strings.Contains(msg, "不能手动添加") || len(a.State.Nodes) != 2 {
		t.Fatalf("manual import into a subscription: %s", msg)
	}
	keep := a.State.Nodes[1].ID
	a.State.SelectedID = keep

	// Only B after the update, beside an entry that cannot be used: B is kept, the entry skipped and logged.
	body = base64.StdEncoding.EncodeToString([]byte(apptest.LinkB + "\ntuic://unsupported@example.com:443#skip-me"))
	a.UpdateSubscription(id)
	h.Wait(t, func() bool { return !a.SubBusy[id] })
	if len(a.State.Nodes) != 1 || a.State.Nodes[0].ID != keep {
		t.Fatalf("B should keep its ID: %+v", a.State.Nodes)
	}
	if s := a.GroupByID(id); s.Count != 1 || s.Skipped != 1 || s.LastError != "" {
		t.Fatalf("a skipped entry does not fail the update: %+v", s)
	}
	a.FlushLogs()
	if !slices.ContainsFunc(a.Logs, func(l app.LogLine) bool { return strings.Contains(l.Text, "跳过") }) {
		t.Fatalf("the skipped entry should be logged: %+v", a.Logs)
	}
	// Nothing usable fails the update and keeps the nodes.
	body = base64.StdEncoding.EncodeToString([]byte("tuic://unsupported@example.com:443#skip-me"))
	a.UpdateSubscription(id)
	h.Wait(t, func() bool { return !a.SubBusy[id] })
	if s := a.GroupByID(id); s.LastError == "" || len(a.State.Nodes) != 1 || a.State.Nodes[0].ID != keep {
		t.Fatalf("a failed update keeps the nodes: %+v, %d nodes", s, len(a.State.Nodes))
	}
	a.DeleteGroup(id)
	if len(a.State.Groups) != 0 || len(a.State.Nodes) != 0 {
		t.Fatalf("after deleting: %d groups, %d nodes", len(a.State.Groups), len(a.State.Nodes))
	}
	if _, err := a.AddGroup(app.GroupInput{Name: "x", URL: "ftp://example.com", Subscription: true}); err == nil {
		t.Error("non-http URL should fail")
	}
	if _, err := a.AddGroup(app.GroupInput{Subscription: true}); err == nil {
		t.Error("a subscription needs a URL")
	}
}

// A connect waiting for the core continues once it is downloaded.
func TestConnectWaitsForCore(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	a.Connect(a.State.Nodes[0].ID, nil)
	if !a.WaitCore {
		t.Fatal("should wait for the core")
	}
	// Placeholders are not executables: the start error proves the connect reached starting the core.
	h.Wait(t, func() bool { return !a.CoreBusy && a.Conn == app.StateOff })
	if !a.CoreReady() || h.Core.Installs.Load() != 1 || a.WaitCore || !strings.Contains(a.ConnErr, "核心") {
		t.Fatalf("ready %v installs %d waitCore %v err %q", a.CoreReady(), h.Core.Installs.Load(), a.WaitCore, a.ConnErr)
	}
	// Files present but not runnable (placeholders): the startup check treats them as broken and downloads again.
	a.EnsureCore()
	h.Wait(t, func() bool { return h.Core.Installs.Load() == 2 && !a.CoreBusy })
}

// Startup never looks for a newer core; checking offers one and installs it only when asked again.
func TestCoreUpdatesOnlyWhenAsked(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	if err := os.MkdirAll(a.CoreDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(a.CoreDir(), name), []byte("core"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.ReadCoreVersion = func(string) (string, error) { return "26.3.27", nil }
	h.Core.Tag = "v26.10.10"
	a.EnsureCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	if h.Core.Lookups.Load() != 0 || h.Core.Installs.Load() != 0 || a.CoreLatest != "" || a.CoreVersion != "26.3.27" {
		t.Fatalf("startup: lookups %d installs %d latest %q version %q", h.Core.Lookups.Load(), h.Core.Installs.Load(), a.CoreLatest, a.CoreVersion)
	}
	a.CheckCoreUpdate()
	h.Wait(t, func() bool { return !a.CoreChecking })
	if a.CoreLatest != "v26.10.10" || h.Core.Installs.Load() != 0 {
		t.Fatalf("check: latest %q installs %d", a.CoreLatest, h.Core.Installs.Load())
	}
	a.CoreLatest, h.Core.Tag = "", "v26.3.27"
	a.CheckCoreUpdate()
	h.Wait(t, func() bool { return !a.CoreChecking })
	if a.CoreLatest != "" || a.CoreNote != "已是最新版本" || h.Core.Installs.Load() != 0 {
		t.Fatalf("up to date: latest %q note %q installs %d", a.CoreLatest, a.CoreNote, h.Core.Installs.Load())
	}
}

// With a bundled core, startup copies it instead of downloading.
func TestBundledCore(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.BundledCore = t.TempDir()
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(a.BundledCore, name), []byte("bundled"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.ImportLinks(apptest.LinkA, "")
	a.EnsureCore()
	a.Connect(a.State.Nodes[0].ID, nil) // connect while copying, waiting for the core
	if !a.CoreBusy || !a.WaitCore {
		t.Fatalf("busy %v waitCore %v", a.CoreBusy, a.WaitCore)
	}
	h.Wait(t, func() bool { return !a.CoreBusy && a.Conn == app.StateOff })
	if !a.CoreReady() || h.Core.Installs.Load() != 0 {
		t.Fatalf("ready %v downloads %d", a.CoreReady(), h.Core.Installs.Load())
	}
	if b, _ := os.ReadFile(a.CoreExe()); string(b) != "bundled" {
		t.Fatalf("core in data dir %q", b)
	}
}

// Route for core and subscription downloads: our proxy when connected, otherwise another program's system proxy.
func TestUpstreamProxy(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	if u := a.UpstreamProxy(); u != nil {
		t.Errorf("no system proxy should mean direct: %v", u)
	}
	h.Proxy.Cur = sysproxy.Settings{Enabled: true, Server: "127.0.0.1:10808"}
	if u := a.UpstreamProxy(); u == nil || u.Host != "127.0.0.1:10808" {
		t.Errorf("should use the system proxy: %v", u)
	}
	h.Proxy.Cur.Server = "http=127.0.0.1:1;https=127.0.0.1:2"
	if u := a.UpstreamProxy(); u != nil {
		t.Errorf("per-protocol proxy settings should be skipped: %v", u)
	}
	a.Conn = app.StateOn
	if u := a.UpstreamProxy(); u == nil || u.Host != a.ProxyAddr() {
		t.Errorf("connected should use our proxy: %v", u)
	}
}

func TestGroupsAndChain(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	// Without a group, imports go to a new default group.
	a.ImportLinks(apptest.LinkA, "")
	if len(a.State.Groups) != 1 || a.State.Groups[0].Name != store.DefaultGroup || a.State.Nodes[0].GroupID != a.State.Groups[0].ID {
		t.Fatalf("groups %+v nodes %+v", a.State.Groups, a.State.Nodes)
	}
	home := a.State.Groups[0].ID
	relay, err := a.AddGroup(app.GroupInput{Name: " 中转 "})
	if err != nil || a.GroupByID(relay).Name != "中转" {
		t.Fatalf("add group %v", err)
	}
	if _, err := a.AddGroup(app.GroupInput{Name: "  "}); err == nil {
		t.Error("a normal group needs a name")
	}
	a.ImportLinks(apptest.LinkB+"\n"+"socks://dXNlcjpwYXNz@10.0.0.2:1080#出口", relay)
	// The same server may sit in two groups.
	if msg := a.ImportLinks(apptest.LinkA, relay).String(); strings.Contains(msg, "已存在") || len(a.State.Nodes) != 4 {
		t.Fatalf("import into another group: %s", msg)
	}
	nodeA, front, back := a.State.Nodes[0].ID, a.State.Nodes[1].ID, a.State.Nodes[2].ID

	if err := a.EditGroup(home, app.GroupInput{Name: "家", Front: nodeA}); err == nil {
		t.Error("a proxy cannot be a node of the group itself")
	}
	if err := a.EditGroup(home, app.GroupInput{Name: "家", Front: "missing"}); err == nil {
		t.Error("a proxy must exist")
	}
	if err := a.EditGroup(home, app.GroupInput{Name: "家", Front: front, Back: back}); err != nil {
		t.Fatal(err)
	}
	chain, err := a.Chain(a.NodeByID(nodeA))
	if err != nil || len(chain) != 3 || chain[0].ID != front || chain[1].ID != nodeA || chain[2].ID != back {
		t.Fatalf("chain %v %v", chain, err)
	}
	// Proxies use only their own settings: the relay group has none, so its nodes connect alone.
	if chain, _ := a.Chain(a.NodeByID(front)); len(chain) != 1 {
		t.Fatalf("relay chain %d", len(chain))
	}
	// Changing the chain of the connection in use applies at once (TestChangesApplyInPlace), not on reconnecting.
	a.Conn, a.ActiveID = app.StateOn, nodeA
	a.EditGroup(home, app.GroupInput{Name: "家", Front: front})
	if a.SettingsDirty || a.GroupByID(home).Back != "" {
		t.Fatalf("dirty %v group %+v", a.SettingsDirty, a.GroupByID(home))
	}
	a.Conn, a.ActiveID, a.SettingsDirty = app.StateOff, "", false

	// A front proxy that vanished (e.g. dropped by a subscription update) stops connecting with a reason.
	a.GroupByID(home).Front = "gone"
	a.Connect(nodeA, nil)
	if a.Conn != app.StateOff || !strings.Contains(a.ConnErr, "前置代理节点已不存在") {
		t.Fatalf("conn %v err %q", a.Conn, a.ConnErr)
	}
	// Deleting a required hop preserves its invalid reference and fails closed.
	a.GroupByID(home).Front = front
	a.DeleteNode(front)
	if a.GroupByID(home).Front != front {
		t.Fatal("required front proxy reference must not be silently cleared")
	}
	if _, err := a.Chain(a.NodeByID(nodeA)); err == nil {
		t.Fatal("deleting a hop must invalidate the chain")
	}
	a.DeleteGroup(relay)
	if len(a.State.Groups) != 1 || len(a.State.Nodes) != 1 {
		t.Fatalf("after deleting the relay: %d groups, %d nodes", len(a.State.Groups), len(a.State.Nodes))
	}
}

func TestSpeedTest(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	ids := []string{a.State.Nodes[0].ID, a.State.Nodes[1].ID}
	if err := a.TestSpeed(ids); err == nil || a.SpeedBusy {
		t.Fatal("speed tests need the core")
	}
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	// The placeholders are not executables: every test reports the core failing to start, one after another.
	if err := a.TestSpeed(ids); err != nil || !a.SpeedBusy || a.Speed[ids[1]].Phase != "排队" {
		t.Fatalf("start %v busy %v speed %+v", err, a.SpeedBusy, a.Speed)
	}
	if err := a.TestSpeed(ids); err == nil {
		t.Error("only one speed test at a time")
	}
	h.Wait(t, func() bool { return !a.SpeedBusy })
	for _, id := range ids {
		if s := a.Speed[id]; s.Testing() || s.Err != "核心启动失败" {
			t.Fatalf("%s: %+v", id, s)
		}
	}
	// A node with a broken chain fails without starting a core.
	a.State.Groups[0].Back = "gone"
	a.TestSpeed(ids[:1])
	h.Wait(t, func() bool { return !a.SpeedBusy })
	if s := a.Speed[ids[0]]; s.Err != "代理链无效" {
		t.Fatalf("broken chain %+v", s)
	}
}

func TestLatencyTest(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\nhysteria2://pw@d.example.com:443#Hy2", "")
	ids := []string{a.State.Nodes[0].ID, a.State.Nodes[1].ID}
	if err := a.TestLatency(ids); err == nil || a.LatencyBusy {
		t.Fatal("latency tests need the core")
	}
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	if err := a.TestLatency(ids); err != nil || !a.LatencyBusy || !a.Latency[ids[0]].Testing {
		t.Fatalf("start %v busy %v %+v", err, a.LatencyBusy, a.Latency)
	}
	if !a.Latency[ids[1]].Testing {
		t.Errorf("a UDP node is measured too: %+v", a.Latency[ids[1]])
	}
	if err := a.TestLatency(ids); err == nil {
		t.Error("one latency test at a time")
	}
	h.Wait(t, func() bool { return !a.LatencyBusy })
	// The placeholder core cannot start a resolver on Windows; elsewhere the name does not resolve.
	if l := a.Latency[ids[0]]; l.Testing || l.Err == "" {
		t.Fatalf("%+v", l)
	}
	// The UDP node's test core is the placeholder too: it fails, and the test ends.
	if l := a.Latency[ids[1]]; l.Testing || l.Err != "失败" {
		t.Fatalf("UDP node %+v", l)
	}
	if _, err := os.Stat(filepath.Join(a.DataDir, "latency.json")); err != nil {
		t.Fatalf("the UDP test core was not configured: %v", err)
	}
}

// Nodes filled in by hand go where imports go, checked like imports; edits keep the node's identity.
func TestAddAndEditNode(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	id, err := a.AddNode("", node.Node{Name: "手动", Protocol: node.Hysteria2, Host: "h.example.com", Port: 443, Password: "pw",
		Security: "tls", HopPorts: "20000-30000"})
	if err != nil {
		t.Fatal(err)
	}
	n := a.NodeByID(id)
	if len(a.State.Groups) != 1 || n.GroupID != a.State.Groups[0].ID || n.Name != "手动" || n.HopPorts != "20000-30000" ||
		!strings.HasPrefix(n.Link, "hysteria2://pw@h.example.com:443/") {
		t.Fatalf("groups %+v node %+v", a.State.Groups, n)
	}
	if _, err := a.AddNode("", *n); err == nil || !strings.Contains(err.Error(), "相同") {
		t.Errorf("the same node twice: %v", err)
	}
	if _, err := a.AddNode("", node.Node{Protocol: node.VLESS, Host: "v.example.com", Port: 443, UUID: "u", Security: "reality"}); err == nil ||
		!strings.Contains(err.Error(), "pbk") {
		t.Errorf("REALITY without a key: %v", err)
	}
	sub, _ := a.AddGroup(app.GroupInput{Name: "订阅", URL: "http://127.0.0.1:1/sub", Subscription: true})
	h.Wait(t, func() bool { return !a.SubBusy[sub] })
	if _, err := a.AddNode(sub, node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p"}); err == nil {
		t.Error("nodes of a subscription come from it")
	}

	// Editing: measured results go, a dedicated port and the connection follow.
	a.Latency[id] = app.Latency{Duration: time.Second}
	a.Conn, a.ActiveID = app.StateOn, id
	edited := *n
	edited.Port, edited.Obfs, edited.ObfsPassword = 8443, "salamander", "ob"
	if err := a.EditNode(edited); err != nil {
		t.Fatal(err)
	}
	n = a.NodeByID(id)
	if n.Port != 8443 || n.Obfs != "salamander" || !strings.Contains(n.Link, "obfs-password=ob") || n.GroupID != a.State.Groups[0].ID {
		t.Fatalf("%+v", n)
	}
	if _, ok := a.Latency[id]; ok || a.SettingsDirty { // the connection takes the edit at once
		t.Fatalf("latency %+v dirty %v", a.Latency, a.SettingsDirty)
	}
	if st, _ := store.Load(a.StatePath()); st.Nodes[0].Port != 8443 {
		t.Fatal("not saved")
	}
	// A rename alone keeps results; nothing changed changes nothing.
	a.SettingsDirty = false
	a.Latency[id] = app.Latency{Duration: time.Second}
	renamed := *n
	renamed.Name = "改名"
	if err := a.EditNode(renamed); err != nil || a.NodeByID(id).Name != "改名" || a.SettingsDirty || a.Latency[id].Duration != time.Second {
		t.Fatalf("rename %v %+v dirty %v", err, a.NodeByID(id), a.SettingsDirty)
	}
	bad := *a.NodeByID(id)
	bad.HopPorts = "30000-20000"
	if err := a.EditNode(bad); err == nil || a.NodeByID(id).HopPorts != "20000-30000" {
		t.Fatalf("an invalid edit must change nothing: %v", err)
	}
	a.Conn, a.ActiveID = app.StateOff, ""
}

func TestDedicatedPorts(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	idA, idB := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	p1, p2 := freeTCPPort(t), freeTCPPort(t)
	for _, c := range []struct {
		port int
		want string
	}{{80, "1024"}, {a.State.Settings.Port, "本地代理端口"}} {
		if err := a.SetNodePort(idA, c.port); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("port %d: %v", c.port, err)
		}
	}
	if err := a.SetNodePort(idA, p1); err != nil {
		t.Fatal(err)
	}
	if err := a.SetNodePort(idB, p1); err == nil || !strings.Contains(err.Error(), "节点A") {
		t.Errorf("a port serves one node: %v", err)
	}
	if err := a.SetNodePort(idB, p2); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.Load(a.StatePath()); st.Ports[idA] != p1 || st.Ports[idB] != p2 {
		t.Fatalf("saved ports %v", st.Ports)
	}
	// Without a core nothing runs yet; once installed, the cores start for them (and the placeholder fails).
	if len(a.PortErrs) != 0 {
		t.Fatalf("errors before the core %v", a.PortErrs)
	}
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy && a.PortErrs[idA] != "" })
	if a.PortErrs[idA] != "启动失败，详见日志" || a.PortErrs[idB] != "启动失败，详见日志" {
		t.Fatalf("errors %v", a.PortErrs)
	}
	// A port taken by the main proxy, or a broken chain, fails alone.
	a.State.Settings.Port = p2
	a.State.Groups[0].Front = "gone"
	a.SettingChanged()
	if !strings.Contains(a.PortErrs[idA], "前置代理") || a.PortErrs[idB] != "与本地代理端口冲突" {
		t.Fatalf("errors %v", a.PortErrs)
	}
	// Removing a node releases its port.
	a.DeleteNode(idB)
	if _, ok := a.State.Ports[idB]; ok {
		t.Fatal("port of a deleted node")
	}
	if err := a.SetNodePort(idA, 0); err != nil || len(a.State.Ports) != 0 || len(a.PortErrs) != 0 {
		t.Fatalf("remove %v ports %v errors %v", err, a.State.Ports, a.PortErrs)
	}
}

// A dedicated port another program listens on is left out of the cores, which would fail with it, the
// connection included: it fails alone, and runs once the port is free.
func TestDedicatedPortTaken(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	idA, idB := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	taken, free := l.Addr().(*net.TCPAddr).Port, freeTCPPort(t)
	if err := a.SetNodePort(idA, taken); err != nil {
		t.Fatal(err)
	}
	if err := a.SetNodePort(idB, free); err != nil {
		t.Fatal(err)
	}
	if a.PortErrs[idA] != "端口已被其他程序占用" || a.PortErrs[idB] != "" {
		t.Fatalf("errors %v", a.PortErrs)
	}
	if cfg := string(a.CoreConfig()); strings.Contains(cfg, fmt.Sprint(taken)) || !strings.Contains(cfg, fmt.Sprint(free)) {
		t.Fatalf("config %s", cfg)
	}
	l.Close()
	a.SyncCores()
	if cfg := string(a.CoreConfig()); !strings.Contains(cfg, fmt.Sprint(taken)) || a.PortErrs[idA] != "" {
		t.Fatalf("the port freed: errors %v config %s", a.PortErrs, cfg)
	}
}

// freeTCPPort returns a port no program listens on now.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestMoveNodesAndGroups(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	link := func(name string) string { return "trojan://p@" + name + ".example.com:443?security=tls#" + name }
	a.ImportLinks(strings.Join([]string{link("a"), link("b"), link("c")}, "\n"), "")
	other, _ := a.AddGroup(app.GroupInput{Name: "其它"})
	a.ImportLinks(link("x"), other)
	a.ImportLinks(link("d"), a.State.Groups[0].ID) // after x in the global order
	names := func() string {
		var out []string
		for _, n := range a.State.Nodes {
			out = append(out, n.Name)
		}
		return strings.Join(out, ",")
	}
	id := func(name string) string {
		for _, n := range a.State.Nodes {
			if n.Name == name {
				return n.ID
			}
		}
		return ""
	}
	if names() != "a,b,c,x,d" {
		t.Fatalf("start %s", names())
	}
	a.MoveNodes([]string{id("d")}, id("a"))
	if names() != "d,a,b,x,c" {
		t.Fatalf("d to the front: %s", names())
	}
	a.MoveNodes([]string{id("d"), id("a")}, "")
	if names() != "b,c,d,x,a" {
		t.Fatalf("d and a to the end: %s", names())
	}
	a.MoveNodes([]string{id("b")}, id("b")) // onto itself: nothing
	if st, _ := store.Load(a.StatePath()); names() != "b,c,d,x,a" || st.Nodes[0].Name != "b" {
		t.Fatalf("order %s, saved first %q", names(), st.Nodes[0].Name)
	}

	home := a.State.Groups[0].ID
	a.MoveGroup(other, home)
	if a.State.Groups[0].ID != other {
		t.Fatalf("groups %+v", a.State.Groups)
	}
	a.MoveGroup(other, "")
	if a.State.Groups[1].ID != other {
		t.Fatalf("groups %+v", a.State.Groups)
	}
}

// A subscription update keeps the order the user arranged; new servers follow.
func TestSubscriptionKeepsOrder(t *testing.T) {
	link := func(name string) string { return "trojan://p@" + name + ".example.com:443?security=tls#" + name }
	body := strings.Join([]string{link("a"), link("b"), link("c")}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	h := apptest.New(t)
	a := h.A
	id, _ := a.AddGroup(app.GroupInput{URL: srv.URL, Subscription: true})
	h.Wait(t, func() bool { return !a.SubBusy[id] })
	a.MoveNodes([]string{a.State.Nodes[2].ID}, a.State.Nodes[0].ID) // c, a, b
	body = strings.Join([]string{link("new"), link("a"), link("b"), link("c")}, "\n")
	a.UpdateSubscription(id)
	h.Wait(t, func() bool { return !a.SubBusy[id] })
	var got []string
	for _, n := range a.State.Nodes {
		got = append(got, n.Name)
	}
	if strings.Join(got, ",") != "c,a,b,new" {
		t.Fatalf("order %v", got)
	}
}

// The core serves the dedicated ports with the connection, or alone without one. Under TUN it dials each
// chain through the physical interface. Once the connection ends, it serves the ports off the interface.
func TestDedicatedPortsUnderTUN(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.Elevated = true
	// This fixture models a supported core whose process still fails to start (placeholder bytes).
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.10", nil }
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	port, p := a.State.Nodes[1].ID, freeTCPPort(t)
	tag := fmt.Sprintf("\"port-%d\"", p) // its inbound, whose connections take its chain
	if err := a.SetNodePort(port, p); err != nil {
		t.Fatal(err)
	}
	cfg := string(a.CoreConfig())
	if !strings.Contains(cfg, fmt.Sprintf(`"port": %d`, p)) || strings.Contains(cfg, "mixed-in") || !strings.Contains(cfg, tag) ||
		strings.Contains(cfg, `"interface"`) || strings.Contains(cfg, `"dns"`) {
		t.Fatalf("the ports alone: %s", cfg)
	}
	h.Wait(t, func() bool { return a.PortErrs[port] != "" }) // the placeholder cores fail
	a.State.Settings.TUN.Enabled = true
	a.Connect(a.State.Nodes[0].ID, nil)
	if cfg := string(a.CoreConfig()); !strings.Contains(cfg, tag) || !strings.Contains(cfg, `"interface"`) || !strings.Contains(cfg, `"protocol": "tun"`) || !strings.Contains(cfg, "mixed-in") {
		t.Fatalf("under TUN one core serves the TUN, local port and dedicated ports: %s", cfg)
	}
	// The placeholder core fails: the connection ends with the TUN, and the core serves the port off the
	// interface again.
	h.Wait(t, func() bool {
		cfg := string(a.CoreConfig())
		return a.Conn == app.StateOff && a.PortErrs[port] != "" && strings.Contains(cfg, tag) && !strings.Contains(cfg, `"interface"`)
	})
}

func TestUpdateGeo(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.UpdateGeo()
	if a.GeoBusy || a.GeoErr == "" {
		t.Fatal("rule data needs the core first")
	}
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	if v := a.GeoVersion(); !strings.HasPrefix(v, "随核心安装") {
		t.Fatalf("version %q", v)
	}
	// Rule data that came with an earlier core stays too: a newer package may carry less of it.
	geosite := filepath.Join(a.CoreDir(), "geosite.dat")
	if err := os.WriteFile(geosite, []byte("earlier package"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	if b, _ := os.ReadFile(geosite); string(b) != "earlier package" {
		t.Fatalf("geosite.dat after a core update: %q", b)
	}
	h.Core.GeoTag = "202610132213"
	a.UpdateGeo()
	h.Wait(t, func() bool { return !a.GeoBusy })
	if a.State.GeoTag != "202610132213" || a.GeoMsg != "已更新" || a.GeoVersion() != "2026-10-13" {
		t.Fatalf("tag %q msg %q err %q version %q", a.State.GeoTag, a.GeoMsg, a.GeoErr, a.GeoVersion())
	}
	if st, _ := store.Load(a.StatePath()); st.GeoTag != "202610132213" {
		t.Fatalf("saved tag %q", st.GeoTag)
	}
	// The same release again: nothing downloaded.
	a.UpdateGeo()
	h.Wait(t, func() bool { return !a.GeoBusy })
	if a.GeoMsg != "已是最新" || h.Core.GeoInstalls.Load() != 1 {
		t.Fatalf("msg %q installs %d", a.GeoMsg, h.Core.GeoInstalls.Load())
	}
	// A core update keeps the newer rule data.
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	if b, _ := os.ReadFile(filepath.Join(a.CoreDir(), "geosite.dat")); string(b) != "geo 202610132213" {
		t.Fatalf("geosite.dat after a core update: %q", b)
	}
	h.Core.GeoTag, h.Core.GeoErr = "", errors.New("HTTP 403")
	a.UpdateGeo()
	h.Wait(t, func() bool { return !a.GeoBusy })
	if !strings.Contains(a.GeoErr, "HTTP 403") || a.State.GeoTag != "202610132213" {
		t.Fatalf("err %q tag %q", a.GeoErr, a.State.GeoTag)
	}
}

// A core download changes nothing until it is complete: a connect and tests run on the current core
// meanwhile (only the replacement holds them, TestCoreReplacementHoldsConnection), and a failed download
// leaves it in place.
func TestConnectDuringCoreUpdate(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	id := a.State.Nodes[0].ID
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	h.Core.Gate = make(chan struct{})
	a.InstallCore()
	a.Connect(id, nil)
	if a.WaitCore || a.Conn != app.StateConnecting {
		t.Fatalf("a download held a connect: waitCore %v conn %v", a.WaitCore, a.Conn)
	}
	// The placeholder core fails to run, which proves the start.
	h.Wait(t, func() bool { return a.Conn == app.StateOff })
	if !strings.Contains(a.ConnErr, "核心") {
		t.Fatalf("conn err %q", a.ConnErr)
	}
	if _, err := os.Stat(filepath.Join(a.DataDir, "config.json")); err != nil {
		t.Fatal("the connect did not start the current core")
	}
	if err := a.TestLatency([]string{id}); err != nil {
		t.Fatalf("a download held a test: %v", err)
	}
	h.Wait(t, func() bool { return !a.LatencyBusy })
	h.Core.Err = errors.New("HTTP 502")
	close(h.Core.Gate)
	h.Wait(t, func() bool { return !a.CoreBusy })
	if !strings.Contains(a.CoreErr, "HTTP 502") || !a.CoreReady() {
		t.Fatalf("core err %q ready %v", a.CoreErr, a.CoreReady())
	}
}

// A core install asked for during a rule data update waits for it, then keeps the new rule data.
func TestCoreInstallWaitsForGeo(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	h.Core.GeoTag, h.Core.GeoGate = "202610132213", make(chan struct{})
	a.UpdateGeo()
	a.InstallCore()
	if a.CoreBusy || h.Core.Installs.Load() != 1 {
		t.Fatal("the core install should wait for the rule data")
	}
	close(h.Core.GeoGate)
	h.Wait(t, func() bool { return !a.GeoBusy })
	if !a.CoreBusy {
		t.Fatal("the core install should start after the rule data update")
	}
	h.Wait(t, func() bool { return !a.CoreBusy })
	if b, _ := os.ReadFile(filepath.Join(a.CoreDir(), "geoip.dat")); string(b) != "geo 202610132213" || h.Core.Installs.Load() != 2 {
		t.Fatalf("geoip.dat %q, installs %d", b, h.Core.Installs.Load())
	}
}

// A subscription listing one server twice gets two nodes with their own IDs.
func TestSubscriptionDuplicateServers(t *testing.T) {
	link := "trojan://p@a.example.com:443?security=tls#a"
	body := link + "\n" + link
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	h := apptest.New(t)
	a := h.A
	id, _ := a.AddGroup(app.GroupInput{URL: srv.URL, Subscription: true})
	h.Wait(t, func() bool { return !a.SubBusy[id] })
	first := []string{a.State.Nodes[0].ID, a.State.Nodes[1].ID}
	a.UpdateSubscription(id)
	h.Wait(t, func() bool { return !a.SubBusy[id] })
	if len(a.State.Nodes) != 2 || a.State.Nodes[0].ID == a.State.Nodes[1].ID ||
		a.State.Nodes[0].ID != first[0] || a.State.Nodes[1].ID != first[1] {
		t.Fatalf("IDs before %v, after %s %s", first, a.State.Nodes[0].ID, a.State.Nodes[1].ID)
	}
}

// The log names the node a dedicated port belongs to, not its group's back proxy.
func TestPortLogNamesNode(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	other, _ := a.AddGroup(app.GroupInput{Name: "出口"})
	a.ImportLinks(apptest.LinkB, other)
	idA, idB := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	if err := a.EditGroup(a.State.Nodes[0].GroupID, app.GroupInput{Name: "默认分组", Back: idB}); err != nil {
		t.Fatal(err)
	}
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	p := freeTCPPort(t)
	if err := a.SetNodePort(idA, p); err != nil {
		t.Fatal(err)
	}
	// What the log says once the cores serve the ports.
	if got := a.DescribePorts(); got != fmt.Sprintf("%d → 节点A", p) {
		t.Fatalf("ports described as %q", got)
	}
}

func TestShortErrKeepsUTF8(t *testing.T) {
	s := app.ShortErr(errors.New(strings.Repeat("核心", 50)))
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) != 81 {
		t.Fatalf("%q", s)
	}
}

// A node asking to skip certificate verification gets the certificate seen first pinned, which Xray 26
// takes instead; edits and subscription updates keep it only for the same server.
func TestPinCertificates(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	h.Pin = "ab12"
	a.ImportLinks("hysteria2://pw@h.example.com:443?sni=s.example.com&insecure=1#H\ntrojan://p@t.example.com:443#T", "")
	h.Wait(t, func() bool { return a.State.Nodes[0].AutoPin != "" })
	hy, tr := a.State.Nodes[0], a.State.Nodes[1]
	if hy.AutoPin != "ab12" || tr.AutoPin != "" || h.Pins.Load() != 1 {
		t.Fatalf("pins %q %q, fetched %d", hy.AutoPin, tr.AutoPin, h.Pins.Load())
	}
	if w := xray.Warnings(&hy); len(w) != 0 {
		t.Errorf("a pinned node has nothing to warn about: %v", w)
	}
	if st, _ := store.Load(a.StatePath()); st.Nodes[0].AutoPin != "ab12" {
		t.Fatal("pin not saved")
	}
	// A rename keeps the pin; another server drops it, and the new one is pinned.
	renamed := hy
	renamed.Name = "改名"
	if err := a.EditNode(renamed); err != nil || a.State.Nodes[0].AutoPin != "ab12" {
		t.Fatalf("rename: %v %+v", err, a.State.Nodes[0])
	}
	moved := a.State.Nodes[0]
	moved.Host, h.Pin = "h2.example.com", "cd34"
	if err := a.EditNode(moved); err != nil {
		t.Fatal(err)
	}
	h.Wait(t, func() bool { return a.State.Nodes[0].AutoPin == "cd34" })
	// A failed fetch leaves the node as it was, and connecting asks again.
	h.Pin = ""
	a.ImportLinks("trojan://p@u.example.com:443?allowInsecure=1#U", "")
	h.Wait(t, func() bool { return !a.Pinning(a.State.Nodes[2].ID) })
	if a.State.Nodes[2].AutoPin != "" {
		t.Fatal("no pin without a certificate")
	}
	before := h.Pins.Load()
	a.Connect(a.State.Nodes[2].ID, nil)
	h.Wait(t, func() bool { return h.Pins.Load() > before })
	a.Disconnect()
	h.Wait(t, func() bool { return !a.CoreBusy }) // the connect installed the missing core
}
