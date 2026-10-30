//go:build integration

package app

import (
	"fmt"
	"net"
	"os"
	"testing"

	"raydock/internal/xray"
)

func TestIntegrationBadPortLeavesHealthyConnection(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("RAYDOCK_CORE_DIR required")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.State.Settings.Port, _ = freePort()
	a.State.Settings.SystemProxy = false
	a.ImportLinks("socks://127.0.0.1:9#good", "")
	a.ImportLinks(`{"protocol":"socks","settings":{"address":"127.0.0.1","port":10},"streamSettings":{"network":"no-such-transport"}}`, "")
	good, bad := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	a.Connect(good, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	if a.Conn != StateOn {
		t.Fatal(a.ConnErr)
	}
	old, since := a.running.core(), a.Since
	port, _ := freePort()
	if err := a.SetNodePort(bad, port); err != nil {
		t.Fatal(err)
	}
	waitRegression(t, posts, func() bool { return a.Conn == StateOn && a.PortErrs[bad] != "" })
	if a.running.core() != old || !old.core.Running() || a.Since != since || a.ConnErr != "" {
		t.Fatal("unrelated bad port touched the healthy connection")
	}
	if a.ownPorts[port] {
		t.Fatal("rejected port reservation retained")
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	if !a.portTaken(port) {
		l.Close()
		t.Fatal("foreign listener mistaken for rejected reservation")
	}
	l.Close()
	// Editing the rejected node invalidates the cached failure; the same assigned port can recover.
	n := *a.NodeByID(bad)
	n.Outbound = ""
	n.Network = "tcp"
	if err := a.EditNode(n); err != nil {
		t.Fatal(err)
	}
	waitRegression(t, posts, func() bool { return a.Conn == StateOn && a.PortErrs[bad] == "" && a.running.core() != old })
	if !a.ownPorts[port] || a.portTaken(port) {
		t.Fatal("live owned listener not recognized")
	}
}
