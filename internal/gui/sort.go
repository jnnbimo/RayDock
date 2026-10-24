package gui

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/egoist/mygo/ui"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type sortColumn int

const (
	sortNone sortColumn = iota
	sortName
	sortLatency
)

// nameOrder compares node names the way people read them: Chinese by pinyin and numbers by value,
// so "节点2" comes before "节点10". Collation keys are cached per name, as the list sorts every frame.
type nameOrder struct {
	coll *collate.Collator
	buf  collate.Buffer
	keys map[string][]byte
}

func newNameOrder() *nameOrder {
	return &nameOrder{coll: collate.New(language.Chinese, collate.Numeric, collate.IgnoreCase), keys: map[string][]byte{}}
}

func (o *nameOrder) key(s string) []byte {
	k, ok := o.keys[s]
	if !ok {
		k = bytes.Clone(o.coll.KeyFromString(&o.buf, s))
		o.buf.Reset()
		o.keys[s] = k
	}
	return k
}

// sortRows orders rows (indexes of nodes) by the column chosen; ties keep their order.
func (a *View) sortRows(rows []int) {
	nodes := a.State.Nodes
	switch a.sortBy {
	case sortName:
		slices.SortStableFunc(rows, func(i, j int) int {
			r := bytes.Compare(a.names.key(nodes[i].Label()), a.names.key(nodes[j].Label()))
			if r == 0 {
				r = cmp.Compare(nodes[i].Address(), nodes[j].Address())
			}
			if a.sortDesc {
				r = -r
			}
			return r
		})
	case sortLatency:
		// Measured nodes by time; failed and untested ones stay last in both directions.
		rank := func(i int) (class int, d int64) {
			l, ok := a.Latency[nodes[i].ID]
			switch {
			case !ok || l.Testing:
				return 2, 0
			case l.Err != "":
				return 1, 0
			}
			return 0, int64(l.Duration)
		}
		slices.SortStableFunc(rows, func(i, j int) int {
			ci, di := rank(i)
			cj, dj := rank(j)
			if ci != cj {
				return cmp.Compare(ci, cj)
			}
			r := cmp.Compare(di, dj)
			if a.sortDesc {
				r = -r
			}
			return r
		})
	}
}

// toggleSort cycles a column through ascending, descending and the original order.
func (a *View) toggleSort(col sortColumn) {
	switch {
	case a.sortBy != col:
		a.sortBy, a.sortDesc = col, false
	case !a.sortDesc:
		a.sortDesc = true
	default:
		a.sortBy, a.sortDesc = sortNone, false
	}
}

// sortHeader is a column header that sorts the list by col when clicked.
func (a *View) sortHeader(c *ui.Context, label string, col sortColumn, alignEnd bool) ui.Element {
	p := a.pal
	active := a.sortBy == col
	b := ui.ButtonBase(c.Key(col)).Gap(4).AlignItems(ui.Center).Cursor(ui.CursorPointer).Shrink(0).
		Label(label).Tooltip("点击排序：升序 → 降序 → 原顺序（原顺序下可拖动节点调整位置）")
	if alignEnd {
		b.Justify(ui.End)
	} else {
		b.Justify(ui.Start)
	}
	fg := p.muted
	if active || b.Hovered() {
		fg = p.text
	}
	// Sorting reorders the rows being built, so it applies after the build.
	b.OnClick(func() { a.toggleSort(col) })
	b.Children(func() {
		ui.Text(c, label).FontSize(11.5).TextColor(fg).SingleLine()
		if active {
			ic := icArrowUp
			if a.sortDesc {
				ic = icArrowDown
			}
			ui.Icon(c, ic).FontSize(11).TextColor(p.on(p.accent))
		}
	})
	return b
}
