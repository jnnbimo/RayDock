package gui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

	"raydock/internal/app"
	"raydock/internal/xray"
)

// The home page: the connection's exit IP and its risk score beside the services it reaches, over the
// traffic of the last minute and of the days.

// ipWidth is the exit IP card's width; the services take the rest of the row. topCardsHeight is the row's
// height, the exit IP card's when it shows an IP, whatever the cards show: the traffic card under it then
// starts and ends with the sidebar's connection panel (MainHeight).
const (
	ipWidth        = 320
	topCardsHeight = 241
)

func (a *View) homePage(c *ui.Context) {
	// Checked when looked at, once for what the connection runs; a switch or an applied change checks again.
	a.CheckOverview(false)
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Padding(4, padPage, 0).Children(func() {
			a.pageHeader(c, "首页", "当前连接的出口 IP、服务连通性与流量", func() {
				if iconButton(c, icRefresh, "重新检测").Disabled(!a.Checkable() || a.Overview.Testing()).Clicked() {
					a.CheckOverview(true)
				}
			})
		})
		ui.Column(c).Grow(1).MinHeight(0).Padding(gapBlock, padPage, padPage).Gap(gapBlock).Children(func() {
			ui.Row(c).Height(topCardsHeight).Shrink(0).Gap(gapBlock).AlignItems(ui.Stretch).Children(func() {
				a.card(c, "出口 IP", "数据来自 IPPure", func() { a.ipCard(c) }).Width(ipWidth).Shrink(0)
				a.card(c, "服务连通", "", func() { a.servicesCard(c) }).Grow(1).MinWidth(0)
			})
			a.card(c, "流量统计", "", func() { a.trafficCard(c) }).Grow(1).MinHeight(0)
		})
	})
}

// card is a home page card: its title, with a note at the right if any, over body.
func (a *View) card(c *ui.Context, title, note string, body func()) ui.Element {
	return a.pal.pane(ui.Column(c).Radius(16).Padding(14, paneInset, paneInset).Gap(12)).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(13).Bold().Grow(1).MinWidth(0).SingleLine()
			if note != "" {
				ui.Text(c, note).FontSize(11).TextColor(a.pal.muted).SingleLine()
			}
		})
		body()
	})
}

// cardNote fills a card that has nothing to show: not connected, or checking.
func (a *View) cardNote(c *ui.Context, spinner bool, text string) {
	ui.Row(c).Grow(1).MinHeight(120).Center().Gap(8).Children(func() {
		if spinner {
			ui.Spinner(c).Size(14, 14)
		}
		ui.Text(c, text).FontSize(12.5).TextColor(a.pal.muted)
	})
}

func (a *View) ipCard(c *ui.Context) {
	p := a.pal
	o := &a.Overview
	switch {
	case a.Conn != app.StateOn:
		a.cardNote(c, false, "连接后显示出口 IP")
		return
	case o.IPTesting || !a.OverviewCurrent():
		a.cardNote(c, true, "正在查询…")
		return
	case o.IPErr != "":
		ui.Column(c).Grow(1).MinHeight(120).Center().Gap(6).Children(func() {
			ui.Text(c, "查询失败").FontSize(13).TextColor(p.danger)
			ui.Text(c, o.IPErr).FontSize(12).TextColor(p.muted).MaxLines(3).Selectable()
		})
		return
	}
	ip := o.IP
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, ip.IP).FontSize(20).Bold().Font("monospace").SingleLine().Grow(1).MinWidth(0).Tooltip(ip.IP).Selectable()
			if toolButton(c, icCopy, "复制 IP", p.text).Clicked() {
				a.Clipboard.WriteText(ip.IP)
				c.Toast("已复制 " + ip.IP)
			}
		})
		place := joinNonEmpty(", ", ip.City, ip.Region, ip.Country)
		ui.Text(c, place).FontSize(12.5).TextColor(p.muted).SingleLine().Tooltip(place)
		org := strings.TrimSpace(fmt.Sprintf("AS%d %s", ip.ASN, ip.ASOrg))
		ui.Text(c, org).FontSize(12).TextColor(p.muted).SingleLine().Tooltip(org)
	})
	score := *ip.FraudScore
	label, col := p.riskLevel(score)
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.End).Children(func() {
			ui.Text(c, fmt.Sprint(score)).FontSize(30).Bold().TextColor(col).FontFeatures("tnum").LineHeight(1)
			ui.Column(c).Gap(2).Grow(1).Children(func() {
				ui.Text(c, label).FontSize(13).Bold().TextColor(col)
				ui.Text(c, "风险值 0–100，越低越好").FontSize(11).TextColor(p.muted)
			})
		})
		// The bar of the score along its scale.
		ui.Box(c).Height(6).Radius(3).Background(p.faint).Label(fmt.Sprintf("风险值 %d", score)).Draw(func(pt *ui.Painter, r ui.Rect) {
			w := max(r.W*float32(min(max(score, 0), 100))/100, r.H)
			pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: w, H: r.H}, col, r.H/2)
		})
	})
	ui.Row(c).Gap(6).Wrap().Children(func() {
		if ip.Residential {
			a.chip(c, "住宅 IP", p.success).Tooltip("家庭宽带运营商的 IP")
		} else {
			a.chip(c, "机房 IP", p.warning).Tooltip("数据中心的 IP，较易被网站识别为代理")
		}
		if ip.Broadcast {
			a.chip(c, "广播 IP", p.warning).Tooltip("IP 的注册地与实际使用地不一致")
		} else {
			a.chip(c, "原生 IP", p.success).Tooltip("IP 的注册地与实际使用地一致")
		}
	})
}

