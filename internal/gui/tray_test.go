package gui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
)

// The tray menu holds the sidebar panel's connection controls, but the local proxy, then the window's
// two items: nothing else, no item of its own to connect. Its height fits what it shows, the state
// whatever it is; its controls act at once, and the main window is asked to show what they changed.
func TestTrayMenu(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	a.State.SelectedID = a.State.Nodes[0].ID
	var posted []func()
	shown, quit := 0, 0
	m := &trayMenu{View: &View{App: a}, post: func(fn func()) { posted = append(posted, fn) },
		show: func() { shown++ }, quit: func() { quit++ }}
	tt := ui.NewTester(m.build, trayWidth, int(m.trayHeight()))
	fits := func(what string) {
		t.Helper()
		height := m.trayHeight()
		tt.SetSize(trayWidth, int(height))
		tt.Frame()
		r, ok := tt.Find("退出 RayDock")
		if bottom := r.Y + r.H; !ok || bottom > height-trayPad || bottom < height-trayPad-2 {
			t.Fatalf("%s: quit at %+v, menu %v high", what, r, height)
		}
	}
	fits("disconnected")
	for _, s := range []string{"未连接", "节点A", "绕过大陆", "全局", "系统代理", "TUN 模式", "显示主窗口", "退出 RayDock"} {
		if !tt.HasText(s) {
			t.Fatalf("no %q: %q", s, tt.Texts())
		}
	}
	for _, s := range []string{"本地代理", "连接 节点A", "已连接"} {
		if tt.HasText(s) {
			t.Fatalf("%q in the menu: %q", s, tt.Texts())
		}
	}
	// Connected to another node than the one selected, with errors: every row shows, and still fits.
	a.Conn, a.ActiveID = app.StateOn, a.State.Nodes[1].ID
	a.ConnErr, a.ProxyErr = "一个很长很长的连接错误，在托盘菜单里只占一行，完整内容在提示里", "设置系统代理失败"
	fits("connected, with errors")
	if !tt.HasText("切换到 节点A") {
		t.Fatalf("no switch: %q", tt.Texts())
	}
	a.Conn, a.ActiveID, a.ConnErr, a.ProxyErr = app.StateOff, "", "", ""
	fits("disconnected again")
	posted = nil

	clickFrame(t, tt, "显示主窗口")
	if shown != 1 {
		t.Fatal("not shown")
	}
	// The mode changes where it is, and the main window is asked to show it.
	clickFrame(t, tt, "全局")
	if a.State.Settings.Mode != "global" || len(posted) != 1 {
		t.Fatalf("mode %q, %d posted", a.State.Settings.Mode, len(posted))
	}
	// The power button connects from the menu.
	clickFrame(t, tt, "连接开关")
	if a.Conn == app.StateOff && a.ConnErr == "" {
		t.Fatal("connect not run")
	}
	n := len(posted)
	clickFrame(t, tt, "退出 RayDock")
	posted[n]()
	if quit != 1 {
		t.Fatal("not quit")
	}
}

// While connected, the menu redraws every second by itself, as the main window does, so that the
// connected time runs without the pointer moving over it.
func TestTrayMenuTicks(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	m := &trayMenu{View: &View{App: a}, post: func(func()) {}, show: func() {}, quit: func() {}}
	builds := 0
	tt := ui.NewTester(func(c *ui.Context) { builds++; m.build(c) }, trayWidth, int(m.trayHeight()))
	tt.Frame()
	ticks := func() int {
		before := builds
		tt.Advance(3 * time.Second)
		return builds - before
	}
	if n := ticks(); n != 0 {
		t.Fatalf("disconnected, %d redraws", n)
	}
	a.Conn, a.ActiveID, a.Since = app.StateOn, a.State.Nodes[0].ID, time.Now()
	tt.Frame()
	if n := ticks(); n < 3 {
		t.Fatalf("connected, %d redraws in 3 s", n)
	}
}
