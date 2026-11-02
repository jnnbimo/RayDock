package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"raydock/internal/node"
)

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

func TestDNSResolversKeepDistinctRoles(t *testing.T) {
	for _, endpoint := range []string{"1.1.1.1", "1.1.1.1:5353", "https://dns.google/dns-query"} {
		for _, mode := range []string{ModeRule, ModeGlobal, ModeDirect} {
			t.Run(mode+"/"+endpoint, func(t *testing.T) {
				o := DefaultOptions()
				o.DirectDNS, o.RemoteDNS, o.Mode = endpoint, endpoint, mode
				m := decodeMain(t, o)
				if at(m, "dns.servers.0.tag") != tagDNS || at(m, "dns.servers.1.tag") != tagRemoteDNS {
					t.Fatal("resolver roles were lost on the shared endpoint")
				}
				rules := rulesIn(m)
				if _, tag := findRule(rules, "inboundTag", tagDNS); tag != tagDirectDNS {
					t.Fatalf("domestic resolver goes to %q", tag)
				}
				want := testProxy
				if mode == ModeDirect {
					want = tagDirectDNS
				}
				if _, tag := findRule(rules, "inboundTag", tagRemoteDNS); tag != want {
					t.Fatalf("remote resolver goes to %q, want %q", tag, want)
				}
			})
		}
	}
}
