package gui

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
	"raydock/internal/probe"
	"raydock/internal/xray"
)

func TestHomePage(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	tt := ui.NewTester(New(a).Build, MainWidth, MainHeight)
	for _, want := range []string{"首页", "连接后显示出口 IP", "连接后检测常用网站与 AI 服务", "暂无流量", "今日", "本月"} {
		if !tt.HasText(want) {
			t.Fatalf("not connected: no %q in %q", want, tt.Texts())
		}
	}

	// Connected and checked: the IP, its score and kind, and each service's latency or why it fails.
	a.ImportLinks(apptest.LinkA, "")
	a.Conn, a.ActiveID, a.Since = app.StateOn, a.State.Nodes[0].ID, time.Now()
	score := 63
	a.Overview = app.Overview{Key: a.OverviewKey(),
		IP: probe.IPInfo{IP: "203.0.113.9", ASN: 31898, ASOrg: "OCPL", Country: "Japan", Region: "Osaka", City: "Osaka", FraudScore: &score, Broadcast: true},
		Services: []app.ServiceCheck{
			{Name: "Google", Latency: 120 * time.Millisecond},
			{Name: "GitHub", Testing: true},
			{Name: "Netflix", Latency: 430 * time.Millisecond},
			{Name: "ChatGPT", AI: true, Refused: "地区不支持"},
			{Name: "Claude", AI: true, Err: "超时"},
		}}
	day := time.Now()
	a.Traffic.Days = map[string]xray.Traffic{day.Format(time.DateOnly): {ProxyUp: 3 << 20, ProxyDown: 1536 << 20, DirectDown: 2048}}
	a.Rates = []app.Rate{{At: day.Add(-time.Second), Down: 2 << 20}, {At: day, Up: 1 << 10, Down: 3 << 20}}
	tt.Frame()
	for _, want := range []string{"203.0.113.9", "数据来自 IPPure", "Osaka, Japan", "AS31898 OCPL", "63", "中度风险", "机房 IP", "广播 IP",
		"常用网站", "AI 服务", "120 ms", "430 ms", "地区不支持", "不可用", "1.5 GB", "↑ 3.0 MB  ↓ 1.5 GB", "2.0 KB", "3 MB/s", "1 KB/s"} {
		if !tt.HasText(want) {
			t.Fatalf("connected: no %q in %q", want, tt.Texts())
		}
	}
	if !a.OverviewCurrent() || a.Overview.Services[0].Latency == 0 {
		t.Fatal("the page must not check again what was checked")
	}
	if err := tt.Click("复制 IP"); err != nil {
		t.Fatal(err)
	}
	if h.Clip.Text != "203.0.113.9" {
		t.Fatalf("copied %q", h.Clip.Text)
	}
}

// The rate kept from before the window starts left of the plot, which shows only what is within it: left of
// the plot, the page is the same with it as without it.
func TestRateChartStaysInPlot(t *testing.T) {
	render := func(before bool) (*image.RGBA, ui.Rect) {
		h := apptest.New(t)
		a := h.A
		a.ImportLinks(apptest.LinkA, "")
		a.Conn, a.ActiveID, a.Since = app.StateOn, a.State.Nodes[0].ID, time.Now()
		now := time.Now()
		a.Rates = []app.Rate{{At: now.Add(-2 * time.Second), Down: 2 << 20}, {At: now.Add(-time.Second), Down: 1 << 20}, {At: now, Down: 2 << 20}}
		if before {
			a.Rates = append([]app.Rate{{At: now.Add(-5 * time.Minute), Down: 1 << 20, Up: 1 << 20}}, a.Rates...)
		}
		tt := ui.NewTester(New(a).Build, MainWidth, MainHeight)
		tt.Frame()
		r, ok := tt.Find("60 秒前") // centered under the plot's left edge
		if !ok {
			t.Fatalf("no axis label in %q", tt.Texts())
		}
		return tt.Image(), r
	}
	with, r := render(true)
	without, _ := render(false)
	left := int(r.X + r.W/2)
	for y := int(r.Y) - 150; y < int(r.Y)-2; y++ {
		for x := left - 60; x < left-1; x++ {
			if with.RGBAAt(x, y) != without.RGBAAt(x, y) {
				t.Fatalf("the chart draws left of the plot at (%d, %d)", x, y)
			}
		}
	}
}

type fakeWindow struct{ minimized, closed int }

func (w *fakeWindow) Minimize()       { w.minimized++ }
func (w *fakeWindow) Close()          { w.closed++ }
func (w *fakeWindow) IsFocused() bool { return true }

// The window has a fixed size: its title bar has minimize and close, and no maximize.
func TestWindowButtons(t *testing.T) {
	h := apptest.New(t)
	v := New(h.A)
	w := &fakeWindow{}
	v.SetWindow(w)
	tt := ui.NewTester(v.Build, MainWidth, MainHeight)
	tt.Frame()
	if tt.HasText("\ue922") {
		t.Error("a maximize button shows")
	}
	// The glyph is centered in its button, which starts at the window's top.
	if r, ok := tt.Find("\ue8bb"); !ok || r.Y+r.H/2 > titleBarHeight/2+1 {
		t.Errorf("the close button is not at the window's top: glyph at %v", r)
	}
	if err := tt.Click("\ue921"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("\ue8bb"); err != nil {
		t.Fatal(err)
	}
	if w.minimized != 1 || w.closed != 1 {
		t.Errorf("minimized %d times, closed %d times", w.minimized, w.closed)
	}
	// They take the pointer, not the keyboard focus: Tab goes around the page without them.
	for range 30 {
		tt.Key(0, ui.KeyTab)
		tt.Frame()
		if tt.Focused("最小化") || tt.Focused("关闭") {
			t.Fatal("Tab focuses a window button")
		}
	}
}

// A display too short for the window leaves it shorter (main.go's fitWindow): the page keeps its layout and
// scrolls, so its bottom, the connection panel's last line, can be reached.
func TestShortWindowScrolls(t *testing.T) {
	h := apptest.New(t)
	const height = MainHeight - 140
	tt := ui.NewTester(New(h.A).Build, MainWidth, height)
	tt.Frame()
	addr, ok := tt.Find(h.A.ProxyAddr())
	if !ok || addr.Y < height {
		t.Fatalf("the page should be laid out at its height, under the window's bottom: %v", addr)
	}
	tt.Wheel(sideWidth/2, height/2, 0, 400)
	for range 20 {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	if addr, _ = tt.Find(h.A.ProxyAddr()); addr.Y+addr.H > height {
		t.Fatalf("scrolled, the panel's last line should show: %v", addr)
	}
}

func TestFormatTraffic(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{{0, "0 B"}, {1023, "1023 B"}, {1536, "1.5 KB"}, {150 << 20, "150 MB"}, {5 << 40, "5.0 TB"}} {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("%d: %q", c.n, got)
		}
	}
	if formatSpeed(0) != "0 B/s" || formatSpeed(1.25*(1<<20)) != "1.3 MB/s" {
		t.Error(formatSpeed(0), formatSpeed(1.25*(1<<20)))
	}
}
