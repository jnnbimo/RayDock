package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"raydock/internal/node"
)

// decodeNode builds the backend core config connecting to n and decodes it for path lookups; n's outbound
// is the first.
func decodeNode(t *testing.T, n node.Node) map[string]any {
	t.Helper()
	b := testBackend
	b.Chain = []*node.Node{&n}
	data, err := BuildBackend(DefaultOptions(), b)
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSON(t, data)
}

// testNode is the node testBackend connects to.
var testNode = node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Security: "tls"}

// testBackend connects to testNode and takes connections on 23457 (local port) and 23458 (TUN).
var testBackend = Backend{Chain: []*node.Node{&testNode}, Mixed: 23457, TUN: 23458}

// decodeMain builds the backend core config for o and decodes it for path lookups.
func decodeMain(t *testing.T, o Options) map[string]any {
	t.Helper()
	data, err := BuildBackend(o, testBackend)
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSON(t, data)
}

// decodeFront builds the front core config for o, handing over to testBackend, and decodes it.
func decodeFront(t *testing.T, o Options) map[string]any {
	t.Helper()
	data, err := BuildFront(o, Front{Backend: testBackend, Gen: 1, APIPort: 23459})
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSON(t, data)
}

func rulesIn(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, r := range at(m, "routing.rules").([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func decodeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// at looks up a path like "a.b.0.c".
func at(v any, path string) any {
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			i := 0
			for _, ch := range k {
				i = i*10 + int(ch-'0')
			}
			if i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func TestBuildVLESSReality(t *testing.T) {
	m := decodeNode(t, node.Node{Protocol: node.VLESS, Host: "a.example.com", Port: 443, UUID: "u", Flow: "xtls-rprx-vision",
		Security: "reality", SNI: "www.apple.com", PublicKey: "pk", ShortID: "ab"})
	for path, want := range map[string]any{
		"outbounds.0.tag":                                        tagProxy,
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
	if _, err := BuildBackend(DefaultOptions(), Backend{Chain: []*node.Node{{Protocol: node.VLESS, Host: "h", Port: 1, UUID: "u", Security: "reality"}}, Mixed: 23457}); err == nil {
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
	if at(m, "outbounds.0.tag") != tagProxy || at(m, "outbounds.0.streamSettings.network") != "masque" {
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

func rulesOf(t *testing.T, o Options) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range at(decodeMain(t, o), "routing.rules").([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

// findRule returns the index and outbound of the first rule whose key contains value, or -1.
func findRule(rules []map[string]any, key, value string) (int, string) {
	for i, r := range rules {
		match := false
		switch v := r[key].(type) {
		case string:
			match = v == value
		case []any:
			for _, x := range v {
				match = match || x == value
			}
		}
		if match {
			tag, _ := r["outboundTag"].(string)
			return i, tag
		}
	}
	return -1, ""
}

func TestRoutingModes(t *testing.T) {
	for mode, want := range map[string]string{ModeRule: tagProxy, ModeGlobal: tagProxy, ModeDirect: tagDirect} {
		o := DefaultOptions()
		o.Mode = mode
		rules := rulesOf(t, o)
		if tag := rules[len(rules)-1]["outboundTag"]; tag != want {
			t.Errorf("%s mode: last rule goes to %v", mode, tag)
		}
		_, cn := findRule(rules, "ip", "geoip:cn")
		if (mode == ModeRule) != (cn == tagDirect) {
			t.Errorf("%s mode: geoip:cn goes to %q", mode, cn)
		}
		// DNS module queries: domestic DNS direct, remote DNS proxied (direct in direct mode).
		if _, tag := findRule(rules, "inboundTag", tagDNS); tag != tagDirectDNS {
			t.Errorf("%s mode: domestic DNS goes to %q", mode, tag)
		}
		remote := tagProxy
		if mode == ModeDirect {
			remote = tagDirectDNS
		}
		if _, tag := findRule(rules, "inboundTag", tagRemoteDNS); tag != remote {
			t.Errorf("%s mode: remote DNS goes to %q", mode, tag)
		}
	}
}

func TestCustomRules(t *testing.T) {
	o := DefaultOptions()
	o.ProxyRules = "# 注释\nopenai.com\ngeosite:netflix\n"
	o.DirectRules = "10.0.0.0/8\nprocess:steam.exe\nWeChat.exe"
	o.BlockRules = "keyword:tracker"
	o.BlockAds = true
	rules := rulesOf(t, o)
	iProxy, tag := findRule(rules, "domain", "domain:openai.com")
	if tag != tagProxy {
		t.Errorf("openai.com goes to %q", tag)
	}
	if _, tag := findRule(rules, "domain", "geosite:netflix"); tag != tagProxy {
		t.Errorf("geosite:netflix goes to %q", tag)
	}
	if _, tag := findRule(rules, "ip", "10.0.0.0/8"); tag != tagDirect {
		t.Errorf("10.0.0.0/8 goes to %q", tag)
	}
	// Only the front core sees processes: it hands WeChat.exe's connections over as the direct user, which
	// the backend core sends direct where the process rule stands.
	iUser, tag := findRule(rules, "user", tagDirect)
	if tag != tagDirect || iUser < iProxy {
		t.Errorf("direct processes go to %q at %d", tag, iUser)
	}
	if i, _ := findRule(rules, "process", "WeChat.exe"); i >= 0 {
		t.Error("the backend core cannot match processes")
	}
	front := rulesIn(decodeFront(t, o))
	if i, tag := findRule(front, "process", "WeChat.exe"); tag != "f1/mixed-in-direct" || i != 0 {
		t.Errorf("front: WeChat.exe goes to %q at %d", tag, i)
	}
	if _, tag := findRule(front, "inboundTag", "mixed-in-10818"); tag != "f1/mixed-in-direct" {
		t.Errorf("front: the first rule of the local port goes to %q", tag)
	}
	if last := front[len(front)-1]; last["outboundTag"] != "f1/mixed-in-plain" || last["process"] != nil {
		t.Errorf("front: the rest goes by %v", last)
	}
	iBlock, tag := findRule(rules, "domain", "keyword:tracker")
	if tag != tagBlock || iBlock > iProxy {
		t.Errorf("block rules should precede proxy rules: %d %d %q", iBlock, iProxy, tag)
	}
	// Custom rules precede geosite:cn.
	if iCN, _ := findRule(rules, "domain", "geosite:cn"); iCN < iProxy {
		t.Error("custom proxy rules should precede geosite:cn")
	}
	if _, tag := findRule(rules, "domain", "geosite:category-ads-all"); tag != tagBlock {
		t.Errorf("ad rule goes to %q", tag)
	}
	// Direct domains also resolve through the domestic DNS.
	o.DirectRules = "corp.example"
	m := decodeMain(t, o)
	if !contains(at(m, "dns.servers.0.domains"), "domain:corp.example") {
		t.Errorf("domestic DNS domains %v", at(m, "dns.servers.0.domains"))
	}
	o.ProxyRules = "bad rule"
	if _, err := BuildBackend(o, testBackend); err == nil || !strings.Contains(err.Error(), "第 1 行") {
		t.Errorf("a bad rule should report its line: %v", err)
	}
}

func contains(v any, want string) bool {
	list, _ := v.([]any)
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

func TestParseRules(t *testing.T) {
	rs, errs := ParseRules("Example.COM\n.cn.example\nfull:a.b\nregexp:^x$\n1.1.1.1\nip:2001:db8::/32\ngeoip:US\nprocess:C:\\Apps\\x.exe\n\n  # c")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if strings.Join(rs.Domains, " ") != "domain:example.com domain:cn.example full:a.b regexp:^x$" ||
		strings.Join(rs.IPs, " ") != "1.1.1.1 2001:db8::/32 geoip:us" || rs.Processes[0] != `C:\Apps\x.exe` {
		t.Errorf("%+v", rs)
	}
	_, errs = ParseRules("ok.com\n1.2.3.4/40\nfoo:bar\nregexp:(\na b")
	if len(errs) != 4 || errs[0].Line != 2 || errs[3].Line != 5 {
		t.Errorf("%v", errs)
	}
}

func TestDNS(t *testing.T) {
	o := DefaultOptions()
	o.DirectDNS = "https://dns.alidns.com/dns-query"
	o.RemoteDNS = "1.1.1.1:5353"
	o.Hosts = "router.lan 192.168.1.1\n10.0.0.2 nas.lan nas"
	m := decodeMain(t, o)
	for path, want := range map[string]any{
		"dns.tag":                 tagDNS,
		"dns.queryStrategy":       "UseIPv4",
		"dns.servers.0.address":   "https://dns.alidns.com/dns-query",
		"dns.servers.0.tag":       tagDNS,
		"dns.servers.1.tag":       tagRemoteDNS,
		"dns.servers.0.domains.0": "geosite:cn",
		"dns.servers.1.address":   "1.1.1.1",
		"dns.servers.1.port":      float64(5353),
		"dns.hosts.nas.0":         "10.0.0.2",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	hosts := at(m, "dns.hosts").(map[string]any)
	if !contains(hosts["router.lan"], "192.168.1.1") || !contains(hosts["dns.alidns.com"], "223.5.5.5") {
		t.Errorf("hosts %v", hosts)
	}
	// DoH follows the resolver role, even if its host/IP is shared with remote DNS.
	rules := rulesOf(t, o)
	if _, tag := findRule(rules, "inboundTag", tagDNS); tag != tagDirectDNS {
		t.Errorf("DoH domain goes to %q", tag)
	}
	for _, bad := range []string{"udp://1.1.1.1", "dns.google", "1.1.1.1:0", "https://"} {
		if ValidateDNS(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
	for _, ok := range []string{"223.5.5.5", "[2400:3200::1]:53", "tcp+local://8.8.8.8", "localhost", "https+local://doh.pub/dns-query"} {
		if err := ValidateDNS(ok); err != nil {
			t.Errorf("%q：%v", ok, err)
		}
	}
}

func TestTUN(t *testing.T) {
	o := DefaultOptions()
	m, front := decodeMain(t, o), decodeFront(t, o)
	if len(at(m, "inbounds").([]any)) != 1 || len(at(front, "inbounds").([]any)) != 1 || at(m, "fakedns") != nil {
		t.Fatal("no TUN inbound or FakeDNS expected without TUN")
	}
	o.TUN.Enabled, o.FakeDNS = true, true
	m, front = decodeMain(t, o), decodeFront(t, o)
	s := "inbounds.1.settings."
	// The front core holds the TUN; it hands its connections to the backend core's second relay inbound,
	// which sniffs them.
	for path, want := range map[string]any{
		"inbounds.1.protocol":          "tun",
		"inbounds.1.sniffing":          nil,
		s + "mtu":                      float64(1500),
		s + "gateway.0":                tunGateway4,
		s + "dns.0":                    tunDNS4,
		s + "autoSystemRoutingTable.0": "0.0.0.0/0",
		s + "autoSystemRoutingTable.1": nil,
		s + "autoOutboundsInterface":   "auto",
	} {
		if got := at(front, path); got != want {
			t.Errorf("front %s = %v, want %v", path, got, want)
		}
	}
	for path, want := range map[string]any{
		"inbounds.1.tag":                     tagTUN,
		"inbounds.1.protocol":                "vless",
		"inbounds.1.port":                    float64(23458),
		"inbounds.1.sniffing.destOverride.3": "fakedns",
		"fakedns.0.ipPool":                   fakePool4,
		"fakedns.1":                          nil, // no IPv6 pool with IPv4 only
		"dns.servers.0":                      "fakedns",
		"outbounds.3.protocol":               "dns",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	rules := rulesOf(t, o)
	if i, tag := findRule(rules, "outboundTag", tagDNSOut); i != 0 || tag != tagDNSOut || rules[0]["port"] != "53" {
		t.Errorf("first rule should send TUN DNS queries to the DNS module: %v", rules[0])
	}
	frontRules := rulesIn(front)
	if r := frontRules[0]; r["port"] != "53" || r["outboundTag"] != "f1/tun-in-plain" {
		t.Errorf("the front core should hand TUN DNS queries over first: %v", r)
	}
	// Only the user's own process: rules may make the core look up the process of each connection.
	for _, r := range append(rules, frontRules...) {
		if _, ok := r["process"]; ok {
			t.Errorf("built-in process rule: %v", r)
		}
	}
	if _, tag := findRule(frontRules, "port", "443"); tag != "f1/block" {
		t.Errorf("QUIC goes to %q", tag)
	}
	o.TUN.IPv6, o.DNSStrategy, o.TUN.BlockQUIC = true, "UseIP", false
	m, front = decodeMain(t, o), decodeFront(t, o)
	if at(front, s+"autoSystemRoutingTable.1") != "::/0" || at(m, "fakedns.1.ipPool") != fakePool6 {
		t.Errorf("IPv6：%v %v", at(front, s+"autoSystemRoutingTable"), at(m, "fakedns"))
	}
	if i, _ := findRule(rulesIn(front), "port", "443"); i >= 0 {
		t.Error("QUIC should not be blocked when off")
	}
	// Under TUN the backend core's own connections leave by the physical interface.
	b := testBackend
	b.Local = &Local{Interface: "WLAN", DNS: []string{"192.168.1.1"}}
	data, err := BuildBackend(o, b)
	if err != nil {
		t.Fatal(err)
	}
	if got := at(decodeJSON(t, data), "outbounds.1.streamSettings.sockopt.interface"); got != "WLAN" {
		t.Errorf("direct outbound bound to %v", got)
	}
}

func TestOptions(t *testing.T) {
	o := DefaultOptions()
	o.AllowLAN, o.LogLevel = true, "error"
	m := decodeMain(t, o)
	front := decodeFront(t, o)
	if at(front, "inbounds.0.listen") != "0.0.0.0" || at(front, "inbounds.0.tag") != "mixed-in-10818-lan" || at(m, "log.loglevel") != "warning" {
		t.Errorf("listen %v loglevel %v", at(front, "inbounds.0.listen"), at(m, "log.loglevel"))
	}
	// The backend core takes the front core's connections on loopback only.
	if at(m, "inbounds.0.listen") != "127.0.0.1" || at(m, "inbounds.0.port") != float64(23457) || at(m, "inbounds.0.protocol") != "vless" {
		t.Errorf("relay inbound %v", at(m, "inbounds.0"))
	}
	if at(m, "inbounds.0.settings.clients.0.id") != relayID || at(m, "inbounds.0.settings.decryption") != "none" {
		t.Errorf("relay inbound %v", at(m, "inbounds.0"))
	}
	// The backend core reaches the node itself: its outbound is the proxy, the default.
	if at(m, "outbounds.0.tag") != tagProxy || at(m, "outbounds.0.protocol") != "trojan" ||
		at(m, "outbounds.0.settings.servers.0.address") != "t.example.com" {
		t.Errorf("proxy outbound %v", at(m, "outbounds.0"))
	}
	o.Port = 0
	if _, err := BuildBackend(o, testBackend); err == nil {
		t.Error("port 0 should fail")
	}
	if _, err := BuildFront(o, Front{Backend: testBackend, APIPort: 1}); err == nil {
		t.Error("port 0 should fail in front too")
	}
	o.Port = 10818
	if _, err := BuildBackend(o, Backend{Chain: testBackend.Chain}); err == nil {
		t.Error("no relay port should fail")
	}
	if _, err := BuildBackend(o, Backend{Chain: []*node.Node{}, Mixed: 23457}); err == nil {
		t.Error("an empty chain should fail")
	}
}

func TestPickRelease(t *testing.T) {
	list := `[
		{"tag_name":"v27.0.0","draft":true,"assets":[{"name":"X.zip","browser_download_url":"d"},{"name":"X.zip.dgst","browser_download_url":"d"}]},
		{"tag_name":"v26.10.11","prerelease":true,"assets":[{"name":"Other.zip","browser_download_url":"o"}]},
		{"tag_name":"v26.10.10","prerelease":true,"assets":[{"name":"X.zip","browser_download_url":"z"},{"name":"X.zip.dgst","browser_download_url":"g"}]},
		{"tag_name":"v26.3.27","assets":[{"name":"X.zip","browser_download_url":"old"},{"name":"X.zip.dgst","browser_download_url":"old"}]}
	]`
	rel, err := pickRelease(strings.NewReader(list), "X.zip")
	if err != nil || rel != (Release{Tag: "v26.10.10", ZipURL: "z", DgstURL: "g"}) {
		t.Fatalf("%+v %v", rel, err)
	}
	if _, err := pickRelease(strings.NewReader(`[]`), "X.zip"); err == nil {
		t.Error("no usable release should fail")
	}
}

func TestParseDigest(t *testing.T) {
	d, err := parseDigest(strings.NewReader("MD5= 3a\nSHA1= 42\nSHA2-256= d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad\nSHA2-512= 5b\n"))
	if err != nil || d != "d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad" {
		t.Fatalf("%q %v", d, err)
	}
}

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

// Domains no rule names are resolved and matched against IP rules by default; an unknown saved value
// falls back to the default.
func TestDefaultDomainStrategy(t *testing.T) {
	for _, ds := range []string{DefaultOptions().DomainStrategy, "", "bogus"} {
		o := DefaultOptions()
		o.DomainStrategy = ds
		m := decodeMain(t, o)
		if got := at(m, "routing.domainStrategy"); got != "IPIfNonMatch" {
			t.Errorf("%q: %v", ds, got)
		}
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
		{node.Node{Protocol: node.VLESS, Host: "192.168.1.2", Port: 80, UUID: "u"}, true},
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
