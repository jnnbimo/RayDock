package gui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
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
