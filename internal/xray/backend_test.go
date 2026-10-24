package xray

import (
	"strings"
	"testing"

	"raydock/internal/node"
)

func mustBackend(t *testing.T, o Options, b Backend) []byte {
	t.Helper()
	data, err := BuildBackend(o, b)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustFront(t *testing.T, o Options, f Front) []byte {
	t.Helper()
	data, err := BuildFront(o, f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// outboundsByTag indexes the outbounds of a decoded config.
func outboundsByTag(m map[string]any) map[string]any {
	tags := map[string]any{}
	for _, out := range at(m, "outbounds").([]any) {
		tags[tagOf(out)] = out
	}
	return tags
}

// The backend core reaches the node through the chain, dialing each node through the one before it; the
// last is the proxy, the default outbound, which routing uses.
func TestBuildChain(t *testing.T) {
	front := node.Node{Protocol: node.SOCKS, Host: "front.example.com", Port: 1080}
	mid := node.Node{Protocol: node.Trojan, Host: "mid.example.com", Port: 443, Password: "p", Security: "tls"}
	back := node.Node{Protocol: node.WireGuard, Host: "162.159.192.1", Port: 2408, SecretKey: "k", PublicKey: "pk"}
	imported := node.Node{Protocol: node.MASQUE, Host: "m.example.com", Port: 443,
		Outbound: `{"protocol":"masque","tag":"mine","streamSettings":{"network":"masque","sockopt":{"mark":1}}}`}
	m := decodeJSON(t, mustBackend(t, DefaultOptions(), Backend{Chain: []*node.Node{&front, &mid, &back, &imported}, Mixed: 23457}))
	for path, want := range map[string]any{
		"outbounds.0.tag": tagProxy,
		"outbounds.0.streamSettings.sockopt.dialerProxy": "chain-2",
		"outbounds.0.streamSettings.sockopt.mark":        float64(1),
		"outbounds.1.tag": "chain-0",
		"outbounds.1.streamSettings.sockopt.dialerProxy": nil,
		"outbounds.2.tag": "chain-1",
		"outbounds.2.streamSettings.sockopt.dialerProxy": "chain-0",
		"outbounds.2.streamSettings.security":            "tls",
		"outbounds.3.tag":                                "chain-2",
		"outbounds.3.protocol":                           "wireguard",
		"outbounds.3.streamSettings.sockopt.dialerProxy": "chain-1",
		"outbounds.4.tag":                                tagDirect,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	// A broken node names itself in the error.
	bad := node.Node{Name: "坏节点", Protocol: node.VLESS, Host: "h", Port: 1, UUID: "u", Security: "reality"}
	if _, err := BuildBackend(DefaultOptions(), Backend{Chain: []*node.Node{&front, &bad}, Mixed: 23457}); err == nil || !strings.Contains(err.Error(), "坏节点") {
		t.Errorf("error %v", err)
	}
}

// A front core switches to another local port, LAN access, process rules, dedicated ports or backend core
// through its API; the TUN needs a restart. The parts add the new outbounds and inbounds and remove the old.
func TestFrontParts(t *testing.T) {
	o := DefaultOptions()
	f := Front{Backend: testBackend, Gen: 1, APIPort: 23459}
	cur := mustFront(t, o, f)
	next := Front{Backend: Backend{Chain: testBackend.Chain, Mixed: 24001}, Gen: 2, APIPort: 23459}
	o2 := o
	o2.Port, o2.DirectRules = 10819, "steam.exe"
	nextCfg := mustFront(t, o2, next)
	if !CanSwitchFront(cur, nextCfg) {
		t.Fatal("another port, rules and backend switch through the API")
	}
	s, err := FrontParts(cur, nextCfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.RemoveAfter, ",") != "mixed-in-10818" || len(s.RemoveFirst) != 0 || !strings.Contains(string(s.AddInbounds), "10819") ||
		strings.Join(s.OldOutbounds, ",") != "f1/mixed-in-plain" || !strings.Contains(string(s.Outbounds), "24001") ||
		!strings.Contains(string(s.Routing), "steam.exe") {
		t.Errorf("%+v %s %s", s, s.Outbounds, s.Routing)
	}
	// LAN access on the same port: the old inbound goes first.
	o3 := o
	o3.AllowLAN = true
	if s, _ := FrontParts(cur, mustFront(t, o3, f)); strings.Join(s.RemoveFirst, ",") != "mixed-in-10818" || len(s.RemoveAfter) != 0 {
		t.Errorf("LAN: %+v", s)
	}
	// The backend alone: the inbound stays.
	if s, _ := FrontParts(cur, mustFront(t, o, next)); s.AddInbounds != nil || len(s.RemoveFirst)+len(s.RemoveAfter) != 0 {
		t.Errorf("backend: %+v", s)
	}
	// Dedicated ports come and go beside the local port; one listening otherwise is replaced, its port
	// freed first.
	a := node.Node{Protocol: node.SOCKS, Host: "a.example.com", Port: 1080}
	ports := f
	ports.Backend.Ports = []PortRoute{{20001, []*node.Node{&a}}, {20002, []*node.Node{&a}}}
	withPorts := mustFront(t, o, ports)
	if s, _ := FrontParts(cur, withPorts); !CanSwitchFront(cur, withPorts) || !strings.Contains(string(s.AddInbounds), "20002") ||
		strings.Contains(string(s.AddInbounds), "10818") || len(s.RemoveFirst)+len(s.RemoveAfter) != 0 {
		t.Errorf("ports added: %+v %s", s, s.AddInbounds)
	}
	moved := ports
	moved.PortsLAN, moved.Backend.Ports = true, ports.Backend.Ports[1:]
	if s, _ := FrontParts(withPorts, mustFront(t, o, moved)); strings.Join(s.RemoveFirst, ",") != "port-20002" ||
		strings.Join(s.RemoveAfter, ",") != "port-20001" || !strings.Contains(string(s.AddInbounds), "port-20002-lan") {
		t.Errorf("ports moved: %+v %s", s, s.AddInbounds)
	}
	// Disconnected, the dedicated ports stay and the local port goes, through the API.
	alone := ports
	alone.Backend.Chain = nil
	if s, _ := FrontParts(withPorts, mustFront(t, o, alone)); !CanSwitchFront(withPorts, mustFront(t, o, alone)) ||
		strings.Join(s.RemoveAfter, ",") != "mixed-in-10818" || s.AddInbounds != nil {
		t.Errorf("disconnected: %+v", s)
	}
	o4 := o
	o4.TUN.Enabled = true
	f.Backend.TUN = 23458
	if CanSwitchFront(cur, mustFront(t, o4, f)) {
		t.Error("the TUN needs a restart")
	}
	o5 := o4
	o5.TUN.MTU = 1400
	if CanSwitchFront(mustFront(t, o4, f), mustFront(t, o5, f)) {
		t.Error("TUN settings need a restart")
	}
	if CanSwitchFront(mustFront(t, o4, f), mustFront(t, o4, alone)) {
		t.Error("the TUN goes with a restart")
	}
}

// The dedicated ports: the front core takes each on its inbound and hands its connections over as the
// port's user, which the backend core sends through the port's chain before any other rule. Disconnected,
// a backend core serves them alone.
func TestDedicatedPorts(t *testing.T) {
	front := node.Node{Protocol: node.SOCKS, Host: "front.example.com", Port: 1080}
	a := node.Node{Protocol: node.Trojan, Host: "a.example.com", Port: 443, Password: "p", Security: "tls"}
	b := Backend{Chain: testBackend.Chain, Mixed: 23457, Ports: []PortRoute{{20001, []*node.Node{&front, &a}}, {20002, []*node.Node{&a}}}}
	o := DefaultOptions()
	m := decodeJSON(t, mustBackend(t, o, b))
	tags := outboundsByTag(m)
	if at(tags["port-20001-proxy"], "streamSettings.sockopt.dialerProxy") != "port-20001-chain-0" || tags["port-20002-proxy"] == nil {
		t.Errorf("port chains %v", at(m, "outbounds"))
	}
	for path, want := range map[string]any{
		"routing.rules.0.user.0":              "port-20001",
		"routing.rules.0.outboundTag":         "port-20001-proxy",
		"routing.rules.1.outboundTag":         "port-20002-proxy",
		"inbounds.0.settings.clients.4.email": "port-20001",
		"inbounds.0.settings.clients.4.id":    portUserID(20001),
		"inbounds.0.settings.clients.5.email": "port-20002",
		"outbounds.0.tag":                     tagProxy,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	fm := decodeJSON(t, mustFront(t, o, Front{Backend: b, Gen: 1, APIPort: 23459}))
	for path, want := range map[string]any{
		"inbounds.0.tag":               "mixed-in-10818",
		"inbounds.1.tag":               "port-20001",
		"inbounds.1.listen":            "127.0.0.1",
		"inbounds.1.port":              float64(20001),
		"routing.rules.0.inboundTag.0": "port-20001",
		"routing.rules.0.outboundTag":  "f1/mixed-in-port-20001",
	} {
		if got := at(fm, path); got != want {
			t.Errorf("front %s = %v, want %v", path, got, want)
		}
	}
	if id := at(outboundsByTag(fm)["f1/mixed-in-port-20001"], "settings.vnext.0.users.0.id"); id != portUserID(20001) {
		t.Errorf("the port is handed over as %v", id)
	}
	// Disconnected: the ports alone, through a backend core of nothing else.
	b.Chain = nil
	m = decodeJSON(t, mustBackend(t, o, b))
	if at(m, "dns") != nil || at(m, "outbounds.0.tag") != tagBlock || at(m, "routing.rules.2") != nil || outboundsByTag(m)["port-20002-proxy"] == nil {
		t.Errorf("ports alone %v", m)
	}
	fm = decodeJSON(t, mustFront(t, o, Front{Backend: b, PortsLAN: true, Gen: 1, APIPort: 23459}))
	if at(fm, "inbounds.0.tag") != "port-20001-lan" || at(fm, "inbounds.0.listen") != "0.0.0.0" || at(fm, "inbounds.2") != nil {
		t.Errorf("front with the ports alone %v", at(fm, "inbounds"))
	}
	bad := node.Node{Protocol: node.VLESS, Host: "h", Port: 1, UUID: "u", Security: "reality"}
	b.Ports = []PortRoute{{20003, []*node.Node{&bad}}}
	if _, err := BuildBackend(o, b); err == nil || !strings.Contains(err.Error(), "20003") {
		t.Errorf("error %v", err)
	}
}

// A running backend core switches routing rules through its API; anything else needs a new one.
func TestCanSwitch(t *testing.T) {
	o := DefaultOptions()
	cur := mustBackend(t, o, testBackend)
	global := o
	global.Mode = ModeGlobal
	if !CanSwitch(cur, mustBackend(t, global, testBackend)) {
		t.Error("another mode switches")
	}
	other := testNode
	other.Port = 8443
	b := testBackend
	b.Chain = []*node.Node{&other}
	if CanSwitch(cur, mustBackend(t, o, b)) {
		t.Error("another node needs a new backend core")
	}
	dns := o
	dns.RemoteDNS = "1.1.1.1"
	if CanSwitch(cur, mustBackend(t, dns, testBackend)) {
		t.Error("another DNS needs a new backend core")
	}
	routing, err := SwitchParts(mustBackend(t, global, testBackend))
	if err != nil || !strings.Contains(string(routing), `"routing"`) {
		t.Errorf("%s %v", routing, err)
	}
}

// Under TUN the backend core reaches the first node of the connection's and each dedicated port's chain
// through the physical interface, their addresses resolved by the local DNS servers, as the system would
// without the TUN; later hops go through the one before.
func TestBackendLocal(t *testing.T) {
	front := node.Node{Protocol: node.VLESS, Host: "Front.example.com", Port: 443, UUID: "u", Security: "tls"}
	a := node.Node{Protocol: node.SOCKS, Host: "a.example.com", Port: 1080}
	ip := node.Node{Protocol: node.Trojan, Host: "203.0.113.9", Port: 443, Password: "p", Security: "tls"}
	o := DefaultOptions()
	o.TUN.Enabled = true
	b := Backend{Chain: []*node.Node{&front, &a}, Mixed: 23457, TUN: 23458, Ports: []PortRoute{{20001, []*node.Node{&testNode}}, {20002, []*node.Node{&ip}}},
		Local: &Local{Interface: "WLAN", DNS: []string{"192.168.1.1", "8.8.8.8"}, Strategy: "UseIPv4"}}
	m := decodeJSON(t, mustBackend(t, o, b))
	tags := outboundsByTag(m)
	for tag, want := range map[string]any{"chain-0": "WLAN", tagProxy: nil, "port-20001-proxy": "WLAN", "port-20002-proxy": "WLAN", tagDirect: "WLAN"} {
		if got := at(tags[tag], "streamSettings.sockopt.interface"); got != want {
			t.Errorf("%s bound to %v, want %v", tag, got, want)
		}
	}
	if at(tags["chain-0"], "streamSettings.sockopt.domainStrategy") != "UseIPv4" {
		t.Errorf("the first node's address is resolved by Xray: %v", tags["chain-0"])
	}
	// The local server, not the remote DNS one, resolves the nodes' domains; its queries go direct.
	for path, want := range map[string]any{
		"dns.servers.0.address":       "192.168.1.1",
		"dns.servers.0.domains.0":     "full:front.example.com",
		"dns.servers.0.domains.1":     "full:t.example.com",
		"dns.servers.0.domains.2":     nil,
		"dns.servers.0.skipFallback":  true,
		"dns.servers.1.address":       "223.5.5.5",
		"routing.rules.2.ip.0":        "192.168.1.1",
		"routing.rules.2.ip.1":        nil,
		"routing.rules.2.outboundTag": tagDirect,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	// With none left, the domestic DNS resolves them.
	b.Local.DNS = []string{"8.8.8.8"}
	m = decodeJSON(t, mustBackend(t, o, b))
	if at(m, "dns.servers.0.address") != "223.5.5.5" || !contains(at(m, "dns.servers.0.domains"), "full:front.example.com") {
		t.Errorf("servers %v", at(m, "dns.servers"))
	}
}
