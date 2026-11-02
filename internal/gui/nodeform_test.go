package gui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
	"raydock/internal/node"
)

// The form's drop-downs choose by value from the keyboard: the protocol, then its own fields.
func TestFormChoiceKeyboard(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	t.Cleanup(a.Shutdown)
	v := nodesView(a)
	v.openAddNode()
	v.nf.mode = 1
	tt := ui.NewTester(v.Build, 1180, 760)
	if err := tt.Click("VLESS"); err != nil { // the protocol drop-down
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if v.nf.n.Protocol != node.VMess || !tt.HasText("额外 ID（alterId）") || tt.HasText("流控（flow）") {
		t.Fatalf("protocol %q texts %q", v.nf.n.Protocol, tt.Texts())
	}
}

// Importing nothing keeps the dialog and what was pasted, with why.
func TestFailedImportKeepsInput(t *testing.T) {
	h := apptest.New(t)
	t.Cleanup(h.A.Shutdown)
	v := nodesView(h.A)
	v.openAddNode()
	v.nf.links = "this is not a proxy node"
	tt := ui.NewTester(v.Build, 1180, 760)
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !v.nf.open || v.nf.links != "this is not a proxy node" || !strings.Contains(v.nf.err, "没有可导入的节点") {
		t.Fatalf("open %v links %q error %q", v.nf.open, v.nf.links, v.nf.err)
	}
}

func TestSaveAppliesConnectionChanges(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "node", true: "group"}[group], func(t *testing.T) {
			h := apptest.New(t)
			a := h.A
			t.Cleanup(a.Shutdown)
			a.ImportLinks("socks://127.0.0.1:9#current", "")
			id, home := a.State.Nodes[0].ID, a.State.Groups[0].ID
			a.Conn, a.ActiveID = app.StateOn, id
			v := nodesView(a)
			if group {
				relay, err := a.AddGroup(app.GroupInput{Name: "relay"})
				if err != nil {
					t.Fatal(err)
				}
				a.ImportLinks("socks://127.0.0.1:10#front", relay)
				v.openGroupDialog(home)
				_, ids := v.proxyOptions(home)
				v.gf.front = ids[1]
			} else {
				v.openEditNode(id)
				v.nf.port = 11
			}
			tt := ui.NewTester(v.Build, 1180, 760)
			if err := tt.Click("保存"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if a.SettingsDirty || a.Conn != app.StateOn || v.nf.open || v.gf.open {
				t.Fatal("save left a pending change or the dialog")
			}
			texts := strings.Join(tt.Texts(), "\n")
			if !strings.Contains(texts, "已保存") || strings.Contains(texts, "尚未应用") {
				t.Fatalf("toast: %q", tt.Texts())
			}
			tt.Frame()
			tt.Frame()
			if tt.HasText("设置已修改，应用时会重新连接，已建立的连接将断开") {
				t.Fatalf("reconnect banner: %q", tt.Texts())
			}
		})
	}
}
