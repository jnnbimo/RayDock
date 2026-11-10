package gui

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// Menus edit bound values; MyGo has no public input-edit command API.
// Restore focus and selection after the next build commits the new text.

// pendingSel restores an input selection after the new bound value is committed.
type pendingSel struct {
	id   string
	a, b int
	set  bool
}

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

// textMenu preserves newlines only for multiline inputs.
// id is window-unique; changed also runs for edits made from this menu.
func (a *View) textMenu(c *ui.Context, in ui.Element, id string, value *string, multiline bool, changed func()) ui.Element {
	id = "text:" + id
	a.restoreSel(in, id)
	a.contextMenu(c, in, id, func() { a.menu.selA, a.menu.selB = in.TextSelection() }, func() []menuItem {
		return a.textItems(c, id, value, multiline, changed)
	})
	return in
}

// textItems are the items of a text input's menu, which edit the selection it had as the menu opened.
func (a *View) textItems(c *ui.Context, id string, value *string, multiline bool, changed func()) []menuItem {
	m := &a.menu
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
	return []menuItem{
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
	}
}

// numberInput edits *value, a whole number from lo to hi, as MyGo's NumberInput does: Up and Down and the
// buttons beside it step by step, what is typed applies once it is a number in range, and the text shows
// the value once the input loses the keyboard. It is built here because a right-click on a NumberInput's
// own text input opens the system's editing menu: on this one it opens the window's, whose paste takes
// a number only, kept in range. changed runs as the value changes, however; it returns the text input.
func (a *View) numberInput(c *ui.Context, key, label string, value *float64, lo, hi, step float64, changed func()) ui.Element {
	t := a.theme
	id := "number:" + key
	format := func(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }
	set := func(v float64) {
		if v = min(max(math.Round(v), lo), hi); v != *value {
			*value = v
			if changed != nil {
				changed()
			}
		}
	}
	row := ui.Row(c.Key(key)).Gap(t.Space(1)).Shrink(0).AlignItems(ui.Stretch).Height(controlHeight)
	text := ui.Local(row, "text", func() string { return format(*value) })
	var in ui.Element
	row.Children(func() {
		in = ui.TextInput(c, text).Width(t.Space(20)).Shrink(1).MinWidth(0).Label(label).Range(lo, hi, *value).Step(step)
		a.restoreSel(in, id)
		if in.Changed() {
			if v, err := strconv.ParseFloat(strings.TrimSpace(*text), 64); err == nil && v >= lo && v <= hi {
				set(v)
			}
		}
		for _, k := range []struct {
			key ui.Key
			d   float64
		}{{ui.KeyUp, step}, {ui.KeyDown, -step}} {
			if in.Shortcut(0, k.key) {
				set(*value + k.d)
				*text = format(*value)
			}
		}
		if !in.Focused() {
			*text = format(*value) // what was set, or typed, in full
		}
		a.contextMenu(c, in, id, nil, func() []menuItem {
			shown := *text
			return []menuItem{
				{label: "复制", act: func() { a.Clipboard.WriteText(shown) }},
				{label: "粘贴", act: func() {
					// ParseFloat takes "NaN" and "Inf" as well, which no range holds.
					v, err := strconv.ParseFloat(strings.TrimSpace(a.Clipboard.ReadText()), 64)
					if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
						c.Toast("剪贴板中不是数字")
						return
					}
					set(v)
					*text = format(*value)
					a.selectLater(c, id, 0, len(*text))
				}},
				{},
				{label: "全选", act: func() { a.selectLater(c, id, 0, len([]rune(shown))) }},
			}
		})
		for _, b := range []struct {
			text, name string
			d          float64
		}{{"−", "减少", -step}, {"+", "增加", step}} {
			btn := ui.Button(c, b.text).Padding(t.Space(1), t.Space(2)).Label(b.name).TextColor(t.Text).
				Disabled(b.d < 0 && *value <= lo || b.d > 0 && *value >= hi)
			if btn.Clicked() {
				set(*value + b.d)
				*text = format(*value)
			}
		}
	})
	return in
}

func (a *View) copyMenu(c *ui.Context, e ui.Element, id, text string) ui.Element {
	a.contextMenu(c, e, "copy:"+id, nil, func() []menuItem {
		return []menuItem{{label: "复制", act: func() {
			if s := selectedText(e); s != "" {
				text = s
			}
			a.Clipboard.WriteText(text)
		}}}
	})
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