// riskLevel names a risk score's band and its color. IPPure publishes no bands; these are the usual ones
// of risk scores out of 100.
func (p palette) riskLevel(score int) (string, ui.Color) {
	switch {
	case score <= 25:
		return "低风险", p.success
	case score <= 50:
		return "轻度风险", p.success.Mix(p.warning, 0.5)
	case score <= 70:
		return "中度风险", p.warning
	}
	return "高风险", p.danger
}

func (a *View) servicesCard(c *ui.Context) {
	if a.Conn != app.StateOn {
		a.cardNote(c, false, "连接后检测常用网站与 AI 服务")
		return
	}
	if !a.OverviewCurrent() {
		a.cardNote(c, true, "正在检测…")
		return
	}
	ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1)).GapX(24).Children(func() {
		for _, ai := range []bool{false, true} {
			ui.Column(c).Gap(2).MinWidth(0).Children(func() {
				title := "常用网站"
				if ai {
					title = "AI 服务"
				}
				ui.Text(c, title).FontSize(11.5).TextColor(a.pal.muted).Padding(0, 0, 4, 0)
				for _, s := range a.Overview.Services {
					if s.AI == ai {
						a.serviceRow(c, s)
					}
				}
			})
		}
	})
}

func (a *View) serviceRow(c *ui.Context, s app.ServiceCheck) {
	p := a.pal
	var status, tip string
	col := p.muted
	switch {
	case s.Testing:
	case s.Err != "":
		status, tip, col = "不可用", s.Err, p.danger
	case s.Refused != "":
		status, tip, col = s.Refused, "服务可以访问，但不向当前出口 IP 提供服务", p.danger
	default:
		ms := s.Latency.Milliseconds()
		status, col = fmt.Sprintf("%d ms", ms), p.latencyColor(ms)
	}
	row := ui.Row(c).Height(30).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(7, 7).Radius(4).Background(col).Shrink(0)
		ui.Text(c, s.Name).FontSize(13).Grow(1).MinWidth(0).SingleLine()
		if s.Testing {
			ui.Spinner(c).Size(12, 12)
		} else {
			ui.Text(c, status).FontSize(12).TextColor(col).FontFeatures("tnum").SingleLine()
		}
	})
	if tip != "" {
		row.Tooltip(tip)
	}
}

// Traffic chart colors.
var (
	downColor = ui.Hex("#22d3ee")
	upColor   = ui.Hex("#a78bfa")
)

func (a *View) trafficCard(c *ui.Context) {
	p := a.pal
	now := time.Now()
	var cur app.Rate
	if n := len(a.Rates); n > 0 && now.Sub(a.Rates[n-1].At) < 2*time.Second {
		cur = a.Rates[n-1]
	}
	if n := len(a.Rates); n > 0 && now.Sub(a.Rates[n-1].At) < app.RateWindow+time.Second {
		c.After(time.Second) // the rates move out of the window
	}
	ui.Row(c).Grow(1).MinHeight(0).Gap(gapBlock + 8).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(8).Children(func() {
			ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
				a.rateLegend(c, downColor, "下载", cur.Down)
				a.rateLegend(c, upColor, "上传", cur.Up)
			})
			a.rateChart(c, now)
		})
		a.trafficTable(c, now)
	})
	ui.Text(c, "统计经代理和直连的流量，不含节点的延迟与速度测试").FontSize(11).TextColor(p.muted)
}

func (a *View) rateLegend(c *ui.Context, col ui.Color, label string, rate float64) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(8, 8).Radius(2).Background(col)
		ui.Text(c, label).FontSize(12).TextColor(a.pal.muted)
		ui.Text(c, formatSpeed(rate)).FontSize(12.5).Bold().FontFeatures("tnum")
	})
}

