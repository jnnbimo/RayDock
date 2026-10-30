package xray

import (
	"encoding/json"
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

// testNode is the node testRun connects to.
var testNode = node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Security: "tls"}

// testRun connects to testNode.
var testRun = Run{Chain: []*node.Node{&testNode}}

// testLocal is the physical network of the tests under TUN.
var testLocal = &Local{Interface: "WLAN", DNS: []string{"192.168.1.1"}}

// testProxy is the tag of testRun's proxy outbound.
var testProxy = proxyTag(testRun.Chain, nil)

// proxyTag is the tag of the outbound reaching chain's final node, under TUN bound to local.
func proxyTag(chain []*node.Node, local *Local) string {
	_, tag, err := nodeOutbounds(chain, local)
	if err != nil {
		panic(err)
	}
	return tag
}

// decodeMain builds the config of the core serving testRun with o, under TUN bound to testLocal, and
// decodes it for path lookups.
func decodeMain(t *testing.T, o Options) map[string]any {
	t.Helper()
	return decodeJSON(t, mustBuild(t, o, runFor(o)))
}

func runFor(o Options) Run {
	r := testRun
	if o.TUN.Enabled {
		r.Local = testLocal
	}
	return r
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
	for mode, want := range map[string]string{ModeRule: testProxy, ModeGlobal: testProxy, ModeDirect: tagDirect} {
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
		remote := testProxy
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
	if tag != testProxy {
		t.Errorf("openai.com goes to %q", tag)
	}
	if _, tag := findRule(rules, "domain", "geosite:netflix"); tag != testProxy {
		t.Errorf("geosite:netflix goes to %q", tag)
	}
	if _, tag := findRule(rules, "ip", "10.0.0.0/8"); tag != tagDirect {
		t.Errorf("10.0.0.0/8 goes to %q", tag)
	}
	// Process rules stand where their set does: after the proxy rules in rule mode. They match the
	// connections of the local port and of the TUN alike.
	iProc, tag := findRule(rules, "process", "WeChat.exe")
	if tag != tagDirect || iProc < iProxy || !contains(rules[iProc]["process"], "steam.exe") || rules[iProc]["inboundTag"] != nil {
		t.Errorf("direct processes go to %q at %d", tag, iProc)
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
	if _, err := Build(o, testRun); err == nil || !strings.Contains(err.Error(), "第 1 行") {
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

// Under TUN the core holds the TUN: DNS queries entering it go to the DNS module, which FakeDNS answers
// first when on; QUIC entering it is blocked. Its own connections leave by the physical interface.
func TestTUN(t *testing.T) {
	o := DefaultOptions()
	m := decodeMain(t, o)
	if len(at(m, "inbounds").([]any)) != 1 || at(m, "fakedns") != nil {
		t.Fatal("no TUN or FakeDNS expected without TUN")
	}
	if _, err := Build(func() Options { o := o; o.TUN.Enabled = true; return o }(), testRun); err == nil {
		t.Fatal("TUN needs the local network")
	}
	o.TUN.Enabled = true
	m = decodeMain(t, o)
	s := "inbounds.1.settings."
	for path, want := range map[string]any{
		"inbounds.1.tag":                     tagTUN,
		"inbounds.1.protocol":                "tun",
		"inbounds.1.sniffing.destOverride.2": "quic",
		"inbounds.1.sniffing.destOverride.3": nil,
		"inbounds.1.sniffing.metadataOnly":   false,
		s + "name":                           TUNNames[0],
		s + "mtu":                            float64(1500),
		s + "gateway.0":                      "172.19.0.1/30",
		s + "dns.0":                          "172.19.0.2",
		s + "autoSystemRoutingTable.0":       "0.0.0.0/1",
		s + "autoSystemRoutingTable.1":       "128.0.0.0/1",
		s + "autoSystemRoutingTable.2":       nil,
		s + "autoOutboundsInterface":         "auto",
		s + "autoSystemWfpBlockLeak.0":       "dns",
		"fakedns":                            nil,
		"dns.servers.0.address":              "192.168.1.1", // the node's resolver first
		"outbounds.3.protocol":               "dns",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	rules := rulesOf(t, o)
	if i, tag := findRule(rules, "outboundTag", tagDNSOut); i != 0 || tag != tagDNSOut || rules[0]["port"] != "53" || !contains(rules[0]["inboundTag"], tagTUN) {
		t.Errorf("first rule should send TUN DNS queries to the DNS module: %v", rules[0])
	}
	if i, tag := findRule(rules, "port", "443"); i != 1 || tag != tagBlock || rules[i]["network"] != "udp" || !contains(rules[i]["inboundTag"], tagTUN) {
		t.Errorf("QUIC entering the TUN goes to %q at %d", tag, i)
	}
	// Only the user's own process rules: one may make the core look up the process of each connection.
	for _, r := range rules {
		if _, ok := r["process"]; ok {
			t.Errorf("built-in process rule: %v", r)
		}
	}
	direct := outboundsByTag(m)[tagDirect]
	if at(direct, "streamSettings.sockopt.interface") != "WLAN" {
		t.Errorf("direct outbound %v", direct)
	}

	// The other adapter, for the core starting beside this one, has its own name and addresses.
	r := runFor(o)
	r.Adapter = 1
	m = decodeJSON(t, mustBuild(t, o, r))
	for path, want := range map[string]any{
		s + "name":      TUNNames[1],
		s + "gateway.0": "172.19.0.5/30",
		s + "dns.0":     "172.19.0.6",
	} {
		if got := at(m, path); got != want {
			t.Errorf("adapter 1: %s = %v, want %v", path, got, want)
		}
	}

	o.FakeDNS, o.TUN.IPv6, o.DNSStrategy, o.TUN.BlockQUIC = true, true, "UseIP", false
	m = decodeMain(t, o)
	for path, want := range map[string]any{
		"inbounds.1.sniffing.destOverride.3": "fakedns",
		s + "gateway.1":                      "fdfe:dcba:9876::1/126",
		s + "autoSystemRoutingTable.2":       "::/1",
		s + "autoSystemRoutingTable.3":       "8000::/1",
		"fakedns.0.ipPool":                   fakePool4,
		"fakedns.1.ipPool":                   fakePool6,
		"dns.servers.0":                      "fakedns",
		"dns.servers.1.address":              "192.168.1.1",
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if i, _ := findRule(rulesIn(m), "port", "443"); i >= 0 {
		t.Error("QUIC should not be blocked when off")
	}
}

// Public listeners are exclusive: no unrelated SO_REUSEADDR socket may join them.
func TestExclusiveListeners(t *testing.T) {
	o := DefaultOptions()
	r := testRun
	r.Ports = []PortRoute{{20001, []*node.Node{&testNode}}}
	m := decodeJSON(t, mustBuild(t, o, r))
	for _, in := range []string{"inbounds.0", "inbounds.1"} {
		so := at(m, in+".streamSettings.sockopt.customSockopt.0")
		if Overlap != (so != nil) || so != nil && (at(so, "level") != "65535" || at(so, "opt") != "-5" || at(so, "value") != "1" || at(so, "network") != nil) {
			t.Errorf("%s: %v", in, so)
		}
	}
}
func TestOptions(t *testing.T) {
	o := DefaultOptions()
	o.AllowLAN, o.LogLevel = true, "error"
	m := decodeMain(t, o)
	// The local port takes SOCKS5 and HTTP, sniffing domains for the routing alone.
	for path, want := range map[string]any{
		"inbounds.0.tag":                tagMixed,
		"inbounds.0.protocol":           "socks",
		"inbounds.0.listen":             "0.0.0.0",
		"inbounds.0.port":               float64(10818),
		"inbounds.0.sniffing.routeOnly": true,
		"log.loglevel":                  "warning",
		"api":                           nil,
		"stats":                         nil,
	} {
		if got := at(m, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	// The core reaches the node itself: its outbound is the proxy, the default.
	if at(m, "outbounds.0.tag") != testProxy || at(m, "outbounds.0.protocol") != "trojan" ||
		at(m, "outbounds.0.settings.servers.0.address") != "t.example.com" {
		t.Errorf("proxy outbound %v", at(m, "outbounds.0"))
	}
	// Its API is reached only through the authenticated tunnel.
	o.APIPort, o.APIToken = 23459, "token"
	m = decodeMain(t, o)
	if at(m, "api.listen") != nil || at(m, "api.services.0") != "StatsService" || at(m, "api.services.1") != "HandlerService" {
		t.Errorf("api %v", at(m, "api"))
	}
	if in := at(m, "inbounds.1").(map[string]any); in["tag"] != tagAPIAuth || in["listen"] != "127.0.0.1" ||
		at(m, "inbounds.1.settings.accounts.0.pass") != "token" || at(m, "routing.rules.0.inboundTag.0") != tagAPIAuth {
		t.Errorf("api inbound %v", in)
	}
	o.Port = 0
	if _, err := Build(o, testRun); err == nil {
		t.Error("port 0 should fail")
	}
	o.Port = 10818
	if _, err := Build(o, Run{Chain: []*node.Node{}}); err == nil {
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
