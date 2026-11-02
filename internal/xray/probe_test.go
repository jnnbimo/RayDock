package xray

import (
	"encoding/json"
	"testing"

	"raydock/internal/node"
)

func TestBuildProbe(t *testing.T) {
	front := node.Node{Protocol: node.SOCKS, Host: "front.example.com", Port: 1080}
	n := node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Security: "tls"}
	single := node.Node{Protocol: node.Hysteria2, Host: "h.example.com", Port: 443, Password: "p"}
	data, err := BuildProbe([]PortRoute{{Port: 23456, Chain: []*node.Node{&front, &n}}, {Port: 23457, Chain: []*node.Node{&single}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{
		"inbounds.0.listen": "127.0.0.1",
		"inbounds.0.port":   float64(23456),
		"inbounds.1.port":   float64(23457),
		"outbounds.1.tag":   "port-23456-" + tagProxy,
		"outbounds.1.streamSettings.sockopt.dialerProxy": "port-23456-chain-0",
		"outbounds.2.tag":              "port-23457-" + tagProxy,
		"routing.rules.0.inboundTag.0": "port-23456",
		"routing.rules.0.outboundTag":  "port-23456-" + tagProxy,
		"routing.rules.1.outboundTag":  "port-23457-" + tagProxy,
		"log.loglevel":                 "warning",
		"dns":                          nil,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}

// Tests reach the network through the local interface and resolve with its DNS servers.
func TestBuildProbeLocal(t *testing.T) {
	front := node.Node{Protocol: node.SOCKS, Host: "front.example.com", Port: 1080}
	n := node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Security: "tls"}
	local := &Local{Interface: "WLAN", DNS: []string{"192.168.3.1"}, Strategy: "UseIPv4"}
	single := node.Node{Protocol: node.Hysteria2, Host: "h.example.com", Port: 443, Password: "p"}
	data, err := BuildProbe([]PortRoute{{Port: 23456, Chain: []*node.Node{&front, &n}}, {Port: 23457, Chain: []*node.Node{&single}}}, local)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{
		// Only the first node dials out; the rest go through it.
		"outbounds.0.streamSettings.sockopt.interface":      "WLAN",
		"outbounds.0.streamSettings.sockopt.domainStrategy": "UseIPv4",
		"outbounds.1.streamSettings.sockopt.interface":      nil,
		"outbounds.1.streamSettings.sockopt.dialerProxy":    "port-23456-chain-0",
		// Each route's first node dials out.
		"outbounds.2.streamSettings.sockopt.interface": "WLAN",
		"outbounds.3.tag": tagDirect,
		"outbounds.3.streamSettings.sockopt.interface": "WLAN",
		"dns.servers.0":                "192.168.3.1",
		"dns.tag":                      tagDNS,
		"routing.rules.0.inboundTag.0": tagDNS,
		"routing.rules.0.outboundTag":  tagDirect,
		"routing.rules.1.outboundTag":  "port-23456-" + tagProxy,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if _, err := BuildProbe([]PortRoute{{Port: 1, Chain: []*node.Node{&n}}}, &Local{Interface: "WLAN"}); err == nil {
		t.Error("a local network without DNS servers should fail")
	}
}

func TestBuildResolver(t *testing.T) {
	data, err := BuildResolver(23457, Local{Interface: "WLAN", DNS: []string{"192.168.3.1", "fd00::1"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{
		"inbounds.0.protocol":                          "dokodemo-door",
		"inbounds.0.listen":                            "127.0.0.1",
		"inbounds.0.port":                              float64(23457),
		"outbounds.0.protocol":                         "dns",
		"outbounds.1.streamSettings.sockopt.interface": "WLAN",
		"dns.servers.1":                                "fd00::1",
		"dns.queryStrategy":                            "UseIPv4",
		"routing.rules.0.outboundTag":                  tagDirect,
		"routing.rules.1.outboundTag":                  tagDNSOut,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}
