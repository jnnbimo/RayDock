package gui

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// The context menus of text: the window's own glass menu (ctxMenu) in place of the system's editing
// menu. MyGo has no call that runs an editing command on an input, so these edit the value an input is
// bound to, which it shows from the next frame, and give the selection back to it then (pendingSel).
// Undo stays on Ctrl+Z.

// pendingSel is a selection for a text input to take as it builds next: after an edit by its menu, the
// text it holds is the new value only from then on.
type pendingSel struct {
	id   string
	a, b int
	set  bool
}

// restoreSel gives in the keyboard focus and the selection its menu left it.
func (a *View) restoreSel(in ui.Element, id string) {
	if s := a.editSel; s.set && s.id == id {
		a.editSel = pendingSel{}
		in.Focus().SetTextSelection(s.a, s.b)
	}
}

// selectLater has in select runes a to b as it builds next, and asks for that frame.
func (a *View) selectLater(c *ui.Context, id string, start, end int) {
	a.editSel = pendingSel{id: id, a: start, b: end, set: true}
	c.AnimationFrame()
}

// textMenu gives in, an input editing value, its menu: cut, copy, paste, delete and select all. id is
// unique in the window; multiline is a text area's, where pasted lines stay lines; changed, when not nil,
// runs after an item changed value, as the input's OnChange would.
func (a *View) textMenu(c *ui.Context, in ui.Element, id string, value *string, multiline bool, changed func()) ui.Element {
	id = "text:" + id
	a.restoreSel(in, id)
	a.takeRightClick(in, id)
	if !a.menuFor(id) {
		return in
	}
	m := &a.menu
	if m.fresh {
		m.selA, m.selB = in.TextSelection()
	}
	runes := []rune(*value)
	sa, sb := min(max(m.selA, 0), len(runes)), min(max(m.selB, 0), len(runes))
	if sa > sb {
		sa, sb = sb, sa
	}
	selected := string(runes[sa:sb])
	replace := func(s string) {
		*value = string(runes[:sa]) + s + string(runes[sb:])
		caret := sa + utf8.RuneCountInString(s)
		a.selectLater(c, id, caret, caret)
		if changed != nil {
			changed()
		}
	}
	a.drawMenu(c, []menuItem{
		{label: "剪切", disabled: sa == sb, act: func() {
			a.Clipboard.WriteText(selected)
			replace("")
		}},
		{label: "复制", disabled: sa == sb, act: func() { a.Clipboard.WriteText(selected) }},
		{label: "粘贴", act: func() {
			s := strings.ReplaceAll(a.Clipboard.ReadText(), "\r\n", "\n")
			if !multiline {
				s = strings.ReplaceAll(s, "\n", " ")
			}
			if s != "" {
				replace(s)
			}
		}},
		{label: "删除", disabled: sa == sb, act: func() { replace("") }},
		{},
		{label: "全选", disabled: len(runes) == 0, act: func() { a.selectLater(c, id, 0, len(runes)) }},
	})
	return in
}

// numberMenu gives in, a number input of value from lo to hi, its menu: copy the number, paste one over
// it, select it. changed, when not nil, runs after a paste changed value, as the input's Changed would
// report. The input and its buttons are controlHeight high, as the other fields.
func (a *View) numberMenu(c *ui.Context, in ui.Element, id string, value *float64, lo, hi float64, changed func()) ui.Element {
	in.AlignItems(ui.Stretch).Height(controlHeight)
	id = "number:" + id
	a.restoreSel(in, id)
	a.takeRightClick(in, id)
	if !a.menuFor(id) {
		return in
	}
	text := strconv.FormatFloat(*value, 'f', -1, 64)
	a.drawMenu(c, []menuItem{
		{label: "复制", act: func() { a.Clipboard.WriteText(text) }},
		{label: "粘贴", act: func() {
			// ParseFloat takes "NaN" and "Inf" as well, which no range holds: min and max keep NaN.
			v, err := strconv.ParseFloat(strings.TrimSpace(a.Clipboard.ReadText()), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				c.Toast("剪贴板中不是数字")
				return
			}
			*value = min(max(v, lo), hi)
			a.selectLater(c, id, 0, len(strconv.FormatFloat(*value, 'f', -1, 64)))
			if changed != nil {
				changed()
			}
		}},
		{},
		{label: "全选", act: func() { a.selectLater(c, id, 0, len(text)) }},
	})
	return in
}

// copyMenu gives e, selectable text showing text, its menu: copy what is selected of it, or all of it.
func (a *View) copyMenu(c *ui.Context, e ui.Element, id, text string) ui.Element {
	id = "copy:" + id
	a.takeRightClick(e, id)
	if !a.menuFor(id) {
		return e
	}
	a.drawMenu(c, []menuItem{{label: "复制", act: func() {
		if s := selectedText(e); s != "" {
			text = s
		}
		a.Clipboard.WriteText(text)
	}}})
	return e
}

// selectedText is what the selection of e, a selectable text or container, holds, its paragraphs on
// lines of their own.
func selectedText(e ui.Element) string {
	var parts []string
	for _, s := range e.SelectedTexts() {
		r := []rune(s.Text)
		if a, b := min(max(s.Start, 0), len(r)), min(max(s.End, 0), len(r)); a < b {
			parts = append(parts, string(r[a:b]))
		}
	}
	return strings.Join(parts, "\n")
}
