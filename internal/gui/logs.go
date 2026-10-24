package gui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
)

func (a *View) logsPage(c *ui.Context) {
	p := a.pal
	ui.Column(c).Grow(1).MinHeight(0).Padding(4, padPage, padPage).Gap(gapBlock).Children(func() {
		a.pageHeader(c, "日志", fmt.Sprintf("Xray 核心与 RayDock 的输出，保留最近 %d 行", app.MaxLogs), func() {
			if iconButton(c, icCopy, "复制").Disabled(len(a.Logs) == 0).Clicked() {
				a.copyLogs(c)
			}
			// Clearing after the build: the list below is built from the current lines.
			iconButton(c, icTrash, "清空").Disabled(len(a.Logs) == 0).OnClick(func() { a.Logs = nil })
		})
		a.logLS.FollowEnd = true
		var list ui.Element
		p.pane(ui.Column(c).Grow(1).MinHeight(0).Radius(16).Clip()).Children(func() {
			list = ui.List(c.Key("logs"), &a.logLS, len(a.Logs), func(i int) {
				l := a.Logs[i]
				col := p.text
				switch {
				case l.Level == 2:
					col = p.danger
				case l.Level == 1:
					col = p.warning
				case strings.HasPrefix(l.Text, "[RayDock]"):
					col = p.accent2
				}
				ui.Text(c, l.Text).Font("monospace").FontSize(12).TextColor(col).Padding(2, 0)
			}).Grow(1).Padding(8, paneInset).Selectable().Label("日志").Children(func() {
				if len(a.Logs) == 0 {
					ui.Text(c, "暂无日志").TextColor(p.muted).Padding(24)
				}
			})
		})
		a.logMenu(c, list)
	})
}

// logMenu is the menu of the log list: copy the selection or every line, or clear the log.
func (a *View) logMenu(c *ui.Context, list ui.Element) {
	a.takeRightClick(list, "logs")
	if !a.menuFor("logs") {
		return
	}
	selected := selectedText(list)
	a.drawMenu(c, []menuItem{
		{label: "复制", disabled: selected == "", act: func() { a.Clipboard.WriteText(selected) }},
		{label: "复制全部", disabled: len(a.Logs) == 0, act: func() { a.copyLogs(c) }},
		{},
		{label: "清空日志", danger: true, disabled: len(a.Logs) == 0, act: func() { a.Logs = nil }},
	})
}

// copyLogs copies every line of the log.
func (a *View) copyLogs(c *ui.Context) {
	var b strings.Builder
	for _, l := range a.Logs {
		b.WriteString(l.Text + "\n")
	}
	a.Clipboard.WriteText(b.String())
	c.Toast(fmt.Sprintf("已复制 %d 行", len(a.Logs)))
}
