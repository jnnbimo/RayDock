package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"raydock/internal/xray"
)

// A change of the connection made while the core files are replaced waits, the core running meanwhile,
// and is then made as it was asked: the connection keeps its time, the settings not applied stay pending.
func TestChangeWaitsForCoreFiles(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	defer runningFake(t, a, a.State.Nodes[0].ID)()
	commitFake(a, a.planned)
	running, since := a.planned, a.Since
	a.State.Settings.Port++ // not applied
	a.SettingChanged()
	a.coreHold = true
	a.SetMode(xray.ModeGlobal)
	if !a.WaitCore || a.planned != running || a.Conn != StateConnecting {
		t.Fatalf("the change should wait, the core running: wait %v", a.WaitCore)
	}
	a.coreHold = false
	a.resumeConnect(change{})
	o := a.planned.opts
	if a.WaitCore || a.planned == running || o.Mode != xray.ModeGlobal || o.Port == a.State.Settings.Port || a.planned.fresh || a.planned.switched {
		t.Fatalf("resumed: wait %v opts %+v target %+v", a.WaitCore, o, a.planned)
	}
	time.Sleep(time.Millisecond)
	commitFake(a, a.planned)
	if a.Since != since || a.Conn != StateOn || !a.SettingsDirty {
		t.Fatalf("since kept %v, state %v, pending %v", a.Since == since, a.Conn, a.SettingsDirty)
	}
}

// A wait that ends with nothing to change (another node, then back), the core still serving
// it, connects through it.
func TestWaitEndsWithoutChange(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	a.ImportLinks("socks://127.0.0.1:9#a\nsocks://127.0.0.1:10#b", "")
	first := a.State.Nodes[0].ID
	defer runningFake(t, a, first)()
	commitFake(a, a.planned)
	conn := a.planned.core()
	a.coreHold = true
	a.Connect(a.State.Nodes[1].ID, nil)
	a.Connect(first, nil)
	if !a.WaitCore || a.ActiveID != first {
		t.Fatal("the changes should wait")
	}
	a.coreHold = false
	a.resumeConnect(change{})
	if a.planned.core() != conn {
		t.Fatal("nothing changed, the core should go on")
	}
	commitFake(a, a.planned)
	if a.Conn != StateOn || a.WaitCore {
		t.Fatalf("state %v", a.Conn)
	}
}

// LAN access applies to the dedicated ports with the rest of the settings while connected: the core
// restarts once, on applying. Without a connection the ports follow it at once.
func TestLANRestartsOnce(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	a.ImportLinks("socks://127.0.0.1:9#a\nsocks://127.0.0.1:10#b", "")
	p, _ := freePort()
	if err := a.SetNodePort(a.State.Nodes[1].ID, p); err != nil {
		t.Fatal(err)
	}
	defer runningFake(t, a, a.State.Nodes[0].ID)()
	commitFake(a, a.planned)
	before := a.planned
	a.State.Settings.AllowLAN = true
	a.SettingChanged()
	if a.planned != before || !a.SettingsDirty || a.Conn != StateOn {
		t.Fatalf("nothing should restart before applying: pending %v state %v", a.SettingsDirty, a.Conn)
	}
	a.Reconnect()
	if a.planned.core() == before.core() || !a.planned.opts.AllowLAN || !strings.Contains(string(a.planned.core().cfg), `"listen": "0.0.0.0"`) {
		t.Fatal("applying should restart the core, the ports open to the LAN")
	}
	commitFake(a, a.planned)

	a.Disconnect()
	idle := a.planned.core()
	if idle == nil || idle.run.Chain != nil {
		t.Fatal("the core should serve the ports alone")
	}
	a.State.Settings.AllowLAN = false
	a.SettingChanged()
	if a.planned.core() == idle || strings.Contains(string(a.planned.core().cfg), `"listen": "0.0.0.0"`) {
		t.Fatal("without a connection the ports follow LAN access at once")
	}
}

// A setting of the TUN alone (its MTU) is pending like any other, and applying it restarts the core.
func TestTUNSettingPending(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	a.Elevated = true
	placeholderCore(t, a)
	a.CoreVersion = xray.MinTUNVersion
	a.State.Settings.TUN.Enabled = true
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	defer runningFake(t, a, a.State.Nodes[0].ID)()
	commitFake(a, a.planned)
	before := a.planned
	a.State.Settings.TUN.MTU = 1400
	a.SettingChanged()
	if !a.SettingsDirty || a.planned != before {
		t.Fatalf("pending %v, replanned %v", a.SettingsDirty, a.planned != before)
	}
	a.Reconnect()
	if a.planned.core() == before.core() || a.Conn != StateConnecting {
		t.Fatal("applying should restart the core")
	}
	commitFake(a, a.planned)
	if a.SettingsDirty || a.Conn != StateOn {
		t.Fatalf("applied: pending %v state %v", a.SettingsDirty, a.Conn)
	}
}

// Replacing the core files ends a latency test running: one through many nodes would hold the
// replacement, and every change waiting for it, for minutes.
func TestReplacementEndsLatencyTest(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.latencyCancel = cancel
	replaced := make(chan struct{})
	a.replaceFiles(func() { close(replaced) })
	if ctx.Err() == nil {
		t.Fatal("the latency test goes on")
	}
	(<-posts)() // queues the replacement
	select {
	case <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("not replaced")
	}
}
