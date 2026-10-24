package gui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app/apptest"
)

// Every page starts alike: its title and the line about it, then its toolbar row, at the same place on
// every page; the content of every pane starts as far in from the pane's edge, a list's column headers
// over their rows; and the sidebar's panel ends where the pages do.
func TestPagesAlign(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	a.QueueLog("[RayDock] 日志第一行")
	a.FlushLogs()
	v := New(a)
	tt := ui.NewTester(v.Build, MainWidth, MainHeight)
	show := func(path string) {
		v.router.Push(path)
		tt.Frame()
		tt.Advance(600 * time.Millisecond) // the page transition
	}
	find := func(s string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q: %q", s, tt.Texts())
		}
		return r
	}
	near := func(what string, got, want float32) {
		t.Helper()
		if math.Abs(float64(got-want)) > 0.5 {
			t.Errorf("%s at %v, want %v", what, got, want)
		}
	}
	middle := func(r ui.Rect) float32 { return r.Y + r.H/2 }

	show("/nodes")
	sub := find("2 个节点 · 1 个分组")
	tabs := find("默认分组")
	header, row := find("名称 / 地址"), find("a.example.com:443")
	near("the name column's header", header.X, row.X)
	content := row.X // where the content of every pane starts

	show("/logs")
	logSub := find("Xray 核心与 RayDock 的输出，保留最近 2000 行")
	near("the logs' line about them", logSub.X, sub.X)
	near("the logs' line about them", logSub.Y, sub.Y)
	near("a log line", find("[RayDock] 日志第一行").X, content)

	show("/settings")
	setSub := find("修改后自动保存")
	near("the settings' line about them", setSub.X, sub.X)
	near("the settings' line about them", setSub.Y, sub.Y)
	near("the settings tabs", middle(find("常规")), middle(tabs))
	near("a settings section's title", find("启动与外观").X, sub.X)
	near("a settings row", find("开机自启").X, content)

	// The connection panel's last row ends as far above the window's bottom as the pages' content does.
	proxy := find("127.0.0.1:10818")
	if bottom := proxy.Y + proxy.H; bottom > MainHeight-padPage || bottom < MainHeight-padPage-sideInset-12 {
		t.Errorf("the panel's last line ends at %v, not just above %v", bottom, MainHeight-padPage)
	}
}
