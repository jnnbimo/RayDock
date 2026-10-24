package gui

import (
	"github.com/egoist/mygo/ui"
	"raydock/internal/app"
	"raydock/internal/app/apptest"
	"strings"
	"testing"
)

// Saving the connection's node or group applies at once (app.refreshNode): nothing is left pending, so
// neither the toast nor the title bar asks for a reconnect.
func TestSaveAppliesConnectionChanges(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "node", true: "group"}[group], func(t *testing.T) {
			h := apptest.New(t)
			a := h.A
			t.Cleanup(a.Shutdown)
			a.ImportLinks("socks://127.0.0.1:9#current", "")
			id, home := a.State.Nodes[0].ID, a.State.Groups[0].ID
			a.Conn, a.ActiveID = app.StateOn, id
			v := New(a)
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
			if tt.HasText("设置已修改，尚未应用到当前连接") {
				t.Fatalf("reconnect banner: %q", tt.Texts())
			}
		})
	}
}
