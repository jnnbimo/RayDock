package gui

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// dialog is one dialog of the window. Every dialog is drawn by View.dialog, so that all look and behave
// alike: a pane of glass over the window, blurred and dimmed; a header; a body that scrolls when the
// window is too short for it; the error of the last action, kept in view above the buttons; and the
// buttons, Cancel before the actions at the end.
type dialog struct {
	width float32
	title string
	icon  *ui.SVG  // shown in a tile of tone before the title; nil for none
	tone  ui.Color // the icon's color
	head  func()   // after the title, on its row
	body  func()
	// err is the error of the last action, shown until the dialog closes; nil for a dialog without one.
	err     *string
	start   func() // buttons at the start of the footer
	actions func() // buttons after Cancel
	// focusCancel gives Cancel the keyboard as the dialog opens, for a dialog asking to delete.
	focusCancel bool
}

// Dialog sizes, in DIPs.
const (
	dialogMargin = 24 // between the dialog and the window's edges
	dialogPad    = 20
	dialogRadius = 22
	focusInset   = 3 // room for focus rings around the fields
)

// dialog shows d while *open; closing it, by Cancel, Escape or a click beside it, clears its error.
func (a *View) dialog(c *ui.Context, open *bool, d dialog) {
	if !*open {
		if d.err != nil {
			*d.err = ""
		}
		return
	}
	p, t := a.pal, a.theme
	w, h := c.Size()
	ui.DialogBase(c, open, func(back, panel ui.Element) {
		// The window shows through, blurred and dimmed, so the dialog stands out from it and its glass
		// still has light to refract.
		back.Material(glass.Blur{Radius: 6}).Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(r, ui.RGBA(0, 0, 0, 0.18), 0)
		})
		p.popup(panel.Radius(dialogRadius))
		// The drop-downs of the dialog keep their options in it.
		a.popupArea = panel.Bounds()
		defer func() { a.popupArea = ui.Rect{} }()
		// Every part is inset by focusInset more, which the body keeps around its fields so that their focus
		// rings are not clipped by its scrolling: the edges of all parts line up.
		ui.Column(c).Width(min(d.width, w-2*dialogMargin)).MaxHeight(h - 2*dialogMargin).
			Padding(dialogPad - focusInset).Gap(t.Space(3)).Children(func() {
			ui.Row(c).Gap(12).Padding(0, focusInset).AlignItems(ui.Center).Shrink(0).Children(func() {
				if d.icon != nil {
					ui.Box(c).Size(36, 36).Radius(10).Center().Background(d.tone.Alpha(0.14)).Shrink(0).Children(func() {
						ui.Icon(c, d.icon).FontSize(18).TextColor(p.on(d.tone))
					})
				}
				ui.Text(c, d.title).FontSize(17).Bold().SingleLine()
				if d.head != nil {
					d.head()
				}
			})
			// The body gives up height first, scrolling.
			ui.Scroll(c.Key("dialog-body")).Shrink(1).MinHeight(0).Padding(focusInset).Children(func() {
				ui.Column(c).Gap(t.Space(3)).Children(d.body)
			})
			// The error shows in the footer, beside the buttons, so that it changes neither the dialog's
			// height nor where the dialog is: a row of its own would grow a short dialog, centered, both up
			// and down, and squeeze the body of a tall one.
			ui.Row(c).Gap(8).Padding(0, focusInset).AlignItems(ui.Center).Shrink(0).Children(func() {
				if d.start != nil {
					d.start()
				}
				if d.err != nil && *d.err != "" {
					a.errorBar(c, *d.err)
				} else {
					ui.Spacer(c)
				}
				cancel := button(c, "取消")
				if d.focusCancel {
					cancel.AutoFocus()
				}
				if cancel.Clicked() {
					*open = false
					if d.err != nil {
						*d.err = ""
					}
				}
				if d.actions != nil {
					d.actions()
				}
			})
		})
	})
}

// errorBar shows err, the error of an action, as dialogs show theirs: a tinted strip with an icon filling
// the footer up to the buttons, as high as they are, on one line; the whole error shows in its tooltip, and
// its menu copies it.
func (a *View) errorBar(c *ui.Context, err string) {
	p := a.pal
	bar := ui.Row(c).Gap(8).Padding(0, 12).Height(controlHeight).Radius(10).AlignItems(ui.Center).Grow(1).MinWidth(0).
		Background(p.danger.Alpha(0.12)).Border(1, p.danger.Alpha(0.35)).Tooltip(err)
	bar.Children(func() {
		ui.Icon(c, icAlert).FontSize(15).TextColor(p.on(p.danger)).Shrink(0)
		ui.Text(c, err).FontSize(12.5).TextColor(p.on(p.danger)).SingleLine().Grow(1).MinWidth(0)
	})
	a.copyMenu(c, bar, "dialog-error", err)
}
