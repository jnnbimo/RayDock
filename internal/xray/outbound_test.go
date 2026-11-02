package xray

import (
	"strings"
	"testing"

	"raydock/internal/node"
)

// decodeNode builds the config of a core connecting to n and decodes it for path lookups; n's outbound
// is the first.
func decodeNode(t *testing.T, n node.Node) map[string]any {
	t.Helper()
	return decodeJSON(t, mustBuild(t, DefaultOptions(), Run{Chain: []*node.Node{&n}}))
}

func TestBuildVLESSReality(t *testing.T) {
	m := decodeNode(t, node.Node{Protocol: node.VLESS, Host: "a.example.com", Port: 443, UUID: "u", Flow: "xtls-rprx-vision",
		Security: "reality", SNI: "www.apple.com", PublicKey: "pk", ShortID: "ab"})
	for path, want := range map[string]any{
		"outbounds.0.protocol":                                   "vless",
		"outbounds.0.settings.vnext.0.users.0.flow":              "xtls-rprx-vision",
		"outbounds.0.settings.vnext.0.users.0.encryption":        "none",
		"outbounds.0.streamSettings.security":                    "reality",
		"outbounds.0.streamSettings.realitySettings.publicKey":   "pk",
		"outbounds.0.streamSettings.realitySettings.serverName":  "www.apple.com",
		"outbounds.0.streamSettings.realitySettings.fingerprint": "chrome",
		"outbounds.0.streamSettings.realitySettings.spiderX":     "/",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if _, err := Build(DefaultOptions(), Run{Chain: []*node.Node{{Protocol: node.VLESS, Host: "h", Port: 1, UUID: "u", Security: "reality"}}}); err == nil {
		t.Error("REALITY without a public key should fail")
	}
}

func TestBuildTLSWebSocketSNI(t *testing.T) {
	n := node.Node{Protocol: node.VMess, Host: "1.2.3.4", Port: 443, UUID: "u", Network: "ws", Path: "/p",
		HostHeader: "cdn.example.com", Security: "tls", ALPN: "h2, http/1.1"}
	m := decodeNode(t, n)
	s := "outbounds.0.streamSettings."
	for path, want := range map[string]any{
		s + "tlsSettings.serverName":                    "cdn.example.com", // Host header when no SNI
		s + "tlsSettings.fingerprint":                   "chrome",
		s + "tlsSettings.alpn.1":                        "http/1.1",
		s + "wsSettings.host":                           "cdn.example.com",
		s + "wsSettings.path":                           "/p",
		"outbounds.0.settings.vnext.0.users.0.security": "auto",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if len(Warnings(&n)) != 0 {
		t.Error("plain TLS node should have no warnings")
	}
	// Xray 26 removed allowInsecure and refuses configs with it: it is dropped with a warning.
	n.Insecure = true
	m = decodeNode(t, n)
	if at(m, s+"tlsSettings.allowInsecure") != nil || len(Warnings(&n)) != 1 {
		t.Errorf("allowInsecure %v, warnings %v", at(m, s+"tlsSettings.allowInsecure"), Warnings(&n))
	}
	n.PinSHA256 = "AB"
	if len(Warnings(&n)) != 0 {
		t.Error("no warning expected with a pinned certificate")
	}
}

func TestBuildHysteria2AndShadowsocks(t *testing.T) {
	m := decodeNode(t, node.Node{Protocol: node.Hysteria2, Host: "h.example.com", Port: 8443, Password: "pw", PinSHA256: "AA"})
	for path, want := range map[string]any{
		"outbounds.0.protocol":                                        "hysteria",
		"outbounds.0.settings.version":                                float64(2),
		"outbounds.0.streamSettings.network":                          "hysteria",
		"outbounds.0.streamSettings.hysteriaSettings.auth":            "pw",
		"outbounds.0.streamSettings.tlsSettings.serverName":           "h.example.com",
		"outbounds.0.streamSettings.tlsSettings.pinnedPeerCertSha256": "AA",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if at(m, "outbounds.0.streamSettings.tlsSettings.fingerprint") != nil {
		t.Error("QUIC should not set a uTLS fingerprint")
	}
	m = decodeNode(t, node.Node{Protocol: node.Shadowsocks, Host: "s", Port: 1, Cipher: "aes-128-gcm", Password: "x"})
	if at(m, "outbounds.0.settings.servers.0.method") != "aes-128-gcm" || at(m, "outbounds.0.streamSettings.network") != "tcp" {
		t.Errorf("shadowsocks outbound %v", at(m, "outbounds.0"))
	}

	// salamander and port hopping: udphop must be the last layer.
	m = decodeNode(t, node.Node{Protocol: node.Hysteria2, Host: "h", Port: 443, Password: "pw", Obfs: "salamander", ObfsPassword: "ob",
		HopPorts: "443,20000-30000", HopInterval: 45})
	fm := "outbounds.0.streamSettings.finalmask.udp."
	for path, want := range map[string]any{
		fm + "0.type":                 "salamander",
		fm + "0.settings.password":    "ob",
		fm + "1.type":                 "udphop",
		fm + "1.settings.remotePorts": "443,20000-30000",
		fm + "1.settings.interval":    float64(45),
		fm + "1.settings.mode":        "intervalLocal,intervalRemote",
		"outbounds.0.settings.port":   float64(443),
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}

func TestBuildMoreProtocols(t *testing.T) {
	m := decodeNode(t, node.Node{Protocol: node.SOCKS, Host: "s", Port: 1080, Username: "u", Password: "p"})
	if at(m, "outbounds.0.protocol") != "socks" || at(m, "outbounds.0.settings.user") != "u" || at(m, "outbounds.0.settings.pass") != "p" {
		t.Errorf("socks %v", at(m, "outbounds.0"))
	}
	m = decodeNode(t, node.Node{Protocol: node.HTTP, Host: "h", Port: 443, Security: "tls"})
	if at(m, "outbounds.0.protocol") != "http" || at(m, "outbounds.0.settings.user") != nil ||
		at(m, "outbounds.0.streamSettings.tlsSettings.serverName") != "h" {
		t.Errorf("https %v", at(m, "outbounds.0"))
	}

	wg := node.Node{Protocol: node.WireGuard, Host: "162.159.192.1", Port: 2408, SecretKey: "sk", PublicKey: "pk",
		PreSharedKey: "psk", LocalAddress: "172.16.0.2/32, 2606:4700::2/128", Reserved: "1,2,3", MTU: 1280}
	m = decodeNode(t, wg)
	for path, want := range map[string]any{
		"outbounds.0.protocol":                   "wireguard",
		"outbounds.0.settings.secretKey":         "sk",
		"outbounds.0.settings.peers.0.publicKey": "pk",
		"outbounds.0.settings.peers.0.endpoint":  "162.159.192.1:2408",
		"outbounds.0.settings.address.1":         "2606:4700::2/128",
		"outbounds.0.settings.reserved.2":        float64(3),
		"outbounds.0.settings.mtu":               float64(1280),
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if at(m, "outbounds.0.streamSettings") != nil {
		t.Error("WireGuard does not use streamSettings")
	}

	// Nodes imported from JSON use the outbound verbatim with a new tag.
	raw := node.Node{Protocol: node.MASQUE, Host: "m", Port: 443,
		Outbound: `{"protocol":"masque","settings":{"address":"m","port":443},"streamSettings":{"network":"masque","security":"tls"}}`}
	m = decodeNode(t, raw)
	if at(m, "outbounds.0.tag") != proxyTag([]*node.Node{&raw}, nil) || at(m, "outbounds.0.streamSettings.network") != "masque" {
		t.Errorf("masque %v", at(m, "outbounds.0"))
	}
}

func TestBuildStreamParams(t *testing.T) {
	base := node.Node{Protocol: node.VLESS, Host: "k", Port: 9000, UUID: "u"}

	// Legacy mKCP: encryption first, header last (closest to the wire).
	n := base
	n.Network, n.HeaderType, n.Seed = "kcp", "wechat-video", "s"
	m := decodeNode(t, n)
	fm := "outbounds.0.streamSettings.finalmask.udp."
	for path, want := range map[string]any{
		"outbounds.0.streamSettings.network": "kcp",
		fm + "0.type":                        "mkcp-legacy",
		fm + "0.settings.value":              "s",
		fm + "1.settings.header":             "wechat",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	n.HeaderType, n.Seed = "", ""
	m = decodeNode(t, n)
	if at(m, fm+"0.type") != "mkcp-legacy" || at(m, fm+"0.settings") != nil || at(m, fm+"1") != nil {
		t.Errorf("mKCP without seed should use the default obfuscation: %v", at(m, "outbounds.0.streamSettings.finalmask"))
	}
	// A node's own fm is used verbatim.
	n.FinalMask = `{"udp":[{"type":"header-dtls"}]}`
	m = decodeNode(t, n)
	if at(m, fm+"0.type") != "header-dtls" || at(m, fm+"1") != nil {
		t.Errorf("fm %v", at(m, "outbounds.0.streamSettings.finalmask"))
	}

	n = base
	n.Network, n.Mode, n.Extra, n.Security = "xhttp", "stream-up", `{"xPaddingBytes":"100-1000"}`, "tls"
	n.ECHConfig, n.PinSHA256, n.VerifyName = "AEn+", "AB", "real.example.com"
	m = decodeNode(t, n)
	s := "outbounds.0.streamSettings."
	for path, want := range map[string]any{
		s + "xhttpSettings.mode":                "stream-up",
		s + "xhttpSettings.extra.xPaddingBytes": "100-1000",
		s + "tlsSettings.echConfigList":         "AEn+",
		s + "tlsSettings.pinnedPeerCertSha256":  "AB",
		s + "tlsSettings.verifyPeerCertByName":  "real.example.com",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}

	// A certificate pinned on first use leaves a name to verify alone: Xray accepts a pinned leaf as it
	// is, which would skip the name's verification (a pin saved before Needed knew).
	n.PinSHA256, n.AutoPin, n.Insecure = "", "CD", true
	if m = decodeNode(t, n); at(m, s+"tlsSettings.pinnedPeerCertSha256") != nil || at(m, s+"tlsSettings.verifyPeerCertByName") != "real.example.com" {
		t.Errorf("auto pin beside a name: %v", at(m, s+"tlsSettings"))
	}
	n.VerifyName = ""
	if m = decodeNode(t, n); at(m, s+"tlsSettings.pinnedPeerCertSha256") != "CD" {
		t.Errorf("auto pin: %v", at(m, s+"tlsSettings"))
	}

	n = base
	n.Network, n.Path, n.Authority, n.Security, n.PublicKey, n.MLDSA65 = "grpc", "svc", "a.example.com", "reality", "pk", "PQ"
	m = decodeNode(t, n)
	if at(m, s+"grpcSettings.authority") != "a.example.com" || at(m, s+"realitySettings.mldsa65Verify") != "PQ" {
		t.Errorf("grpc reality %v", at(m, "outbounds.0.streamSettings"))
	}
}

// Xray 26 refuses to start with VLESS (unencrypted) or Trojan in plain text to a public address; such a
// node is refused before it reaches a core, where it would fail every other node in the same config.
func TestPlaintextToPublicAddress(t *testing.T) {
	for _, c := range []struct {
		n  node.Node
		ok bool
	}{
		{node.Node{Protocol: node.VLESS, Host: "203.198.1.1", Port: 80, UUID: "u", Network: "ws"}, false},
		{node.Node{Protocol: node.VLESS, Host: "support.zoom.us", Port: 80, UUID: "u", Network: "ws"}, false},
		{node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p"}, false},
		// As Xray 26.10.29 ("xray run -test"): shared, benchmarking and documentation ranges are public.
		{node.Node{Protocol: node.VLESS, Host: "100.64.1.1", Port: 80, UUID: "u"}, false},
		{node.Node{Protocol: node.VLESS, Host: "198.18.0.1", Port: 80, UUID: "u"}, false},
		{node.Node{Protocol: node.Trojan, Host: "203.0.113.5", Port: 80, Password: "p"}, false},
		{node.Node{Protocol: node.VLESS, Host: "192.168.1.2", Port: 80, UUID: "u"}, true},
		{node.Node{Protocol: node.VLESS, Host: "169.254.1.1", Port: 80, UUID: "u"}, true},
		{node.Node{Protocol: node.VLESS, Host: "127.0.0.2", Port: 80, UUID: "u"}, true},
		{node.Node{Protocol: node.VLESS, Host: "[fd00::1]", Port: 80, UUID: "u"}, true},
		{node.Node{Protocol: node.VLESS, Host: "::ffff:10.0.0.1", Port: 80, UUID: "u"}, true},
		{node.Node{Protocol: node.VLESS, Host: "router.lan", Port: 80, UUID: "u"}, true},
		{node.Node{Protocol: node.Trojan, Host: "nas", Port: 443, Password: "p"}, true},
		{node.Node{Protocol: node.VLESS, Host: "v.example.com", Port: 443, UUID: "u", Security: "tls"}, true},
		{node.Node{Protocol: node.VLESS, Host: "v.example.com", Port: 443, UUID: "u", Security: "reality", PublicKey: "k"}, true},
		{node.Node{Protocol: node.VLESS, Host: "v.example.com", Port: 443, UUID: "u", Encryption: "mlkem768x25519plus.native.0rtt.key"}, true},
		{node.Node{Protocol: node.VMess, Host: "v.example.com", Port: 80, UUID: "u"}, true},
	} {
		err := Check(&c.n)
		if (err == nil) != c.ok {
			t.Errorf("%s %s security %q: %v", c.n.Protocol, c.n.Host, c.n.Security, err)
		}
	}
}

// An HTTPS proxy is reached over HTTP/1.1 unless the node names its ALPN.
func TestHTTPSProxyALPN(t *testing.T) {
	n := node.Node{Protocol: node.HTTP, Host: "1.2.3.4", Port: 443, Network: "tcp", Security: "tls"}
	if got := at(decodeNode(t, n), "outbounds.0.streamSettings.tlsSettings.alpn.0"); got != "http/1.1" {
		t.Errorf("default ALPN %v", got)
	}
	n.ALPN = "h2"
	if got := at(decodeNode(t, n), "outbounds.0.streamSettings.tlsSettings.alpn.0"); got != "h2" {
		t.Errorf("ALPN given %v", got)
	}
	v := node.Node{Protocol: node.VLESS, Host: "v.example.com", Port: 443, UUID: "u", Network: "ws", Security: "tls"}
	if got := at(decodeNode(t, v), "outbounds.0.streamSettings.tlsSettings.alpn"); got != nil {
		t.Errorf("other protocols keep Xray's default, got %v", got)
	}
}

func TestStructuredTLSReachesOutbound(t *testing.T) {
	ns, errs := node.ParseClash("proxies: [{type: http, server: localhost, port: 443, tls: true, alpn: [h2], client-fingerprint: firefox}]")
	if len(errs) != 0 || len(ns) != 1 {
		t.Fatal(errs)
	}
	cfg := decodeNode(t, ns[0])
	if at(cfg, "outbounds.0.streamSettings.tlsSettings.alpn.0") != "h2" || at(cfg, "outbounds.0.streamSettings.tlsSettings.fingerprint") != "firefox" {
		t.Fatalf("Clash TLS did not reach core: %v", cfg)
	}
	pin := strings.Repeat("ab", 32)
	n, err := node.Normalize(node.Node{Protocol: node.VMess, Host: "localhost", Port: 443, UUID: "u", Network: "grpc", Security: "tls", PinSHA256: pin, Authority: "service.test"})
	if err != nil {
		t.Fatal(err)
	}
	cfg = decodeNode(t, n)
	if at(cfg, "outbounds.0.streamSettings.tlsSettings.pinnedPeerCertSha256") != pin || at(cfg, "outbounds.0.streamSettings.grpcSettings.authority") != "service.test" {
		t.Fatalf("VMess TLS did not reach core: %v", cfg)
	}
}
