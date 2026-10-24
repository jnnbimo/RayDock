package gui

import (
	"github.com/egoist/mygo/ui"
)

// ctxMenu is the window's one context menu. It is drawn in the window, as glass, rather than by the
// system, whose menus are opaque and keep a column for check marks on the left and one for shortcuts on
// the right: this one is of one width (menuWidth), its items starting at the same place.
//
// Every element with a context menu takes its right clicks (takeRightClick), so that no system menu
// opens over the window: node rows, text inputs and selectable text.
type ctxMenu struct {
	open  bool
	id    string  // what the menu is for: "node:" and a node's ID, or the key of a text (edit.go)
	x, y  float32 // where it opens, in the window
	focus int     // item with the keyboard focus, counted from 1; 0 for none
	// fresh marks the frame the menu opened in: a text input's menu takes its selection then.
	fresh bool
	// selA and selB are the selection of the text input the menu is for, as it opened.
	selA, selB int
	// asked is a right click on an element, relative to it, or its menu key (askedKey); the element
	// places the menu as it builds.
	asked    bool
	askedKey bool
	askedX   float32
	askedY   float32
	askID    string
}

// menuWidth is the width of every context menu, whatever its items, so that all are alike.
const menuWidth = 168

// menuItem is an item of a ctxMenu.
type menuItem struct {
	label    string
	disabled bool
	danger   bool
	act      func()
}

// takeRightClick has e open the menu id where it is right-clicked: a release of the secondary button
// over it, as Windows opens context menus. The left button goes on to e. While e has the keyboard focus
// the menu key and Shift+F10 open it too, below e's start.
func (a *View) takeRightClick(e ui.Element, id string) {
	e.HandleInput(func(ev ui.InputEvent) bool {
		switch {
		case ev.Kind == ui.InputKeyDown && (ev.Key == ui.KeyContextMenu && ev.Mods == 0 || ev.Key == ui.KeyF10 && ev.Mods == ui.Shift):
			a.menu.asked, a.menu.askedKey, a.menu.askID = true, true, id
			return true
		case ev.Button != 1:
			return false
		case ev.Kind == ui.InputPointerDown:
			return true // the release comes here too
		case ev.Kind == ui.InputPointerUp:
			a.menu.asked, a.menu.askedKey, a.menu.askID, a.menu.askedX, a.menu.askedY = true, false, id, ev.X, ev.Y
			return true
		}
		return false
	})
	if m := &a.menu; m.asked && m.askID == id {
		lx, ly := m.askedX, m.askedY
		if m.askedKey {
			lx, ly = 0, e.LayoutBounds().H
		}
		x, y := e.LocalToWindow(lx, ly)
		a.openMenuAt(id, x, y, m.askedKey)
	}
}

// openMenuAt opens the menu id at a window point; from the keyboard, its first item focused.
func (a *View) openMenuAt(id string, x, y float32, keyboard bool) {
	a.menu = ctxMenu{open: true, id: id, x: x, y: y, fresh: true}
	if keyboard {
		a.menu.focus = 1
	}
}

// menuFor reports whether the open menu is id's.
func (a *View) menuFor(id string) bool { return a.menu.open && a.menu.id == id }

// drawMenu draws the open menu with items, "" labels being separators; choosing one closes the menu,
// then runs its action. It is built after what the menu is for, so actions may change it.
func (a *View) drawMenu(c *ui.Context, items []menuItem) {
	m := &a.menu
	if !m.open {
		return
	}
	m.fresh = false
	p, t := a.pal, a.theme
	// Arrow keys move among the enabled items, wrapping around; from none, Down goes to the first.
	step := func(d int) {
		n, i := len(items), m.focus-1
		if i < 0 && d < 0 {
			i = n
		}
		for range n {
			i = ((i+d)%n + n) % n
			if items[i].label != "" && !items[i].disabled {
				m.focus = i + 1
				return
			}
		}
	}
	ui.Overlay(c, func() {
		anchor := ui.Box(c.Key("menu-at")).Absolute().Left(m.x).Top(m.y).Size(0, 0)
		panel := ui.Column(c.Key("menu")).Role(ui.RoleMenu).Label("菜单").AttachTo(anchor, ui.AnchorTopLeft, ui.AnchorTopLeft).
			Width(menuWidth).Padding(t.Space(1)).Gap(1).Radius(t.Radius + 4)
		p.popup(panel)
		var chosen func()
		panel.Children(func() {
			for i, it := range items {
				if it.label == "" {
					ui.Box(c).Height(1).Margin(t.Space(1), 0).Background(p.border)
					continue
				}
				b := choosable(ui.ButtonBase(c.Key("menu-"+it.label))).Label(it.label).Role(ui.RoleMenuItem).Cursor(ui.CursorPointer).
					Justify(ui.Start).Padding(t.Space(1.5), t.Space(3.5)).Radius(t.Radius).Disabled(it.disabled).
					FocusBind(&m.focus, i+1)
				col := p.text
				switch {
				case it.disabled:
					col = p.muted.Alpha(0.6)
				case b.Hovered() || b.FocusVisible():
					p.chosen(b)
				case it.danger:
					col = p.danger
				}
				b.Children(func() { ui.Text(c, it.label).FontSize(13).TextColor(col).SingleLine() })
				if b.Clicked() {
					chosen = it.act
				}
			}
		})
		switch {
		case panel.OverlayShortcut(0, ui.KeyDown):
			step(1)
		case panel.OverlayShortcut(0, ui.KeyUp):
			step(-1)
		case panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside():
			a.menu = ctxMenu{}
		}
		if chosen != nil {
			a.menu = ctxMenu{}
			chosen()
		}
	})
}
