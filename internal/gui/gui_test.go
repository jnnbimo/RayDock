package gui

import (
	"errors"
	"fmt"
	"raydock/internal/app"
	"raydock/internal/app/apptest"
	"raydock/internal/store"
	"raydock/internal/system"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestImportAndUI(t *testing.T) {
	h := apptest.New(t)
	v := New(h.A)
	tt := ui.NewTester(v.Build, 1180, 760)
	if !tt.HasText("还没有节点") {
		t.Fatalf("missing empty state: %q", tt.Texts())
	}
	if tt.HasText("从剪贴板导入") || slices.ContainsFunc(pages, func(p page) bool { return p.title == "订阅" }) {
		t.Fatal("the clipboard button and the subscriptions page are gone")
	}
	// Add nodes through the dialog: with no group yet, they go to a new default group.
	h.Clip.Text = apptest.LinkA + "\n" + apptest.LinkB + "\nnot-a-link\n"
	if err := tt.Click("添加节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("添加到「" + store.DefaultGroup + "（新建）」") {
		t.Fatalf("import target missing: %q", tt.Texts())
	}
	if err := tt.Click("粘贴"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(h.A.State.Nodes) != 2 || len(h.A.State.Groups) != 1 || !tt.HasText("节点A") || !tt.HasText("节点B") {
		t.Fatalf("%d nodes after import, texts %q", len(h.A.State.Nodes), tt.Texts())
	}
	// Importing again adds nothing.
	if msg := h.A.ImportLinks(apptest.LinkA, "").String(); !strings.Contains(msg, "已存在") || len(h.A.State.Nodes) != 2 {
		t.Fatalf("reimport: %s, %d nodes", msg, len(h.A.State.Nodes))
	}
	if err := tt.Click("节点B"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if h.A.State.SelectedID != h.A.State.Nodes[1].ID {
		t.Fatalf("selected %q", h.A.State.SelectedID)
	}
	// Without a core, connecting downloads it first; a failed download ends the connect with the reason.
	h.Core.Err = errors.New("HTTP 403")
	if err := tt.Click("连接开关"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if h.A.Conn != app.StateConnecting || !h.A.WaitCore || !h.A.CoreBusy {
		t.Fatalf("should wait for the core: conn %v waitCore %v busy %v", h.A.Conn, h.A.WaitCore, h.A.CoreBusy)
	}
	h.Wait(t, func() bool { return !h.A.CoreBusy })
	tt.Frame()
	if h.A.Conn != app.StateOff || !strings.Contains(h.A.ConnErr, "HTTP 403") || !tt.HasText("重试") {
		t.Fatalf("conn %v err %q texts %q", h.A.Conn, h.A.ConnErr, tt.Texts())
	}
}

func TestPages(t *testing.T) {
	h := apptest.New(t)
	h.A.ImportLinks(apptest.LinkA, "")
	h.A.QueueLog("2026/10/14 [Warning] core: Xray 26.3.27 started")
	h.A.FlushLogs()
	tt := ui.NewTester(New(h.A).Build, 1180, 760)
	for _, pg := range []struct{ item, want string }{
		{"日志", "core: Xray 26.3.27 started"},
		{"设置", "启动后自动连接"},
		{"分流", "自定义规则"}, // settings tabs
		{"DNS", "静态解析（Hosts）"},
		{"TUN", "TUN 虚拟网卡"},
		{"关于", "Xray 核心"},
		{"节点", "节点A"},
	} {
		if err := tt.Click(pg.item); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !tt.HasText(pg.want) {
			t.Fatalf("%s: %q not found: %q", pg.item, pg.want, tt.Texts())
		}
	}
	if h.A.Logs[0].Level != 1 {
		t.Errorf("warning log level %d", h.A.Logs[0].Level)
	}
}

func TestAdminDialog(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	a.State.SelectedID = a.State.Nodes[0].ID
	tt := ui.NewTester(New(a).Build, 1180, 760)
	a.SetTUN(true)
	tt.Frame()
	if !a.AdminPrompt || !tt.HasText("以管理员身份重启") {
		t.Fatalf("prompt %v texts %q", a.AdminPrompt, tt.Texts())
	}
	if err := tt.Click("关闭 TUN 模式"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.State.Settings.TUN.Enabled || a.AdminPrompt {
		t.Fatal("should turn TUN off and close the dialog")
	}
}

func TestAutostartSetting(t *testing.T) {
	h := apptest.New(t)
	v := New(h.A)
	tt := ui.NewTester(v.Build, 1180, 760)
	v.router.Push("/settings")
	tt.Frame()
	if err := tt.Click("管理员"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	h.Wait(t, func() bool { return !h.A.StartBusy })
	if h.A.StartMode != system.StartAdmin || len(h.Start.Sets) != 1 {
		t.Fatalf("mode %v sets %v", h.A.StartMode, h.Start.Sets)
	}
}

func TestTrayIcon(t *testing.T) {
	for _, on := range []bool{false, true} {
		if b := trayIcon(on); len(b) < 100 || string(b[1:4]) != "PNG" {
			t.Fatalf("tray icon is not a PNG (%d bytes)", len(b))
		}
	}
}

// Groups: tabs filter the list, the dialog adds a group with a front proxy, the bar shows and edits it.
func TestGroupsUI(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	v := New(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	if err := tt.Click("添加分组"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	v.gf.name = "中转"
	v.gf.front = a.State.Nodes[0].ID
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.State.Groups) != 2 || v.gf.open {
		t.Fatalf("groups %+v open %v", a.State.Groups, v.gf.open)
	}
	relay := a.GroupByID(v.group)
	if relay == nil || relay.Name != "中转" || relay.Front != a.State.Nodes[0].ID {
		t.Fatalf("new group %+v shown %q", relay, v.group)
	}
	// The new group is shown: empty, with its bar and front proxy.
	if !tt.HasText("此分组还没有节点") || !tt.HasText("前置 · 节点A") || tt.HasText("example.com:443") {
		t.Fatalf("texts %q", tt.Texts())
	}
	// Adding nodes while it is shown targets it.
	if err := tt.Click("添加节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	v.nf.links = apptest.LinkB
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n := a.State.Nodes[1]; n.GroupID != relay.ID || !tt.HasText("b.example.com:443") || tt.HasText("a.example.com:443") {
		t.Fatalf("node %+v texts %q", n, tt.Texts())
	}
	if tt.HasText("全部") {
		t.Fatal("there is no all-nodes tab")
	}
	if err := tt.Click(store.DefaultGroup); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.group != a.State.Groups[0].ID || !tt.HasText("a.example.com:443") || tt.HasText("b.example.com:443") {
		t.Fatalf("default group: group %q texts %q", v.group, tt.Texts())
	}
	// Editing loads the current proxies; clearing the front saves.
	v.openGroupDialog(relay.ID)
	if v.gf.front != a.State.Nodes[0].ID || v.gf.back != "" || v.gf.kind != 0 {
		t.Fatalf("form %+v", v.gf)
	}
	v.gf.front = ""
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.GroupByID(relay.ID).Front != "" {
		t.Fatal("front proxy should be cleared")
	}
	// Speed results show download over upload.
	a.Speed[a.State.Nodes[0].ID] = app.Speed{Down: 12.5 * (1 << 20), Up: 512 << 10}
	a.Speed[a.State.Nodes[1].ID] = app.Speed{Phase: "下载"}
	tt.Frame()
	if !tt.HasText("↓ 12.5 MB/s") || !tt.HasText("↑ 512 KB/s") {
		t.Fatalf("speed texts %q", tt.Texts())
	}
	v.group = relay.ID
	tt.Frame()
	if !tt.HasText("下载") {
		t.Fatalf("speed phase texts %q", tt.Texts())
	}
}

// Deleting while the list is built must not leave the rest of the frame reading removed nodes.
func TestDeleteWhileShown(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	other, _ := a.AddGroup(app.GroupInput{Name: "其它"})
	a.ImportLinks("socks://dXNlcjpwYXNz@10.0.0.2:1080#家里", other)
	v := New(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	v.group = a.State.Groups[0].ID
	tt.Frame()
	// Deleting asks first; cancelling keeps everything.
	if err := tt.Click("删除分组及其节点"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("确定删除分组“默认分组”及其中的 2 个节点吗？删除后无法恢复。") {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.State.Groups) != 2 || len(a.State.Nodes) != 3 || tt.HasText("确定删除分组“默认分组”及其中的 2 个节点吗？删除后无法恢复。") {
		t.Fatalf("cancelled, yet groups %d nodes %d texts %q", len(a.State.Groups), len(a.State.Nodes), tt.Texts())
	}
	if err := tt.Click("删除分组及其节点"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	// The next group is shown.
	if len(a.State.Groups) != 1 || len(a.State.Nodes) != 1 || v.group != other || !tt.HasText("10.0.0.2:1080") {
		t.Fatalf("groups %d nodes %d shown %q texts %q", len(a.State.Groups), len(a.State.Nodes), v.group, tt.Texts())
	}
}

func TestClearLogsWhileShown(t *testing.T) {
	h := apptest.New(t)
	for i := range 5 {
		h.A.QueueLog(fmt.Sprintf("line %d", i))
	}
	h.A.FlushLogs()
	v := New(h.A)
	tt := ui.NewTester(v.Build, 1180, 760)
	v.router.Push("/logs")
	tt.Frame()
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(h.A.Logs) != 0 || !tt.HasText("暂无日志") {
		t.Fatalf("%d logs, texts %q", len(h.A.Logs), tt.Texts())
	}
}

// Deleting a node from its menu, other than the last row, while the list is built.
func TestDeleteNodeFromMenu(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB+"\nsocks://dXNlcjpwYXNz@10.0.0.2:1080#家里", "")
	idA := a.State.Nodes[0].ID
	if err := a.SetNodePort(idA, 20001); err != nil {
		t.Fatal(err)
	}
	a.State.Groups[0].Front = idA
	tt := ui.NewTester(New(a).Build, 1180, 760)
	// Deleting asks first, telling what goes with the node; cancelling keeps it.
	chooseFromMenu(t, tt, "a.example.com:443", "删除")
	for _, want := range []string{"确定删除节点“节点A”吗？删除后无法恢复。", "它的专用端口 20001 也会一并移除。",
		"分组“默认分组”把它用作前置或后置代理，删除后这些分组的节点将无法连接，需要重新选择。"} {
		if !tt.HasText(want) {
			t.Fatalf("no %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.State.Nodes) != 3 {
		t.Fatal("cancelled, yet deleted")
	}
	chooseFromMenu(t, tt, "a.example.com:443", "删除")
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(a.State.Nodes) != 2 || tt.HasText("a.example.com:443") || !tt.HasText("10.0.0.2:1080") {
		t.Fatalf("%d nodes, texts %q", len(a.State.Nodes), tt.Texts())
	}
}

// Sorting by name (pinyin, numbers by value) and by latency (failed and untested last), from the headers.
func TestSortNodes(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(strings.Join([]string{
		"trojan://p@h10.example.com:443#香港 10",
		"trojan://p@b.example.com:443#北京",
		"trojan://p@h2.example.com:443#香港 2",
		"trojan://p@a.example.com:443#Alpha",
		"trojan://p@z.example.com:443#Zulu",
	}, "\n"), "")
	v := New(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	names := func() []string {
		var out []string
		for _, r := range v.visibleNodes() {
			out = append(out, a.State.Nodes[r].Name)
		}
		return out
	}
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	click("名称 / 地址")
	if got := strings.Join(names(), ","); got != "Alpha,Zulu,北京,香港 2,香港 10" {
		t.Fatalf("by name %s", got)
	}
	click("名称 / 地址")
	if got := strings.Join(names(), ","); got != "香港 10,香港 2,北京,Zulu,Alpha" {
		t.Fatalf("by name, descending %s", got)
	}
	click("名称 / 地址")
	if got := strings.Join(names(), ","); got != "香港 10,北京,香港 2,Alpha,Zulu" {
		t.Fatalf("original order %s", got)
	}

	ms := func(n int) app.Latency { return app.Latency{Duration: time.Duration(n) * time.Millisecond} }
	n := a.State.Nodes
	a.Latency[n[0].ID] = ms(300)
	a.Latency[n[1].ID] = app.Latency{Err: "超时"}
	a.Latency[n[2].ID] = ms(80)
	a.Latency[n[4].ID] = ms(150) // Alpha untested
	click("延迟")
	if got := strings.Join(names(), ","); got != "香港 2,Zulu,香港 10,北京,Alpha" {
		t.Fatalf("by latency %s", got)
	}
	click("延迟")
	if got := strings.Join(names(), ","); got != "香港 10,Zulu,香港 2,北京,Alpha" {
		t.Fatalf("by latency, descending %s", got)
	}
}

func TestPortDialog(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	id := a.State.Nodes[0].ID
	tt := ui.NewTester(New(a).Build, 1180, 760)
	chooseFromMenu(t, tt, "a.example.com:443", "设置专用端口")
	tt.Frame()
	if !tt.HasText("专用端口") || !tt.HasText("「节点A」") {
		t.Fatalf("dialog texts %q", tt.Texts())
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.State.Ports[id] != 20001 || !tt.HasText("20001") {
		t.Fatalf("ports %v texts %q", a.State.Ports, tt.Texts())
	}
	openRowMenu(t, tt, "a.example.com:443")
	if tt.HasText("复制专用端口地址") || tt.HasText("设置专用端口…") {
		t.Fatalf("copying moved to the dialog, no ellipsis: %q", tt.Texts())
	}
	if err := tt.Click("设置专用端口"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if err := tt.Click("复制地址"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if h.Clip.Text != "127.0.0.1:20001" {
		t.Fatalf("clipboard %q", h.Clip.Text)
	}
	if err := tt.Click("移除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.State.Ports) != 0 {
		t.Fatalf("ports %v", a.State.Ports)
	}
}

// A node's menu lists its actions in a fixed order, deleting set apart at the end.
func TestNodeMenuOrder(t *testing.T) {
	h := apptest.New(t)
	h.A.ImportLinks(apptest.LinkA, "")
	tt := ui.NewTester(New(h.A).Build, 1180, 760)
	openRowMenu(t, tt, "a.example.com:443")
	want := []string{"连接", "编辑", "测速", "测延迟", "节点分享", "设置专用端口", "删除"}
	// The menu is built last; each item shows its text once and is labeled with it once.
	texts := tt.Texts()
	var got []string
	for _, s := range texts[slices.Index(texts, "连接"):] {
		if slices.Contains(want, s) && (len(got) == 0 || got[len(got)-1] != s) {
			got = append(got, s)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("menu %q, want %q", got, want)
	}
}

// Sharing shows the node's link as a QR code, module for module, above the link itself, and follows a
// changed link. A link beyond a QR code's capacity is still shown and copied, without a cut-short code.
func TestShareNode(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	link := a.State.Nodes[0].Link
	v := New(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	openRowMenu(t, tt, "a.example.com:443")
	if tt.HasText("复制分享链接") || tt.HasText("二维码分享") {
		t.Fatalf("one item shares: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	chooseFromMenu(t, tt, "a.example.com:443", "节点分享")
	for range 30 {
		tt.Frame()
	}
	if !v.share.open || v.share.link != link || !tt.HasText("节点分享") || !tt.HasText("节点A") || !tt.HasText(link) {
		t.Fatalf("share %+v texts %q", v.share, tt.Texts())
	}
	qr, ok := tt.Find("节点二维码")
	text, ok2 := tt.Find(link)
	code := v.share.code
	if !ok || !ok2 || code == nil || text.Y < qr.Y+qr.H {
		t.Fatalf("QR %+v %v, link %+v %v", qr, ok, text, ok2)
	}
	m := qr.W / float32(code.Size+2*qrQuiet)
	if m < 2 || m != float32(int(m)) || qr.W != qr.H {
		t.Fatalf("tile %+v for %d modules", qr, code.Size)
	}
	img := tt.Image()
	dark := func(x, y int) bool {
		px := img.RGBAAt(int(qr.X+(float32(x)+0.5)*m), int(qr.Y+(float32(y)+0.5)*m))
		return int(px.R)+int(px.G)+int(px.B) < 3*128
	}
	for y := -qrQuiet; y < code.Size+qrQuiet; y++ {
		for x := -qrQuiet; x < code.Size+qrQuiet; x++ {
			inside := x >= 0 && y >= 0 && x < code.Size && y < code.Size
			if want := inside && code.Black(x, y); dark(qrQuiet+x, qrQuiet+y) != want {
				t.Fatalf("module %d,%d: dark %v, want %v", x, y, !want, want)
			}
		}
	}
	if err := tt.Click("复制链接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if h.Clip.Text != link {
		t.Fatalf("clipboard %q", h.Clip.Text)
	}
	// A subscription update replaces the link while the dialog is open.
	a.State.Nodes[0].Link = apptest.LinkB
	tt.Frame()
	if v.share.link != apptest.LinkB || v.share.code == code || !tt.HasText(apptest.LinkB) {
		t.Fatalf("stale share of %q", v.share.link)
	}
	long := "vless://x@a.example.com:443#" + strings.Repeat("%E8%8A%82", 400)
	a.State.Nodes[0].Link = long
	tt.Frame()
	tt.Frame()
	if !v.share.open || v.share.code != nil || !tt.HasText("链接过长，无法生成二维码") || !tt.HasText(long) {
		t.Fatalf("share %+v texts %q", v.share.open, tt.Texts())
	}
	if _, ok := tt.Find("节点二维码"); ok {
		t.Fatal("no QR code for a link beyond its capacity")
	}
	if err := tt.Click("复制链接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if h.Clip.Text != long {
		t.Fatal("the long link is copied whole")
	}
}

// Speed tests start from a node's menu; while one runs, the menu stops it.
func TestSpeedFromMenu(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA, "")
	tt := ui.NewTester(New(a).Build, 1180, 760)
	if tt.HasText("测速") {
		t.Fatalf("no speed test button: %q", tt.Texts())
	}
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	chooseFromMenu(t, tt, "a.example.com:443", "测速")
	tt.Frame()
	if !a.SpeedBusy {
		t.Fatal("speed test should run")
	}
	chooseFromMenu(t, tt, "a.example.com:443", "停止测速")
	tt.Frame()
	h.Wait(t, func() bool { return !a.SpeedBusy })
}

// Shift+F10 opens the selected row's menu with its first item focused; arrows and Enter choose, and the
// edit dialog saves a change.
func TestRowMenuKeyboardEdit(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkB, "")
	v := New(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	if err := tt.Click("b.example.com:443"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(ui.Shift, ui.KeyF10)
	tt.Frame()
	tt.Frame()
	if !v.menu.open || !tt.Focused("连接") {
		t.Fatalf("menu %+v texts %q", v.menu, tt.Texts())
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if v.menu.open || !v.nf.open || v.nf.id != a.State.Nodes[0].ID || !tt.HasText("编辑节点") {
		t.Fatalf("menu %+v form %+v", v.menu, v.nf)
	}
	v.nf.n.Password = "new-secret"
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n := a.State.Nodes[0]; n.Password != "new-secret" || !strings.HasPrefix(n.Link, "trojan://new-secret@") || v.nf.open {
		t.Fatalf("%+v", n)
	}
}

// openRowMenu right-clicks the row showing text, which opens its menu.
func openRowMenu(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	if err := tt.RightClick(text); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("设置专用端口") {
		t.Fatalf("no menu: %q", tt.Texts())
	}
}

// chooseFromMenu chooses item from the menu of the row showing text.
func chooseFromMenu(t *testing.T, tt *ui.Tester, text, item string) {
	t.Helper()
	openRowMenu(t, tt, text)
	if err := tt.Click(item); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("设置专用端口") {
		t.Fatalf("menu still open after %s", item)
	}
}

// drag presses at the center of from and moves in steps to (x, y) before releasing.
func drag(t *testing.T, tt *ui.Tester, from string, to func(r ui.Rect) (float32, float32), target string) {
	t.Helper()
	src, ok := tt.Find(from)
	if !ok {
		t.Fatalf("%q not found", from)
	}
	dst, ok := tt.Find(target)
	if !ok {
		t.Fatalf("%q not found", target)
	}
	x0, y0 := src.X+src.W/2, src.Y+src.H/2
	x1, y1 := to(dst)
	tt.Press(x0, y0)
	tt.Frame()
	for k := 1; k <= 8; k++ {
		f := float32(k) / 8
		tt.Move(x0+(x1-x0)*f, y0+(y1-y0)*f)
		tt.Frame()
	}
	tt.Release(x1, y1)
	tt.Frame()
	tt.Frame()
}

func TestDragToReorder(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	link := func(name string) string { return "trojan://p@" + name + ".example.com:443?security=tls#节点" + name }
	a.ImportLinks(strings.Join([]string{link("a"), link("b"), link("c")}, "\n"), "")
	other, _ := a.AddGroup(app.GroupInput{Name: "其它"})
	v := New(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	v.group = a.State.Groups[0].ID
	tt.Frame()
	order := func() string {
		var out []string
		for _, n := range a.State.Nodes {
			out = append(out, n.Name)
		}
		return strings.Join(out, ",")
	}
	// Row c onto the top half of row a: it goes first.
	drag(t, tt, "c.example.com:443", func(r ui.Rect) (float32, float32) { return r.X + r.W/2, r.Y - 6 }, "a.example.com:443")
	if order() != "节点c,节点a,节点b" {
		t.Fatalf("after dragging c up: %s", order())
	}
	// Sorted, rows keep the sort's order and do not drag.
	v.sortBy = sortName
	tt.Frame()
	drag(t, tt, "c.example.com:443", func(r ui.Rect) (float32, float32) { return r.X + r.W/2, r.Y + r.H + 10 }, "b.example.com:443")
	if order() != "节点c,节点a,节点b" {
		t.Fatalf("sorted list reordered: %s", order())
	}
	v.sortBy = sortNone
	tt.Frame()
	// Group "其它" dropped on the first tab takes its place.
	drag(t, tt, "其它", func(r ui.Rect) (float32, float32) { return r.X + r.W/2, r.Y + r.H/2 }, store.DefaultGroup)
	if a.State.Groups[0].ID != other {
		t.Fatalf("groups %+v", a.State.Groups)
	}
	if v.group != a.State.Groups[1].ID {
		t.Fatalf("dragging should not change the group shown: %q", v.group)
	}
}

func TestGeoUpdateUI(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.InstallCore()
	h.Wait(t, func() bool { return !a.CoreBusy })
	v := New(a)
	tt := ui.NewTester(v.Build, 1180, 1400) // the section is below the fold at the default height
	v.router.Push("/settings")
	v.settingsTab = 1
	tt.Frame()
	tt.Frame()
	if !tt.HasText("规则数据") || !tt.HasText("随核心安装") {
		t.Fatalf("texts %q", tt.Texts())
	}
	h.Core.GeoTag = "202610132213"
	if err := tt.Click("更新规则"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	h.Wait(t, func() bool { return !a.GeoBusy })
	tt.Frame()
	if !tt.HasText("2026-10-13") || !tt.HasText("已更新") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// Every control clicked to act shows the hand: buttons, switches, segments, tabs, the sidebar, menus,
// drop-downs and sortable headers.
func TestButtonCursor(t *testing.T) {
	h := apptest.New(t)
	h.A.ImportLinks(apptest.LinkA, "")
	h.A.StartBusy = true // a disabled control shows the hand too, as MyGo leaves it
	tt := ui.NewTester(New(h.A).Build, 1180, 760)
	check := func(want ui.Cursor, labels ...string) {
		t.Helper()
		for _, label := range labels {
			r, ok := tt.Find(label)
			if !ok {
				t.Fatalf("no %q: %q", label, tt.Texts())
			}
			tt.Move(r.X+r.W/2, r.Y+r.H/2)
			if got := tt.Cursor(); got != want {
				t.Errorf("%s: cursor %v, want %v", label, got, want)
			}
		}
	}
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	check(ui.CursorPointer, "连接开关", "添加节点", "测延迟", "日志", "绕过大陆", "名称 / 地址", "默认分组")
	click("设置")
	check(ui.CursorPointer, "常规", "跟随系统", "管理员")
	click("分流")
	check(ui.CursorPointer, "未命中时解析", "阻止")
	click("节点")
	click("添加节点")
	click("手动填写")
	check(ui.CursorPointer, "粘贴链接", "取消", "添加")

	// A switch's label is also the text beside it, so it is checked alone.
	var on bool
	sw := ui.NewTester(func(c *ui.Context) { ui.Row(c).Children(func() { switchControl(c, &on).Label("开关") }) }, 200, 100)
	r, _ := sw.Find("开关")
	sw.Move(r.X+r.W/2, r.Y+r.H/2)
	if sw.Cursor() != ui.CursorPointer {
		t.Errorf("switch: cursor %v", sw.Cursor())
	}
}

// Each routing mode's segment explains that mode, whichever is chosen.
func TestModeTooltips(t *testing.T) {
	h := apptest.New(t)
	tt := ui.NewTester(New(h.A).Build, 1180, 760)
	for _, m := range []struct{ label, hint string }{
		{"全局", "除局域网外全部走代理"}, {"直连", "全部直连，不使用节点"}, {"绕过大陆", "中国大陆网站与 IP 直连，其余走代理"},
	} {
		r, ok := tt.Find(m.label)
		if !ok {
			t.Fatalf("no %s", m.label)
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(time.Second) // past the tooltip delay, on the tester's clock
		if !tt.HasText(m.hint) {
			t.Errorf("%s: no tooltip %q", m.label, m.hint)
		}
		tt.Move(1, 1)
		tt.Frame()
	}
}

// Results of a node in a group with a front proxy say they were measured through the chain.
func TestMeasuredViaTooltip(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks("socks://front.example:1080#前置节点", "")
	g, err := a.AddGroup(app.GroupInput{Name: "链", Front: a.State.Nodes[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	a.ImportLinks("socks://member.example:3333#成员", g)
	member := a.State.Nodes[1].ID
	a.Latency[member] = app.Latency{Duration: 321 * time.Millisecond}
	v := New(a)
	v.group = g
	tt := ui.NewTester(v.Build, 1180, 760)
	tt.Frame()
	r, ok := tt.Find("321 ms")
	if !ok {
		t.Fatalf("no result: %q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	const want = "经代理链测得：前置节点 → 成员"
	tt.Advance(time.Second) // past the tooltip delay, on the tester's clock
	if !tt.HasText(want) {
		t.Fatalf("no tooltip %q: %q", want, tt.Texts())
	}
}

// The exit IP column shows what the last latency test found through each node.
func TestExitIPColumn(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks("socks://a.example:1080#甲\nsocks://b.example:1080#乙\nsocks://c.example:1080#丙", "")
	n := a.State.Nodes
	a.Latency[n[0].ID] = app.Latency{Duration: 120 * time.Millisecond, IP: "203.0.113.7"}
	a.Latency[n[1].ID] = app.Latency{Duration: 90 * time.Millisecond, IPErr: "失败"}
	a.Latency[n[2].ID] = app.Latency{Err: "超时"}
	v := New(a)
	v.group = n[0].GroupID
	tt := ui.NewTester(v.Build, 1180, 760)
	tt.Frame()
	for _, want := range []string{"出口 IP", "203.0.113.7", "失败"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q: %q", want, tt.Texts())
		}
	}
}

// Columns are as wide as the widest they can show, so results coming in never move them, and the name
// takes all the rest.
func TestNodeColumnsStable(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks("socks://a.example:1080#甲\nsocks://b.example:1080#乙", "")
	n := a.State.Nodes
	v := New(a)
	v.group = n[0].GroupID
	tt := ui.NewTester(v.Build, 1180, 760)
	tt.Frame()
	edges := func() []float32 {
		var out []float32
		for _, h := range []string{"协议", "专用端口", "出口 IP", "速度"} {
			r, ok := tt.Find(h)
			if !ok {
				t.Fatalf("no header %q", h)
			}
			out = append(out, r.X)
		}
		return out
	}
	before := edges()
	a.Latency[n[0].ID] = app.Latency{Duration: 8888 * time.Millisecond, IP: "255.255.255.255"}
	a.Latency[n[1].ID] = app.Latency{Duration: 9 * time.Millisecond, IPErr: "失败"}
	a.Speed[n[0].ID] = app.Speed{Down: 999.9 * (1 << 20), Up: 1023 * (1 << 10)}
	a.State.Ports = map[string]int{n[0].ID: 65535}
	tt.Frame()
	if after := edges(); !slices.Equal(before, after) {
		t.Fatalf("columns moved as results came in: %v → %v", before, after)
	}
}
