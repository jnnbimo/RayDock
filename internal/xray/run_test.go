package xray

import (
	"fmt"
	"strings"
	"testing"

	"raydock/internal/node"
)

func mustBuild(t *testing.T, o Options, r Run) []byte {
	t.Helper()
	data, err := Build(o, r)
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

// at returns the value at a dotted path of a decoded config, list indexes included; nil when absent.
func at(v any, path string) any {
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			var i int
			if _, err := fmt.Sscan(k, &i); err != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

// The core reaches the node through the chain, dialing each node through the one before it; the last is
// the proxy, the default outbound, which routing uses.
func TestBuildChain(t *testing.T) {
	front := node.Node{Protocol: node.SOCKS, Host: "front.example.com", Port: 1080}
	mid := node.Node{Protocol: node.Trojan, Host: "mid.example.com", Port: 443, Password: "p", Security: "tls"}
	back := node.Node{Protocol: node.WireGuard, Host: "162.159.192.1", Port: 2408, SecretKey: "k", PublicKey: "pk"}
	imported := node.Node{Protocol: node.MASQUE, Host: "m.example.com", Port: 443,
		Outbound: `{"protocol":"masque","tag":"mine","streamSettings":{"network":"masque","sockopt":{"mark":1}}}`}
	chain := []*node.Node{&front, &mid, &back, &imported}
	m := decodeJSON(t, mustBuild(t, DefaultOptions(), Run{Chain: chain}))
	// The chain's tags start with what its config hashes to.
	p := strings.TrimSuffix(proxyTag(chain, nil), tagProxy)
	if !strings.HasPrefix(p, nodePrefix) || len(p) != len(nodePrefix)+9 {
		t.Fatalf("prefix %q", p)
	}
	for path, want := range map[string]any{
		"outbounds.0.tag": p + tagProxy,
		"outbounds.0.streamSettings.sockopt.dialerProxy": p + "chain-2",
		"outbounds.0.streamSettings.sockopt.mark":        float64(1),
		"outbounds.1.tag": p + "chain-0",
		"outbounds.1.streamSettings.sockopt.dialerProxy": nil,
		"outbounds.2.tag": p + "chain-1",
		"outbounds.2.streamSettings.sockopt.dialerProxy": p + "chain-0",
		"outbounds.2.streamSettings.security":            "tls",
		"outbounds.3.tag":                                p + "chain-2",
		"outbounds.3.protocol":                           "wireguard",
		"outbounds.3.streamSettings.sockopt.dialerProxy": p + "chain-1",
		"outbounds.4.tag":                                tagDirect,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	// A broken node names itself in the error.
	bad := node.Node{Name: "坏节点", Protocol: node.VLESS, Host: "h", Port: 1, UUID: "u", Security: "reality"}
	if _, err := Build(DefaultOptions(), Run{Chain: []*node.Node{&front, &bad}}); err == nil || !strings.Contains(err.Error(), "坏节点") {
		t.Errorf("error %v", err)
	}
}

// The dedicated ports: an inbound each, whose connections go through the port's chain before any other
// rule; a chain the same as another port's, or as the connection's, shares its outbounds. Without a
// connection the core serves them alone, dropping whatever no rule takes.
func TestDedicatedPorts(t *testing.T) {
	front := node.Node{Protocol: node.SOCKS, Host: "front.example.com", Port: 1080}
	a := node.Node{Protocol: node.Trojan, Host: "a.example.com", Port: 443, Password: "p", Security: "tls"}
	r := Run{Ports: []PortRoute{{20001, []*node.Node{&front, &a}}, {20002, []*node.Node{&a}}, {20003, []*node.Node{&a}}}}
	o := DefaultOptions()
	o.LogLevel = "info"
	m := decodeJSON(t, mustBuild(t, o, r))
	tags := outboundsByTag(m)
	p1, p2 := proxyTag(r.Ports[0].Chain, nil), proxyTag(r.Ports[1].Chain, nil)
	if at(tags[p1], "streamSettings.sockopt.dialerProxy") != strings.TrimSuffix(p1, tagProxy)+"chain-0" || tags[p2] == nil {
		t.Errorf("port chains %v", at(m, "outbounds"))
	}
	count := func(m map[string]any, tag string) (n int) {
		for _, out := range at(m, "outbounds").([]any) {
			if tagOf(out) == tag {
				n++
			}
		}
		return n
	}
	if n := count(m, p2); n != 1 {
		t.Errorf("the same chain is in %d outbounds", n)
	}
	for path, want := range map[string]any{
		"inbounds.0.tag":               "port-20001",
		"inbounds.0.listen":            "127.0.0.1",
		"inbounds.0.port":              float64(20001),
		"inbounds.0.protocol":          "socks",
		"inbounds.2.tag":               "port-20003",
		"inbounds.3":                   nil,
		"routing.rules.0.inboundTag.0": "port-20001",
		"routing.rules.0.outboundTag":  p1,
		"routing.rules.1.outboundTag":  p2,
		"routing.rules.2.outboundTag":  p2,
		"routing.rules.3":              nil,
		"outbounds.0.tag":              tagBlock,
		"log.loglevel":                 "info",
		"dns":                          nil,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	// Listening on every address when the LAN may use them.
	o.AllowLAN = true
	if m = decodeJSON(t, mustBuild(t, o, r)); at(m, "inbounds.0.listen") != "0.0.0.0" {
		t.Errorf("listen %v", at(m, "inbounds.0.listen"))
	}

	// With a connection: the ports' rules come first, and the connection to a's node shares its outbounds.
	r.Chain = []*node.Node{&a}
	m = decodeJSON(t, mustBuild(t, o, r))
	rules := rulesIn(m)
	if rules[0]["outboundTag"] != p1 || rules[2]["outboundTag"] != p2 || rules[3]["inboundTag"] == "port-20003" {
		t.Errorf("rules %v", rules[:4])
	}
	if at(m, "outbounds.0.tag") != p2 || count(m, p2) != 1 || at(m, "inbounds.0.tag") != tagMixed || at(m, "inbounds.3.listen") != "0.0.0.0" {
		t.Errorf("outbounds %v inbounds %v", at(m, "outbounds"), at(m, "inbounds"))
	}

	bad := node.Node{Protocol: node.VLESS, Host: "h", Port: 1, UUID: "u", Security: "reality"}
	r = Run{Ports: []PortRoute{{20004, []*node.Node{&bad}}}}
	if _, err := Build(o, r); err == nil || !strings.Contains(err.Error(), "20004") {
		t.Errorf("error %v", err)
	}
	if _, err := Build(o, Run{}); err == nil {
		t.Error("no connection and no ports should fail")
	}
}

// Under TUN the core reaches the first node of each chain, the connection's and the dedicated ports',
// through the physical interface, its address resolved by the local DNS servers, as the system would
// without the TUN; later hops go through the one before.
func TestTUNLocal(t *testing.T) {
	front := node.Node{Protocol: node.VLESS, Host: "Front.example.com", Port: 443, UUID: "u", Security: "tls"}
	a := node.Node{Protocol: node.SOCKS, Host: "a.example.com", Port: 1080}
	ip := node.Node{Protocol: node.Trojan, Host: "203.0.113.9", Port: 443, Password: "p", Security: "tls"}
	o := DefaultOptions()
	o.TUN.Enabled = true
	local := &Local{Interface: "WLAN", DNS: []string{"192.168.1.1", "8.8.8.8"}, Strategy: "UseIPv4"}
	r := Run{Chain: []*node.Node{&front, &a}, Local: local,
		Ports: []PortRoute{{20001, []*node.Node{&testNode}}, {20002, []*node.Node{&ip}}}}
	m := decodeJSON(t, mustBuild(t, o, r))
	tags := outboundsByTag(m)
	main := proxyTag(r.Chain, local)
	first := strings.TrimSuffix(main, tagProxy) + "chain-0"
	want := map[string]any{first: "WLAN", main: nil, tagDirect: "WLAN", tagDirectDNS: "WLAN"}
	for _, p := range r.Ports {
		want[proxyTag(p.Chain, local)] = "WLAN"
	}
	for tag, want := range want {
		if got := at(tags[tag], "streamSettings.sockopt.interface"); got != want {
			t.Errorf("%s bound to %v, want %v", tag, got, want)
		}
	}
	if at(tags[first], "streamSettings.sockopt.domainStrategy") != "UseIPv4" {
		t.Errorf("the first node's address is resolved by Xray: %v", tags[first])
	}
	// The local server, not the remote DNS one, resolves the first nodes' domains; its queries go direct.
	for path, want := range map[string]any{
		"dns.servers.0.address":      "192.168.1.1",
		"dns.servers.0.domains.0":    "full:front.example.com",
		"dns.servers.0.domains.1":    "full:t.example.com",
		"dns.servers.0.domains.2":    nil,
		"dns.servers.0.skipFallback": true,
		"dns.servers.0.tag":          tagDNS,
		"dns.servers.1.address":      "8.8.8.8",
		"dns.servers.1.tag":          tagDNS,
		"dns.servers.2.address":      "223.5.5.5",
		"dns.servers.3.tag":          tagRemoteDNS,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if _, tag := findRule(rulesIn(m), "inboundTag", tagDNS); tag != tagDirectDNS {
		t.Fatalf("node DNS must go direct: %s", tag)
	}
	// Without a physical resolver, the domestic DNS resolves node addresses.
	r.Local = &Local{Interface: "WLAN"}
	m = decodeJSON(t, mustBuild(t, o, r))
	if at(m, "dns.servers.0.address") != "223.5.5.5" || !contains(at(m, "dns.servers.0.domains"), "full:front.example.com") {
		t.Errorf("servers %v", at(m, "dns.servers"))
	}
}

// Open to the LAN, the local port must not take other devices to this computer's services; the DNS
// module's own queries leave by an outbound without that rule, so a resolver on this computer is still
// asked.
func TestDirectLoopbackWithLAN(t *testing.T) {
	for _, lan := range []bool{false, true} {
		o := DefaultOptions()
		o.AllowLAN = lan
		m := decodeMain(t, o)
		rules, _ := at(outboundsByTag(m)[tagDirect], "settings.finalRules").([]any)
		loopback := at(rules[0], "action") == "block" && at(rules[0], "ip.0") == "127.0.0.0/8" && at(rules[0], "ip.1") == "::1/128"
		if loopback != lan || len(rules) == 0 || at(rules[len(rules)-1], "action") != "allow" || at(rules[len(rules)-1], "ip") != nil {
			t.Fatalf("lan %v: loopback refused %v: %v", lan, loopback, rules)
		}
		if dns := outboundsByTag(m)[tagDirectDNS]; dns == nil || at(dns, "settings.finalRules") != nil {
			t.Fatalf("lan %v: DNS outbound %v", lan, dns)
		}
		if _, tag := findRule(rulesIn(m), "inboundTag", tagDNS); tag != tagDirectDNS {
			t.Fatalf("lan %v: DNS queries go to %q", lan, tag)
		}
	}
}
