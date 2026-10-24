package gui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app/apptest"
)

// In the smallest window, a long dialog scrolls its body: the error of a failed save and the buttons
// stay in view, and closing clears the error.
func TestDialogFitsWithError(t *testing.T) {
	h := apptest.New(t)
	const w, ht = 980, 620 // the window's minimum size
	v := New(h.A)
	tt := ui.NewTester(v.Build, w, ht)
	v.openAddNode()
	v.nf.mode = 1 // fill in a node, the longest form
	tt.Frame()
	clickFrame(t, tt, "添加")
	if v.nf.err == "" || !tt.HasText(v.nf.err) {
		t.Fatalf("error %q not shown: %q", v.nf.err, tt.Texts())
	}
	for _, s := range []string{v.nf.err, "取消", "添加"} {
		r, ok := tt.Find(s)
		if !ok || r.Y < dialogMargin || r.Y+r.H > ht-dialogMargin {
			t.Fatalf("%q at %+v, outside the %d DIP window", s, r, ht)
		}
	}
	clickFrame(t, tt, "取消")
	if v.nf.open || v.nf.err != "" {
		t.Fatalf("open %v error %q after Cancel", v.nf.open, v.nf.err)
	}
}

// A group that cannot be added tells why in the dialog, as every dialog does, not in a toast; and an
// error, in any dialog, moves nothing: the dialog keeps its height and place.
func TestDialogErrorMovesNothing(t *testing.T) {
	h := apptest.New(t)
	v := New(h.A)
	tt := ui.NewTester(v.Build, 1180, 760)
	cancel := func() ui.Rect {
		t.Helper()
		r, ok := tt.Find("取消")
		if !ok {
			t.Fatalf("no Cancel: %q", tt.Texts())
		}
		return r
	}

	v.openGroupDialog("")
	v.gf.kind = 1 // a subscription needs its address
	tt.Frame()
	before := cancel()
	clickFrame(t, tt, "添加并更新")
	if !v.gf.open || v.gf.err == "" || !tt.HasText(v.gf.err) {
		t.Fatalf("open %v error %q texts %q", v.gf.open, v.gf.err, tt.Texts())
	}
	if after := cancel(); after != before {
		t.Fatalf("the group dialog's error moved Cancel from %+v to %+v", before, after)
	}
	clickFrame(t, tt, "取消")

	v.openAddNode()
	v.nf.mode = 1
	tt.Frame()
	before = cancel()
	clickFrame(t, tt, "添加")
	if v.nf.err == "" || !tt.HasText(v.nf.err) {
		t.Fatalf("error %q texts %q", v.nf.err, tt.Texts())
	}
	if after := cancel(); after != before {
		t.Fatalf("the node dialog's error moved Cancel from %+v to %+v", before, after)
	}
}
