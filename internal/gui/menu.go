package gui

import (
	"github.com/egoist/mygo/ui"
)

// ctxMenu is the context menu shown in the window, one at a time. MyGo opens and closes it
// (ContextMenuBase); the window draws it, in its glass, in place of every system menu.
type ctxMenu struct {
	id string // what it is for: "node:" and a node's ID, or the key of a text (edit.go); "" for none
	// selA and selB are the selection of the text input the menu is for, as it opened.
	selA, selB int
}

// menuWidth is the width of every context menu, whatever its items, so that all are alike.
const menuWidth = 168

// menuItem is an item of a ctxMenu; one without a label separates the others.
type menuItem struct {
	label    string
	disabled bool
	danger   bool
	act      func()
}

// contextMenu gives e its context menu, which id names: it opens where e is right-clicked, or for the
// menu key or Shift+F10 while e has the keyboard, and closes on a choice, Escape or a press outside.
// opening runs as it opens, and items gives its items while it shows. The item chosen runs once the
// window is built (Build), so that it may remove what the menu is for.
func (a *View) contextMenu(c *ui.Context, e ui.Element, id string, opening func(), items func() []menuItem) {
	open := a.menu.id == id
	m := ui.ContextMenuBase(c, e, &open)
	switch {
	case !open:
		if a.menu.id == id {
			a.menu = ctxMenu{}
		}
		return
	case a.menu.id != id:
		a.menu = ctxMenu{id: id}
		if opening != nil {
			opening()
		}
	}
	p, t := a.pal, a.theme
	m.Popup(func(panel ui.Element) {
		p.popup(panel.Label("菜单").Width(menuWidth).Padding(t.Space(1)).Gap(1).Radius(t.Radius + 4))
		for _, it := range items() {
			if it.label == "" {
				ui.Box(c).Height(1).Margin(t.Space(1), 0).Background(p.border)
				continue
			}
			item := choosable(m.Item()).Label(it.label).Cursor(ui.CursorPointer).AlignItems(ui.Center).
				Padding(t.Space(1.5), t.Space(3.5)).Radius(t.Radius).Disabled(it.disabled)
			col := p.text
			switch {
			case it.disabled:
				col = p.muted.Alpha(0.6)
			case item.Highlighted():
				p.chosen(item)
			case it.danger:
				col = p.danger
			}
			item.Children(func() { ui.Text(c, it.label).FontSize(13).TextColor(col).SingleLine() })
			if item.Clicked() {
				a.menuAct = it.act
			}
		}
	})
	if !open {
		a.menu = ctxMenu{}
	}
}

func (a *View) menuFor(id string) bool { return a.menu.id == id }
