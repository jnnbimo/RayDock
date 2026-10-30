//go:build windows && integration

package xray

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"raydock/internal/node"
)

func TestIntegrationForeignListenerCannotJoin(t *testing.T) {
	exe, dir := coreForTest(t), t.TempDir()
	o := DefaultOptions()
	o.Port = freePort(t)
	o.APIPort = freePort(t)
	o.APIToken = "private-old"
	dedicated := freePort(t)
	r := Run{Chain: []*node.Node{&testNode}, Ports: []PortRoute{{dedicated, []*node.Node{&testNode}}}}
	old := startCore(t, exe, dir, "exclusive", mustBuild(t, o, r), nil)
	lc := net.ListenConfig{Control: func(n, a string, c syscall.RawConn) error {
		var e error
		err := c.Control(func(fd uintptr) {
			e = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return e
	}}
	check := func(port int) {
		t.Helper()
		address := fmt.Sprintf("127.0.0.1:%d", port)
		if l, err := lc.Listen(context.Background(), "tcp4", address); err == nil {
			l.Close()
			t.Fatalf("foreign TCP listener joined %s", address)
		}
	}
	check(o.Port)
	check(dedicated)
	check(o.APIPort)
	o.APIPort = freePort(t)
	o.APIToken = "private-new"
	r.Deferred = true
	startCore(t, exe, dir, "candidate", mustBuild(t, o, r), nil)
	check(o.Port)
	check(dedicated)
	old.Stop()
	r.Deferred = false
	client := StatsClient(o.APIToken)
	defer client.CloseIdleConnections()
	if err := ActivateListeners(context.Background(), client, o.APIPort, mustBuild(t, o, r)); err != nil {
		t.Fatal(err)
	}
	check(o.Port)
	check(dedicated)
	check(o.APIPort)
	// Xray's SOCKS inbound listens on TCP only. UDP_ASSOCIATE allocates a distinct ephemeral UDP
	// socket; the end-to-end suite verifies that data path after activation, not a nonexistent UDP
	// listener on the configured public TCP port.
}
