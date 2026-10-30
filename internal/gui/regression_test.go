package gui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
	"raydock/internal/node"
)

func TestMissingProxyRequiresExplicitChoice(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	t.Cleanup(a.Shutdown)
	a.ImportLinks(apptest.LinkA, "")
	home := a.State.Groups[0].ID
	a.GroupByID(home).Front = "missing"
	v := nodesView(a)
	v.openGroupDialog(home)
	v.gf.name = "renamed"
	tt := ui.NewTester(v.Build, 1180, 760)
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.GroupByID(home).Front != "missing" || !v.gf.open {
		t.Fatal("editing a name silently removed a required missing proxy")
	}
	v.gf.front = "" // no proxy
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.GroupByID(home).Front != "" || v.gf.open {
		t.Fatal("explicit no-proxy choice was not saved")
	}
}

func TestImportDuplicateGroupNames(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	t.Cleanup(a.Shutdown)
	first, err := a.AddGroup(app.GroupInput{Name: "same"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.AddGroup(app.GroupInput{Name: "same"})
	if err != nil {
		t.Fatal(err)
	}
	v := nodesView(a)
	v.group = second
	v.openAddNode()
	v.nf.links = apptest.LinkA
	tt := ui.NewTester(v.Build, 1180, 760)
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.State.Nodes) != 1 || a.State.Nodes[0].GroupID != second {
		t.Fatalf("selected second group %s, imported into first %s: %+v", second, first, a.State.Nodes)
	}
}

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

func TestPowerKeyboardActivation(t *testing.T) {
	for _, key := range []ui.Key{ui.KeyEnter, ui.KeySpace} {
		t.Run(map[ui.Key]string{ui.KeyEnter: "Enter", ui.KeySpace: "Space"}[key], func(t *testing.T) {
			h := apptest.New(t)
			a := h.A
			t.Cleanup(a.Shutdown)
			a.ImportLinks(apptest.LinkA, "")
			a.State.SelectedID = a.State.Nodes[0].ID
			h.Core.Err = errors.New("test offline")
			tt := ui.NewTester(nodesView(a).Build, 1180, 760)
			reached := false
			for range 20 {
				tt.Key(0, ui.KeyTab)
				tt.Frame()
				if tt.Focused("连接开关") {
					reached = true
					break
				}
			}
			if !reached {
				t.Fatal("connection power control is not reachable by Tab")
			}
			tt.Key(0, key)
			tt.Frame()
			if a.Conn != app.StateConnecting {
				t.Fatalf("keyboard did not activate power: conn=%v", a.Conn)
			}
			h.Wait(t, func() bool { return !a.CoreBusy })
			tt.Frame()
			if a.Conn != app.StateOff || a.ConnErr == "" {
				t.Fatal("keyboard connection did not report the download error")
			}
		})
	}
}

// The group dialog keeps the proxies chosen by node: a subscription update while it is open, which gives
// the chosen proxy's label to another node, does not make saving choose that node.
func TestGroupProxyKeptByNode(t *testing.T) {
	gate := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "socks://127.0.0.1:9#same\nsocks://127.0.0.1:10#same")
			return
		}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "socks://127.0.0.1:10#same")
	}))
	defer server.Close()
	h := apptest.New(t)
	a := h.A
	t.Cleanup(a.Shutdown)
	a.ImportLinks("socks://127.0.0.1:11#home", "")
	home := a.State.Groups[0].ID
	sub, err := a.AddGroup(app.GroupInput{Name: "subscription", Subscription: true, URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	h.Wait(t, func() bool { return !a.SubBusy[sub] })
	if len(a.State.Nodes) != 3 {
		t.Fatal("the subscription was not imported")
	}
	chosen, other := a.State.Nodes[1].ID, a.State.Nodes[2].ID
	if err := a.EditGroup(home, app.GroupInput{Name: "home", Front: chosen}); err != nil {
		t.Fatal(err)
	}
	a.UpdateSubscription(sub)
	v := nodesView(a)
	v.openGroupDialog(home)
	tt := ui.NewTester(v.Build, 1180, 760)
	close(gate)
	h.Wait(t, func() bool { return !a.SubBusy[sub] })
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if front := a.GroupByID(home).Front; front == other || front != chosen || !v.gf.open || v.gf.err == "" {
		t.Fatalf("front %q (other %v), dialog open %v, error %q", front, front == other, v.gf.open, v.gf.err)
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
