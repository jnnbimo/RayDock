package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app/apptest"
)

// Hovering an item of a menu, or choosing a page, moves nothing: chosen items keep the rim of the
// others (choosable).
func TestChoosingMovesNothing(t *testing.T) {
	h := apptest.New(t)
	h.A.ImportLinks(apptest.LinkA, "")
	v := nodesView(h.A)
	tt := ui.NewTester(v.Build, 1180, 760)
	at := func(s string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q: %q", s, tt.Texts())
		}
		return r
	}
	if err := tt.RightClick("a.example.com:443"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	before := at("删除")
	r := at("连接")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if after := at("删除"); after != before {
		t.Fatalf("hovering the first item moved the last from %+v to %+v", before, after)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	settings := at("设置")
	clickFrame(t, tt, "日志")
	if after := at("设置"); after != settings {
		t.Fatalf("choosing a page moved the next from %+v to %+v", settings, after)
	}
}

// The proxies of a group are chosen with the window's one drop-down: its options open below it, where
// the dialog has room for them, and typing goes to the first that has what was typed.
func TestProxyDropDown(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.ImportLinks(apptest.LinkA+"\n"+apptest.LinkB, "")
	v := nodesView(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	v.openGroupDialog("")
	tt.Frame()
	label, _ := tt.Find("前置代理")
	tt.ClickAt(label.X+8, label.Y+label.H+6+12)
	tt.Frame()
	tt.Frame()
	var option string
	for _, s := range tt.Texts() {
		if strings.Contains(s, "节点B") && strings.Contains(s, "·") {
			option = s
		}
	}
	r, ok := tt.Find(option)
	if option == "" || !ok || r.Y < label.Y+label.H+34 {
		t.Fatalf("options %q at %+v, not below the field under %+v", option, r, label)
	}
	tt.Type("节点b")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if v.gf.front != a.State.Nodes[1].ID {
		t.Fatalf("typed to %q, want %q (%s)", v.gf.front, a.State.Nodes[1].ID, option)
	}
}

// A drop-down's options are as wide as it, long names cut short, and stay in the dialog it is in, on the
// side with room for them, whatever the window's size.
func TestDropDownFitsDialog(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	links := apptest.LinkA + "\n" + apptest.LinkB
	for i := range 30 {
		links += fmt.Sprintf("\nss://YWVzLTEyOC1nY206cGFzcw@n%d.example.com:8388#香港 HK-%02d IPLC 专线 一个很长很长很长很长很长很长很长很长很长很长的节点名称", i, i)
	}
	a.ImportLinks(links, "")
	long := a.State.Nodes[2].Label() + " · " + a.GroupByID(a.State.Nodes[2].GroupID).Name
	v := nodesView(a)
	tt := ui.NewTester(v.Build, 1180, 760)
	find := func(s string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q: %q", s, tt.Texts())
		}
		return r
	}
	for _, size := range [][2]int{{1180, 760}, {980, 620}} {
		tt.SetSize(size[0], size[1])
		for _, field := range []string{"前置代理", "后置代理"} {
			v.openGroupDialog("")
			tt.Frame()
			tt.Frame()
			title, cancel, chosen := find("添加分组"), find("取消"), find("不使用")
			top, bottom := title.Y-dialogPad, cancel.Y+cancel.H+dialogPad
			label := find(field)
			if field == "后置代理" {
				chosen.Y = label.Y + label.H + 6 // the second field's choice, under its label
			}
			right := chosen.X + chosen.W + arrowWidth // the field's text, then its arrow
			tt.ClickAt(label.X+8, label.Y+label.H+6+12)
			tt.Frame()
			tt.Frame()
			for _, s := range []string{"节点A · " + a.GroupByID(a.State.Nodes[0].GroupID).Name, long} {
				r := find(s)
				if r.X+r.W > right || r.Y < top || r.Y+r.H > bottom {
					t.Fatalf("%v %s: option %q at %+v, out of the field's width (to %v) or the dialog (%v to %v)",
						size, field, s, r, right, top, bottom)
				}
			}
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			v.gf.open = false
			tt.Frame()
		}
	}
}
