package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"raydock/internal/node"
)

// A subscription past the size read is refused, not cut to the nodes that fit: they would replace the
// group's. One of that size exactly is whole.
func TestOversizedSubscription(t *testing.T) {
	const head = "socks://127.0.0.1:1080#first\n"
	body := head + strings.Repeat(" ", maxSubscription-len(head))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/over" {
			w.Write([]byte(body + "\nsocks://127.0.0.1:1081#second\n"))
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	if nodes, _, err := fetchSubscription(context.Background(), srv.Client(), srv.URL+"/over"); err == nil || !strings.Contains(err.Error(), "16 MB") {
		t.Fatalf("oversized: %d nodes, %v", len(nodes), err)
	}
	if nodes, _, err := fetchSubscription(context.Background(), srv.Client(), srv.URL+"/full"); err != nil || len(nodes) != 1 {
		t.Fatalf("at the limit: %d nodes, %v", len(nodes), err)
	}
}

func TestDeletedHopInvalidatesChain(t *testing.T) {
	for _, deleteGroup := range []bool{false, true} {
		t.Run(map[bool]string{false: "node", true: "group"}[deleteGroup], func(t *testing.T) {
			a, _, _ := newTestApp(t)
			a.ImportLinks("trojan://pw@a.example:443#a", "")
			home, id := a.State.Groups[0].ID, a.State.Nodes[0].ID
			relay, err := a.AddGroup(GroupInput{Name: "relay"})
			if err != nil {
				t.Fatal(err)
			}
			a.ImportLinks("socks://b.example:1080#b", relay)
			front := a.State.Nodes[1].ID
			if err := a.EditGroup(home, GroupInput{Name: "home", Front: front}); err != nil {
				t.Fatal(err)
			}
			a.Conn, a.ActiveID = StateOn, id
			if deleteGroup {
				a.DeleteGroup(relay)
			} else {
				a.DeleteNode(front)
			}
			if _, err := a.Chain(a.NodeByID(id)); err == nil {
				t.Fatal("required hop deletion silently bypassed chain")
			}
			if a.Conn != StateOff || a.GroupByID(home).Front != front {
				t.Fatal("invalid connection/reference survived deletion")
			}
		})
	}
}

func TestSubscriptionURLGeneration(t *testing.T) {
	a, posts, _ := newTestApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			close(started)
			<-release
			_, _ = w.Write([]byte("trojan://pw@old.example:443#old"))
		} else {
			_, _ = w.Write([]byte("trojan://pw@new.example:443#new"))
		}
	}))
	defer srv.Close()
	id, err := a.AddGroup(GroupInput{Name: "sub", Subscription: true, URL: srv.URL + "/old"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("old request not started")
	}
	err = a.EditGroup(id, GroupInput{Name: "sub", URL: srv.URL + "/new"})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, posts, func() bool { return !a.SubBusy[id] })
	drainPosts(posts)
	if len(a.State.Nodes) != 1 || a.State.Nodes[0].Host != "new.example" {
		t.Fatalf("old result replaced new URL nodes: %+v", a.State.Nodes)
	}
}

func TestSubscriptionVariantsKeepIDsAndPorts(t *testing.T) {
	a, _, _ := newTestApp(t)
	base := "vless://11111111-2222-3333-4444-555555555555@a.example:443?type=ws&security="
	a.ImportLinks(base+"none#plain\n"+base+"tls#tls", "")
	home := a.State.Groups[0].ID
	plain, tls := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	a.State.Ports = map[string]int{plain: 20001, tls: 20002}
	fresh, errs := node.ParseMany(base + "tls#renamed TLS\n" + base + "none#renamed plain")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	a.replaceGroupNodes(home, fresh)
	if a.NodeByID(plain).Security != "none" || a.NodeByID(tls).Security != "tls" || a.State.Ports[plain] != 20001 || a.State.Ports[tls] != 20002 {
		t.Fatal("reordered variants exchanged their identities/ports")
	}
	// A changed setting of one server retains its identity (the connection takes it: SyncCores).
	a.Conn, a.ActiveID = StateOn, tls
	fresh, _ = node.ParseMany(base + "tls&sni=new.example#renamed TLS\n" + base + "none#renamed plain")
	a.replaceGroupNodes(home, fresh)
	if a.NodeByID(tls) == nil || a.NodeByID(tls).SNI != "new.example" || a.Conn != StateOn {
		t.Fatal("subscription setting change lost identity or the connection")
	}
}