// rateChart draws the rates of the last minute, download filled and upload over it.
func (a *View) rateChart(c *ui.Context, now time.Time) {
	p := a.pal
	// The window ends at the next whole second: the chart moves a second at a time, as the rates come, and
	// the frames in between (a resize, the pointer) draw the same paths, not new ones.
	from := now.Truncate(time.Second).Add(time.Second - app.RateWindow)
	var peak float64
	for _, r := range a.Rates {
		peak = max(peak, r.Up, r.Down)
	}
	// An idle minute keeps a scale of a few KB/s rather than one of bytes.
	peak = max(peak, 10<<10)
	unit, name := rateUnit(peak)
	y := chart.NewLinear(0, peak/unit).Nice(3)
	y.Format = func(v float64) string { return trimFloat(v) + " " + name }
	ticks := y.Ticks(3)
	xs := make([]float32, len(a.Rates))
	up, down := make([]float32, len(a.Rates)), make([]float32, len(a.Rates))
	for i, r := range a.Rates {
		xs[i] = float32(r.At.Sub(from).Seconds() / app.RateWindow.Seconds())
		up[i], down[i] = y.At(r.Up/unit), y.At(r.Down/unit)
	}
	label := func(_ chart.Side, t chart.Tick) {
		ui.Text(c, t.Label).NoWrap().FontSize(10.5).TextColor(p.muted).FontFeatures("tnum")
	}
	bottom := []chart.Tick{{At: 0, Label: "60 秒前"}, {At: 1, Label: "现在"}}
	chart.Plot(c, chart.Axes{Left: ticks, Bottom: bottom, Label: label}, func(area ui.Element) {
		area.Draw(func(pt *ui.Painter, r ui.Rect) {
			for _, t := range ticks {
				yy := chart.Y(r, t.At)
				pt.Line(r.X, yy, r.X+r.W, yy, 1, p.faint)
			}
			if len(xs) < 2 {
				return
			}
			// The rate kept from before the window starts left of the plot. An element's Clip clips its
			// children, not what it draws itself.
			pt.Clip(r, 0, func() {
				pt.FillPath(chart.Area(r, xs, nil, down, chart.CurveMonotone), downColor.Alpha(0.18))
				pt.StrokePath(chart.Line(r, xs, down, chart.CurveMonotone), 1.5, downColor)
				pt.StrokePath(chart.Line(r, xs, up, chart.CurveMonotone), 1.5, upColor)
			})
		})
		if len(a.Rates) == 0 {
			chart.Anchor(c, 0.5, 0.5, ui.Center, ui.Center, func() {
				ui.Text(c, "暂无流量").NoWrap().FontSize(12).TextColor(p.muted)
			})
		}
	}).Grow(1).MinHeight(0).GapX(8).GapY(4).Label("最近一分钟的速率").Description("下载与上传速率")
}

// trafficTable lists the traffic of today, yesterday and this month, through the proxy and direct.
func (a *View) trafficTable(c *ui.Context, now time.Time) {
	p := a.pal
	rows := []struct {
		label string
		t     xray.Traffic
	}{
		{"今日", a.TrafficOn(now)},
		{"昨日", a.TrafficOn(now.AddDate(0, 0, -1))},
		{"本月", a.TrafficMonth(now)},
	}
	cell := func(up, down int64) {
		ui.Column(c).Gap(2).Tooltip(fmt.Sprintf("上传 %s · 下载 %s", formatBytes(up), formatBytes(down))).Children(func() {
			ui.Text(c, formatBytes(up+down)).FontSize(13.5).Bold().FontFeatures("tnum").SingleLine()
			ui.Text(c, "↑ "+formatBytes(up)+"  ↓ "+formatBytes(down)).FontSize(11).TextColor(p.muted).FontFeatures("tnum").SingleLine()
		})
	}
	head := func(s string) { ui.Text(c, s).FontSize(11.5).TextColor(p.muted) }
	ui.Grid(c).Width(330).Shrink(0).ColumnTracks(ui.FitContent(), ui.Fr(1), ui.Fr(1)).GapX(16).GapY(12).AlignItems(ui.Center).
		Label("流量统计").Children(func() {
		head("")
		head("代理")
		head("直连")
		for _, r := range rows {
			ui.Text(c, r.label).FontSize(12.5)
			cell(r.t.ProxyUp, r.t.ProxyDown)
			cell(r.t.DirectUp, r.t.DirectDown)
		}
	})
}

// rateUnit picks the unit rates up to peak are shown in.
func rateUnit(peak float64) (float64, string) {
	switch {
	case peak >= 1<<20:
		return 1 << 20, "MB/s"
	case peak >= 1<<10:
		return 1 << 10, "KB/s"
	}
	return 1, "B/s"
}

func formatSpeed(bps float64) string {
	unit, name := rateUnit(bps)
	return trimFloat(math.Round(bps/unit*10)/10) + " " + name
}

func formatBytes(n int64) string {
	const k = 1024
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= k && i < len(units)-1 {
		v /= k
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// trimFloat writes v with at most a decimal, none when it is whole.
func trimFloat(v float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" && (len(out) == 0 || out[len(out)-1] != s) {
			out = append(out, s)
		}
	}
	return strings.Join(out, sep)
}
