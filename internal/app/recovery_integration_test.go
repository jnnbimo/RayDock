//go:build integration

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"raydock/internal/node"
	"raydock/internal/xray"
)

func TestIntegrationCoreAndRuleCommitRenewsDedicatedProcess(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("RAYDOCK_CORE_DIR required")
	}
	a, posts, _ := newTestApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.CoreDir(), xray.GeoTagFile), []byte("202501010000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.EnsureCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy })
	if !a.CoreReady() {
		t.Fatal(a.CoreErr)
	}
	a.BundledCore = root
	a.ImportLinks("socks://127.0.0.1:9", "")
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.SetNodePort(a.State.Nodes[0].ID, port); err != nil {
		t.Fatal(err)
	}
	waitFor(t, posts, func() bool { return a.running.core() != nil })
	before := a.running.core()
	if !before.core.Running() {
		t.Fatal("dedicated core did not start")
	}
	a.seedCore()
	waitFor(t, posts, func() bool { return !a.CoreBusy && a.running.core() != before })
	if a.CoreErr != "" || a.State.GeoTag != xray.ReadGeoTag(root) {
		t.Fatalf("install: core=%s tag=%s", a.CoreErr, a.State.GeoTag)
	}
	if a.running.core() == nil || !a.running.core().core.Running() || before.core.Running() {
		t.Fatal("dedicated process was not replaced")
	}
}

func TestIntegrationBatchProbeIsolatesRejectedHandler(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("RAYDOCK_CORE_DIR required")
	}
	exe := filepath.Join(root, xray.ExeName())
	valid := &node.Node{Protocol: node.SOCKS, Host: "127.0.0.1", Port: 9}
	noPeers := &node.Node{Protocol: node.WireGuard, Outbound: `{"protocol":"wireguard","settings":{"secretKey":"1111111111111111111111111111111111111111111111111111111111111111","noKernelTun":true}}`}
	routes := []xray.PortRoute{{Port: 23101, Chain: []*node.Node{valid}}, {Port: 23102, Chain: []*node.Node{noPeers}}}
	var rejected []int
	got, err := isolateProbeRoutes(context.Background(), exe, routes, nil, func(port int, _ error) { rejected = append(rejected, port) })
	if err != nil || len(got) != 1 || got[0].Port != 23101 || len(rejected) != 1 || rejected[0] != 23102 {
		t.Fatalf("accepted=%v rejected=%v err=%v", got, rejected, err)
	}
}
