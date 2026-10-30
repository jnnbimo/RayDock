//go:build windows && integration

package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"raydock/internal/localnet"
	"raydock/internal/node"
)

// TestIntegrationTUNHandover changes system routes and requires elevation AND explicit opt-in. Its
// adapters and addresses differ from RayDock's so an existing installation is not stopped or overwritten.
// No node credentials or external test server are used: synthetic destinations go to a local responder.
// Run only when temporary interception of this machine's traffic is acceptable.
func TestIntegrationTUNHandover(t *testing.T) {
	if os.Getenv("RAYDOCK_TUN_TEST") != "1" {
		t.Skip("set RAYDOCK_TUN_TEST=1 to allow temporary system route changes")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("administrator rights required")
	}
	exe, dir := coreForTest(t), t.TempDir()
	iface, err := localnet.Find()
	if err != nil {
		t.Fatal(err)
	}
	local := &Local{Interface: iface.Name}
	for _, server := range iface.DNS {
		local.DNS = append(local.DNS, server.Addr().String())
	}
	type plan struct {
		full, boot []byte
		api        int
		token      string
	}
	public := freePort(t)
	cfg := func(adapter int, marker string, ipv6 bool) plan {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Write([]byte(marker))
				c.Close()
			}
		}()
		o := DefaultOptions()
		o.Port, o.Mode = public, ModeDirect
		o.APIPort, o.APIToken = freePort(t), "tun-control-"+marker
		o.TUN.Enabled, o.TUN.IPv6 = true, ipv6
		data, err := Build(o, Run{Chain: []*node.Node{{Protocol: node.SOCKS, Host: "127.0.0.1", Port: 9}}, Local: local, Adapter: adapter})
		if err != nil {
			t.Fatal(err)
		}
		m := decodeJSON(t, data)
		settings := at(m, "inbounds.1.settings").(map[string]any)
		// Do not collide with an installed RayDock's adapter or addresses.
		settings["name"] = fmt.Sprintf("RDHandoverTest%d", adapter)
		settings["gateway"] = []string{fmt.Sprintf("172.29.239.%d/30", adapter*4+1)}
		settings["dns"] = []string{fmt.Sprintf("172.29.239.%d", adapter*4+2)}
		if ipv6 {
			settings["gateway"] = append(settings["gateway"].([]string), fmt.Sprintf("fdfe:ab98:7654::%d/126", adapter*4+1))
		}
		direct := outboundsByTag(m)[tagDirect].(map[string]any)
		direct["settings"].(map[string]any)["redirect"] = l.Addr().String()
		// The synthetic sink is loopback, not the physical Internet the production direct outbound binds to.
		delete(direct, "streamSettings")
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		ins := m["inbounds"].([]any)
		var kept []any
		for _, in := range ins {
			if in.(map[string]any)["protocol"] != "socks" {
				kept = append(kept, in)
			}
		}
		m["inbounds"] = kept
		boot, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return plan{full: b, boot: boot, api: o.APIPort, token: o.APIToken}
	}
	first, second := cfg(0, "A", true), cfg(1, "B", true)
	if err := WaitTUNReleased(context.Background(), "RDHandoverTest0"); err != nil {
		t.Fatal(err)
	}
	cur := startCore(t, exe, dir, "first", first.full, nil)

	// Observe routing continuously through socket source selection. UDP connect does not send packets:
	// even probes without a reply reveal a fall-through to a physical/foreign adapter. WFP may refuse a
	// connect, particularly DNS during the overlap; that is a blocked packet, not a leak.
	stop, done := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var leaked []string
	var routed int
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, target := range []string{"198.51.100.1:18080", "198.51.100.1:53", "[2001:db8::1]:18080", "[2001:db8::1]:53"} {
				c, err := net.DialTimeout("udp", target, 100*time.Millisecond)
				if err != nil {
					continue
				}
				ip := c.LocalAddr().(*net.UDPAddr).IP.String()
				c.Close()
				mu.Lock()
				if ip != "172.29.239.1" && ip != "172.29.239.5" && ip != "fdfe:ab98:7654::1" && ip != "fdfe:ab98:7654::5" {
					if len(leaked) < 10 {
						leaked = append(leaked, target+" via "+ip)
					}
				}
				routed++
				mu.Unlock()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	finish := sync.OnceFunc(func() { close(stop); <-done })
	defer finish()
	get := func(network, dest, want string) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); ; {
			c, err := net.DialTimeout(network, dest, 300*time.Millisecond)
			if err == nil {
				c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				b := make([]byte, 1)
				_, err = c.Read(b)
				c.Close()
				if err == nil && string(b) == want {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s through new core: reply %q not received, last error %v", network, want, err)
			}
		}
	}
	checkIPv6 := true
	t.Run("IPv6Baseline", func(t *testing.T) {
		c, err := net.DialTimeout("tcp6", "[2001:db8::1]:18080", time.Second)
		if errors.Is(err, windows.WSAEACCES) {
			checkIPv6 = false
			t.Skip("another active VPN/firewall denies IPv6; IPv6 payload handover cannot be verified in this environment")
		}
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		get("tcp6", "[2001:db8::1]:18080", "A")
	})
	for i := range 4 {
		data, want := second, "B"
		if i%2 == 1 {
			data, want = first, "A"
		}
		start := time.Now()
		if err := WaitTUNReleased(context.Background(), fmt.Sprintf("RDHandoverTest%d", (i+1)%2)); err != nil {
			t.Fatal(err)
		}
		next := startCore(t, exe, dir, fmt.Sprintf("next%d", i), data.boot, nil)
		if !cur.Running() || !next.Running() {
			t.Fatal("both cores must be running before the handover")
		}
		// A core's ready line, not merely process creation, gates retirement.
		cur.Stop()
		if cur.Running() || !next.Running() {
			t.Fatal("only the new core must remain after the handover")
		}
		client := StatsClient(data.token)
		err := ActivateListeners(context.Background(), client, data.api, data.full)
		client.CloseIdleConnections()
		if err != nil {
			t.Fatal(err)
		}
		cur = next
		get("tcp4", "198.51.100.1:18080", want)
		if checkIPv6 {
			get("tcp6", "[2001:db8::1]:18080", want)
		}
		t.Logf("handover %d: %v", i, time.Since(start).Round(time.Millisecond))
		time.Sleep(100 * time.Millisecond)
	}
	finish()
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) > 0 || routed < 20 {
		t.Fatalf("route observations %d, outside-TUN source addresses: %v", routed, leaked)
	}
	t.Logf("%d IPv4/IPv6 and DNS route observations: no outside-TUN source", routed)

	// The single-adapter alternative is tested too: opening it before the current session ends must fail,
	// not silently steal the current core's packet reader. It must leave the current core usable.
	m := decodeJSON(t, first.boot)
	var tunOnly []any
	for _, in := range m["inbounds"].([]any) {
		if in.(map[string]any)["protocol"] == "tun" {
			tunOnly = append(tunOnly, in)
		}
	}
	m["inbounds"] = tunOnly
	delete(m, "api")
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var other Core
	if err := other.Start(context.Background(), exe, filepath.Join(dir, "same-adapter.json"), data, nil); err == nil {
		other.Stop()
		t.Fatal("the same Wintun adapter unexpectedly accepted two sessions; reevaluate the handover")
	} else if !strings.Contains(err.Error(), "session") && !strings.Contains(err.Error(), "占用") && !strings.Contains(err.Error(), "being used") {
		t.Logf("same-adapter start refused: %v", err)
	}
	get("tcp4", "198.51.100.1:18080", "A")
}
