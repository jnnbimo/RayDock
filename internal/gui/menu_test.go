package gui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app/apptest"
)

// rightClickMenu right-clicks text and checks that the window's own menu opened, with want among its
// items, and no system menu.
func rightClickMenu(t *testing.T, tt *ui.Tester, text, want string) {
	t.Helper()
	if err := tt.RightClick(text); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if m := tt.Menu(); len(m) > 0 {
		t.Fatalf("system menu %q on %q", m, text)
	}
	if !tt.HasText(want) {
		t.Fatalf("no %q in the menu of %q: %q", want, text, tt.Texts())
	}
}

func clickFrame(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	if err := tt.Click(text); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
}

// The log, a text input and other text open the window's glass menu, whose items edit and copy.
func TestTextMenus(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.QueueLog("2026/10/18 [Warning] core: Xray started")
	a.QueueLog("2026/10/18 [Info] proxy: accepted")
	a.FlushLogs()
	v := nodesView(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	clickFrame(t, tt, "日志")

	rightClickMenu(t, tt, "2026/10/18 [Info] proxy: accepted", "复制全部")
	clickFrame(t, tt, "复制全部")
	if !strings.Contains(h.Clip.Text, "Xray started") || !strings.Contains(h.Clip.Text, "proxy: accepted") || v.menu.open {
		t.Fatalf("clipboard %q menu %+v", h.Clip.Text, v.menu)
	}

	rightClickMenu(t, tt, "2026/10/18 [Info] proxy: accepted", "清空日志")
	clickFrame(t, tt, "清空日志")
	if len(a.Logs) != 0 {
		t.Fatalf("%d lines left", len(a.Logs))
	}

	// Paste into an empty one-line input, which joins the lines and keeps the keyboard.
	dns := &a.State.Settings.DirectDNS
	*dns = ""
	v.settingsTab = 2
	clickFrame(t, tt, "设置")
	h.Clip.Text = "1.1.1.1\r\n8.8.8.8"
	rightClickMenu(t, tt, "223.5.5.5", "粘贴")
	clickFrame(t, tt, "粘贴")
	if *dns != "1.1.1.1 8.8.8.8" {
		t.Fatalf("pasted %q", *dns)
	}
	if !tt.Focused("223.5.5.5") {
		t.Fatal("the input lost the keyboard")
	}
	// Select all, then cut.
	rightClickMenu(t, tt, "223.5.5.5", "全选")
	clickFrame(t, tt, "全选")
	rightClickMenu(t, tt, "223.5.5.5", "剪切")
	clickFrame(t, tt, "剪切")
	if *dns != "" || h.Clip.Text != "1.1.1.1 8.8.8.8" {
		t.Fatalf("input %q clipboard %q", *dns, h.Clip.Text)
	}
}

// No right click in the window opens the system's menu: every input, text area, selectable text and
// combo box has the window's own.
func TestNoSystemMenus(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	a.ConnErr = "连接失败：测试"
	v := nodesView(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	// below right-clicks under the label text, on the field it labels (labeled).
	below := func(text string) {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("no %q: %q", text, tt.Texts())
		}
		tt.RightClickAt(r.X+8, r.Y+r.H+6+12)
	}
	check := func(text string) {
		t.Helper()
		if strings.HasPrefix(text, "↓") {
			below(strings.TrimPrefix(text, "↓"))
		} else if err := tt.RightClick(text); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		if m := tt.Menu(); len(m) > 0 || !v.menu.open {
			t.Fatalf("%q: system menu %q, own menu %+v", text, m, v.menu)
		}
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if v.menu.open {
			t.Fatalf("%q: Escape left the menu open", text)
		}
	}
	check("连接失败：测试")

	v.openAddNode()
	tt.Frame()
	check("分享链接或 JSON")
	if !v.nf.open {
		t.Fatal("Escape on the menu closed the dialog too")
	}
	v.nf.open = false

	v.openGroupDialog("")
	tt.Frame()
	check("分组名称")
	v.gf.kind = 1 // a subscription: its address, and the proxies of the chain
	tt.Frame()
	check("↓订阅地址")
	// The proxies are chosen with the window's drop-down, which has no menu: none of the system's either.
	below("前置代理")
	tt.Frame()
	tt.Frame()
	if m := tt.Menu(); len(m) > 0 {
		t.Fatalf("system menu %q on the proxy drop-down", m)
	}
	v.gf.open = false

	v.openPortDialog(a.State.Nodes[0].ID)
	tt.Frame()
	check("↓端口")
	v.portOpen = false
	tt.Frame()

	clickFrame(t, tt, "设置")
	check("本地代理端口")
	for i, want := range map[int]string{1: "代理规则", 4: "版本 开发版"} {
		v.settingsTab = i
		tt.Frame()
		check(want)
	}
}

// A number pasted by the menu applies the setting as typing it does.
func TestNumberMenu(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	v := nodesView(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	clickFrame(t, tt, "设置")
	h.Clip.Text = " 23456 "
	rightClickMenu(t, tt, "本地代理端口", "粘贴")
	clickFrame(t, tt, "粘贴")
	if a.State.Settings.Port != 23456 {
		t.Fatalf("port %d", a.State.Settings.Port)
	}
	h.Clip.Text = "abc"
	rightClickMenu(t, tt, "本地代理端口", "粘贴")
	clickFrame(t, tt, "粘贴")
	if a.State.Settings.Port != 23456 || !tt.HasText("剪贴板中不是数字") {
		t.Fatalf("port %d texts %q", a.State.Settings.Port, tt.Texts())
	}
	// ParseFloat reads these as numbers, which no range holds.
	for _, s := range []string{"NaN", "Inf", "-infinity"} {
		h.Clip.Text = s
		rightClickMenu(t, tt, "本地代理端口", "粘贴")
		clickFrame(t, tt, "粘贴")
		if a.State.Settings.Port != 23456 {
			t.Fatalf("%s pasted: port %d", s, a.State.Settings.Port)
		}
	}
}
