//go:build integration

package xray

import (
	"context"
	"net/http"
	"net/http/httptest"
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
