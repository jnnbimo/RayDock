package gui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
)

func (a *View) savedToast(c *ui.Context, name string) {
	message := "已保存 " + name
	if a.Conn != app.StateOff && a.SettingsDirty {
		message += "。有设置尚未应用到当前连接，请点击顶部的“应用”。"
	}
	c.Toast(message)
}

func (a *View) pageHeader(c *ui.Context, title, sub string, actions func()) {
	ui.Row(c).Gap(10).AlignItems(ui.End).Children(func() {
		ui.Column(c).Gap(4).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, title).FontSize(22).Bold()
			ui.Text(c, sub).FontSize(12.5).TextColor(a.pal.muted).SingleLine()
		})
		if actions != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(actions)
		}
	})
}

// form is a single-build settings layout; pass the context to each call.
type form struct {
	a *View
	n int
}

func (a *View) section(c *ui.Context, title, desc string, body func(f *form)) {
	p := a.pal
	ui.Column(c).Gap(8).MinWidth(0).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(13).Bold().TextColor(p.text)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(p.muted)
			}
		})
		p.pane(ui.Column(c).Radius(16).Clip()).MinWidth(0).Children(func() {
			body(&form{a: a})
		})
	})
}

func (f *form) divider(c *ui.Context) {
	if f.n > 0 {
		ui.Box(c).Height(1).Margin(0, paneInset).Background(f.a.pal.faint)
	}
	f.n++
}

func (f *form) row(c *ui.Context, title, desc string, control func()) {
	f.divider(c)
	p := f.a.pal
	ui.Row(c).Gap(16).Padding(12, paneInset).MinHeight(56).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(3).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, title).FontSize(13.5)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(p.muted)
			}
		})
		if control != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(control)
		}
	})
}

func (f *form) block(c *ui.Context, body func()) {
	f.divider(c)
	ui.Column(c).Gap(8).Padding(12, paneInset).MinWidth(0).Children(body)
}

func (a *View) chip(c *ui.Context, text string, col ui.Color) ui.Element {
	return ui.Text(c, text).FontSize(11).Bold().TextColor(a.pal.on(col)).Padding(2, 8).Radius(999).Background(col.Alpha(0.16)).SingleLine()
}

func button(c *ui.Context, label string) ui.Element {
	return glassButton(c, ui.Button(c, label), false).Height(controlHeight).Cursor(ui.CursorPointer)
}

func primaryButton(c *ui.Context, label string) ui.Element {
	return glassButton(c, ui.PrimaryButton(c, label), true).Height(controlHeight).Cursor(ui.CursorPointer)
}

func switchControl(c *ui.Context, on *bool) ui.Element {
	t := c.Theme()
	sw := ui.SwitchBase(c, on).Size(t.Space(9), t.Space(5)).Radius(t.Space(2.5)).Cursor(ui.CursorPointer)
	var target float32
	if *on {
		target = 1
	}
	pos := sw.Animate("knob", target, 140*time.Millisecond)
	off := t.Surface
	if sw.Hovered() {
		off = t.SurfaceHover
	}
	tint := off.Mix(t.SurfacePressed, pos)
	edge := t.Border.Mix(raisedEdge(t.Dark, t.Border), pos)
	knob := t.TextMuted.Mix(t.Text, pos)
	lift(t.Dark, sw.Material(glass.Glass{Tint: tint, Interactive: true}).Border(1, edge))
	return sw.Draw(func(p *ui.Painter, r ui.Rect) {
		in := t.Space(0.75)
		d := r.H - 2*in
		k := ui.Rect{X: r.X + in + pos*(r.W-r.H), Y: r.Y + in, W: d, H: d}
		p.Shadow(k, d/2, 0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.25))
		p.Fill(k, knob, d/2)
	})
}

// segmented uses base widgets so each segment can set its cursor.
func segmented(c *ui.Context, selected *int, labels ...string) ui.Element {
	t := c.Theme()
	seg := ui.SegmentedBase(c, selected, len(labels))
	return segmentTrack(t, seg.Track).Children(func() {
		for i, label := range labels {
			segmentItem(t, seg.Segment(i), i == *selected).Padding(t.Space(1), t.Space(3)).
				Children(func() { ui.Text(c, label).SingleLine() })
		}
	})
}

func iconButton(c *ui.Context, ic *ui.SVG, label string) ui.Element {
	return button(c, "").Label(label).Children(func() {
		ui.Icon(c, ic)
		ui.Text(c, label).SingleLine()
	})
}

func primaryIconButton(c *ui.Context, ic *ui.SVG, label string) ui.Element {
	return primaryButton(c, "").Label(label).Children(func() {
		ui.Icon(c, ic)
		ui.Text(c, label).SingleLine()
	})
}

func toolButton(c *ui.Context, ic *ui.SVG, label string, col ui.Color) ui.Element {
	return button(c, "").Label(label).Tooltip(label).Size(controlHeight, controlHeight).Padding(0).Center().Children(func() {
		ui.Icon(c, ic).FontSize(15).TextColor(col)
	})
}

// field calls changed after edits from either typing or its context menu.
func (a *View) field(c *ui.Context, key string, value *string, placeholder string, changed func()) ui.Element {
	p := a.pal
	box := ui.Row(c.Key(key)).Height(controlHeight).Gap(8).Padding(0, 10).AlignItems(ui.Center).
		Radius(a.theme.Radius).Background(a.theme.Surface).Border(1, p.border).Cursor(ui.CursorText)
	var input ui.Element
	box.Children(func() {
		input = a.textMenu(c, ui.TextInputBase(c, value).Placeholder(placeholder).Label(placeholder).Grow(1), key, value, false, changed)
		if changed != nil {
			input.OnChange(changed)
		}
	})
	if box.FocusWithin() {
		box.Border(1, p.accent)
	}
	return input
}

func (a *View) fieldError(c *ui.Context, err error) {
	if err != nil {
		ui.Text(c, err.Error()).FontSize(12).TextColor(a.pal.danger)
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func segmentTrack(t *ui.Theme, track ui.Element) ui.Element {
	return lift(t.Dark, track.Height(controlHeight).AlignItems(ui.Stretch).Padding(t.Space(0.5)).Gap(t.Space(0.5)).Radius(t.Radius).
		Material(glass.Glass{Tint: t.Surface}).Border(1, t.Border))
}

func segmentItem(t *ui.Theme, item ui.Element, chosen bool) ui.Element {
	choosable(item).Center().Radius(max(t.Radius-t.Space(0.5), 0)).Cursor(ui.CursorPointer)
	switch {
	case chosen:
		// A lighter capsule of glass on the track, as iOS chooses a segment.
		item.Material(glass.Glass{Tint: t.SurfacePressed}).Border(1, t.Border).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.12))
	case item.Hovered():
		item.Background(t.SurfaceHover)
	}
	return item
}

// navTabs shares tab styling and lets configure add per-tab behavior.
func navTabs(c *ui.Context, selected *int, n int, content func(i int), configure func(i int, tab ui.Element)) ui.Element {
	t := c.Theme()
	tabs := ui.TabsBase(c, selected, n)
	segmentTrack(t, tabs.List).Shrink(0).AlignSelf(ui.Start).Children(func() {
		for i := range n {
			tab := segmentItem(t, tabs.Tab(i), i == *selected).Gap(6).Padding(t.Space(1), t.Space(3)).Shrink(0)
			if configure != nil {
				configure(i, tab)
			}
			tab.Children(func() { content(i) })
		}
	})
	return tabs.List
}
