package gui

import (
	"cmp"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/node"
)

func (a *View) controlPanel(c *ui.Context) {
	p := a.pal
	p.pane(ui.Column(c).Gap(12).Padding(12).Radius(16)).Shrink(0).Children(func() {
		a.connectionControls(c, false)
		ui.Box(c).Height(1).Background(p.faint)
		a.localProxyRow(c)
	})
}

// Rows of the connection controls, in DIPs, when they are laid out at a fixed height (connectionControls).
const (
	rowStatus = 40 // the power button
	rowSwitch = 26 // "switch to" the node selected
	rowError  = 16 // an error, on one line
	rowMode   = controlHeight
	rowToggle = 28
	toggleGap = 6
)

// Sidebar and tray share these controls; fixed rows let the flyout size itself before painting.
func (a *View) connectionControls(c *ui.Context, fixed bool) {
	p := a.pal
	s := &a.State.Settings
	errorLine := func(key, text string, lines int) {
		e := ui.Text(c, text).FontSize(11.5).TextColor(p.danger).Selectable()
		if fixed {
			e.Height(rowError).SingleLine().Tooltip(text)
			return
		}
		a.copyMenu(c, e.MaxLines(lines), key, text)
	}
	a.connectionStatus(c)
	if a.ConnErr != "" {
		errorLine("conn-error", a.ConnErr, 4)
	}
	mode := ui.Column(c).Justify(ui.Center)
	if fixed {
		mode.Height(rowMode)
	}
	mode.Children(func() { a.modeSegmented(c) })
	ui.Column(c).Gap(toggleGap).Children(func() {
		a.switchRow(c, "sysproxy", "系统代理", "", &s.SystemProxy, func() {
			a.Save()
			a.SyncProxy()
		})
		if a.ProxyErr != "" {
			errorLine("proxy-error", a.ProxyErr, 3)
		}
		hint := ""
		if !a.Elevated {
			hint = "需管理员"
		}
		a.switchRow(c, "tun-quick", "TUN 模式", hint, &s.TUN.Enabled, func() { a.SetTUN(s.TUN.Enabled) })
	})
}

func (a *View) connectionHeight(gap float32) float32 {
	rows := []float32{rowStatus, rowMode, 2*rowToggle + toggleGap}
	if a.switchTarget() != nil {
		rows = append(rows, rowSwitch)
	}
	if a.ConnErr != "" {
		rows = append(rows, rowError)
	}
	if a.ProxyErr != "" {
		rows[2] += toggleGap + rowError
	}
	h := gap * float32(len(rows)-1)
	for _, r := range rows {
		h += r
	}
	return h
}

func (a *View) switchTarget() *node.Node {
	if a.Conn == app.StateOff || a.ActiveID == a.State.SelectedID {
		return nil
	}
	return a.NodeByID(a.State.SelectedID)
}

func (a *View) connectionStatus(c *ui.Context) {
	p := a.pal
	var title, sub string
	col := p.muted
	switch a.Conn {
	case app.StateOff:
		title = "未连接"
		if n := a.NodeByID(a.ConnectTarget()); n != nil {
			sub = n.Label()
		} else {
			sub = "请先选择节点"
		}
	case app.StateConnecting:
		title, col = "正在连接…", p.warning
		if a.WaitCore {
			sub = cmp.Or(a.CoreMsg, "正在准备 Xray 核心")
			if a.CoreProgress > 0 {
				sub = fmt.Sprintf("%s %d%%", sub, int(a.CoreProgress*100))
			}
		} else if n := a.NodeByID(a.ActiveID); n != nil {
			sub = n.Label()
		}
	case app.StateOn:
		title, col = "已连接 · "+formatDuration(time.Since(a.Since)), p.success
		if n := a.NodeByID(a.ActiveID); n != nil {
			sub = n.Label()
		}
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		a.powerButton(c)
		ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, title).FontSize(13).Bold().TextColor(col).FontFeatures("tnum").SingleLine()
			if sub != "" {
				ui.Text(c, sub).FontSize(11.5).TextColor(p.muted).SingleLine().Tooltip(sub)
			}
		})
	})
	if n := a.switchTarget(); n != nil {
		if button(c, "切换到 "+n.Label()).FontSize(12).Height(rowSwitch).Padding(0, 10).SingleLine().Clicked() {
			a.connect(c, n.ID)
		}
	}
}

func (a *View) powerButton(c *ui.Context) {
	p := a.pal
	col, tip := p.powerColor(a.Conn)
	b := ui.ButtonBase(c.Key("power")).Size(40, 40).Radius(20).Shrink(0).Center().Cursor(ui.CursorPointer).
		Label("连接开关").Tooltip(tip)
	// Interactive glass grows while pressed, so the button's own pose only answers hover.
	p.signalGlass(b, col, b.Hovered())
	b.Children(func() {
		ic := ui.Icon(c, icPower).FontSize(18).TextColor(p.on(col))
		if a.Conn == app.StateConnecting {
			ic.Opacity(0.4 + 0.6*ic.Loop("pulse", 900*time.Millisecond, ui.Bounce(ui.EaseInOut)))
		}
	})
	pose := ui.Pose{}
	if b.Hovered() && !b.Pressed() {
		pose = ui.Pose{Scale: 1.05}
	}
	b.AnimatePose(pose, ui.Snappy)
	if b.Clicked() {
		a.togglePower(c)
	}
}

func (a *View) switchRow(c *ui.Context, key, title, hint string, value *bool, onChange func()) {
	p := a.pal
	ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(28).Children(func() {
		ui.Text(c, title).FontSize(13)
		if hint != "" {
			ui.Text(c, hint).FontSize(11).TextColor(p.warning)
		}
		ui.Spacer(c)
		switchControl(c.Key(key), value).Label(title).OnChange(onChange)
	})
}

func (a *View) localProxyRow(c *ui.Context) {
	p := a.pal
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "本地代理").FontSize(11.5).TextColor(p.muted)
				switch t := a.RealTest; {
				case t.Testing:
					ui.Spinner(c).Size(11, 11)
				case t.Err != "":
					ui.Text(c, t.Err).FontSize(11.5).TextColor(p.danger).SingleLine().Tooltip(t.Err)
				case t.Duration > 0:
					ms := t.Duration.Milliseconds()
					ui.Textf(c, "%d ms", ms).FontSize(11.5).Bold().FontFeatures("tnum").TextColor(p.latencyColor(ms))
				}
			})
			ui.Text(c, a.ProxyAddr()).FontSize(12.5).Font("monospace").SingleLine()
		})
		if toolButton(c, icGlobe, "测试连接（经代理访问 Google）", p.text).Disabled(a.Conn != app.StateOn || a.RealTest.Testing).Clicked() {
			a.TestReal()
		}
		if toolButton(c, icCopy, "复制地址（SOCKS5 / HTTP）", p.text).Clicked() {
			a.Clipboard.WriteText(a.ProxyAddr())
			c.Toast("已复制 " + a.ProxyAddr())
		}
	})
}
