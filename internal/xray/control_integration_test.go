//go:build integration

package xray

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"raydock/internal/node"
	"raydock/internal/probe"
)

func TestIntegrationExclusiveHandover(t *testing.T) {
	exe, dir := coreForTest(t), t.TempDir()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	o := DefaultOptions()
	o.Mode = ModeDirect
	o.Port = freePort(t)
	o.APIPort = freePort(t)
	o.APIToken = "test-unique-control-token"
	r := Run{Chain: []*node.Node{{Protocol: node.SOCKS, Host: "127.0.0.1", Port: 9}}}
	cfg, err := Build(o, r)
	if err != nil {
		t.Fatal(err)
	}
	old := startCore(t, exe, dir, "exclusive-old", cfg, nil)
	o.APIPort = freePort(t)
	r.Deferred = true
	boot, err := Build(o, r)
	if err != nil {
		t.Fatal(err)
	}
	next := startCore(t, exe, dir, "exclusive-next", boot, nil)
	if !old.Running() || !next.Running() {
		t.Fatal("both must run before retirement")
	}
	client := StatsClient(o.APIToken)
	defer client.CloseIdleConnections()
	if _, err = ReadTraffic(context.Background(), client, o.APIPort); err != nil {
		t.Fatalf("authenticated API: %v", err)
	}
	bad := StatsClient("wrong-token")
	defer bad.CloseIdleConnections()
	if _, err = ReadTraffic(context.Background(), bad, o.APIPort); err == nil {
		t.Fatal("wrong API credentials accepted")
	}
	old.Stop()
	r.Deferred = false
	full, err := Build(o, r)
	if err != nil {
		t.Fatal(err)
	}
	if err = ActivateListeners(context.Background(), client, o.APIPort, full); err != nil {
		t.Fatal(err)
	}
	if _, err = probe.ViaProxy(context.Background(), o.Port, target.URL, time.Second); err != nil {
		t.Fatalf("activated listener: %v", err)
	}
}

func TestIntegrationResumePartiallyActivatedListeners(t *testing.T) {
	exe, dir := coreForTest(t), t.TempDir()
	n := &node.Node{Protocol: node.SOCKS, Host: "127.0.0.1", Port: 9}
	o := DefaultOptions()
	o.Port, o.APIPort, o.APIToken = freePort(t), freePort(t), "test-resume-token"
	p := freePort(t)
	r := Run{Chain: []*node.Node{n}, Ports: []PortRoute{{Port: p, Chain: []*node.Node{n}}}, Deferred: true}
	boot := mustBuild(t, o, r)
	startCore(t, exe, dir, "resume-listeners", boot, nil)
	r.Deferred = false
	full := mustBuild(t, o, r)
	var cfg struct{ Inbounds []json.RawMessage }
	if err := json.Unmarshal(full, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Inbounds = cfg.Inbounds[:1]
	first, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	client := StatsClient(o.APIToken)
	defer client.CloseIdleConnections()
	ctx := context.Background()
	if err := ActivateListeners(ctx, client, o.APIPort, first); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ActivateListeners(ctx, client, o.APIPort, full); err != nil {
			t.Fatal(err)
		}
	}
	tags, err := inboundTags(ctx, client, o.APIPort)
	if err != nil || !tags[tagMixed] || !tags["port-"+strconv.Itoa(p)] {
		t.Fatalf("incomplete resumed listeners: %v %v", tags, err)
	}
}
