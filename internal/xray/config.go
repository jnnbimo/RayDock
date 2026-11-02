// Package xray builds Xray configs, runs the core process and installs the core.
package xray

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"raydock/internal/localnet"
)

const (
	ModeRule   = "rule"   // China direct, the rest proxied
	ModeGlobal = "global" // everything but LAN proxied
	ModeDirect = "direct" // everything direct
)

// Tags let routing rules refer to inbounds, outbounds and the DNS module.
const (
	tagProxy     = "proxy"
	tagDirect    = "direct"
	tagBlock     = "block"
	tagDNSOut    = "dns-out"
	tagDirectDNS = "direct-dns" // the DNS module's own queries that leave directly (see directFinalRules)
	tagMixed     = "mixed-in"
	tagTUN       = "tun-in"
	tagDNS       = "dns-internal" // direct and node-address DNS queries
	tagRemoteDNS = "dns-remote"   // remote DNS queries keep their role even on a shared endpoint
)

// The TUN's system DNS points at a second address in its gateway subnet (tunAddrs); queries entering the
// TUN go to Xray's DNS module.
const (
	fakePool4  = localnet.FakeDNS4
	fakePool6  = localnet.FakeDNS6
	defaultMTU = 1500
)

type TUNOptions struct {
	Enabled   bool `json:"enabled"`
	MTU       int  `json:"mtu"`
	IPv6      bool `json:"ipv6"`      // route IPv6 too
	Strict    bool `json:"strict"`    // block DNS and IPv6 bypassing the TUN with WFP
	BlockQUIC bool `json:"blockQuic"` // block UDP 443 so browsers fall back to TCP, whose domains can be sniffed
}

// Options are the node-independent settings; JSON tags are the field names in the settings file.
type Options struct {
	Port     int    `json:"port"`
	AllowLAN bool   `json:"allowLan"`
	Mode     string `json:"mode"`
	LogLevel string `json:"logLevel"`

	DomainStrategy string `json:"domainStrategy"` // AsIs / IPIfNonMatch / IPOnDemand
	BlockAds       bool   `json:"blockAds"`
	ProxyRules     string `json:"proxyRules"` // one rule per line, see ParseRules
	DirectRules    string `json:"directRules"`
	BlockRules     string `json:"blockRules"`

	DirectDNS   string `json:"directDns"`   // for China domains
	RemoteDNS   string `json:"remoteDns"`   // for other domains, queried through the proxy
	DNSStrategy string `json:"dnsStrategy"` // UseIP / UseIPv4 / UseIPv6
	FakeDNS     bool   `json:"fakeDns"`     // TUN mode only
	Hosts       string `json:"hosts"`       // see ParseHosts

	TUN TUNOptions `json:"tun"`

	// APIPort, when set, opens the core's API on 127.0.0.1: its traffic counts and listener activation
	// (coreAPI).
	APIPort  int    `json:"-"`
	APIToken string `json:"-"` // random per process, required with APIPort: authenticates every API call
}

// DefaultOptions uses a non-conflicting proxy port and IPIfNonMatch routing.
// Unmatched domains may still go direct through geoip:cn.
func DefaultOptions() Options {
	return Options{
		Port: 10818, Mode: ModeRule, LogLevel: "warning", DomainStrategy: "IPIfNonMatch",
		DirectDNS: "223.5.5.5", RemoteDNS: "8.8.8.8", DNSStrategy: "UseIPv4",
		TUN: TUNOptions{MTU: defaultMTU, Strict: true, BlockQUIC: true},
	}
}

// Addresses of common DoH services, so resolving the DoH host needs no lookup.
var dohHosts = map[string][]string{
	"dns.alidns.com":     {"223.5.5.5", "223.6.6.6"},
	"doh.pub":            {"1.12.12.12", "120.53.53.53"},
	"dns.google":         {"8.8.8.8", "8.8.4.4"},
	"cloudflare-dns.com": {"1.1.1.1", "1.0.0.1"},
	"dns.quad9.net":      {"9.9.9.9", "149.112.112.112"},
}

type obj = map[string]any

// dnsHosts maps the common DoH services and the user's static entries.
func dnsHosts(user map[string][]string) obj {
	hosts := obj{}
	for k, v := range dohHosts {
		hosts[k] = v
	}
	for k, v := range user {
		hosts[k] = v
	}
	return hosts
}

func dnsStrategy(o Options) string { return queryStrategy(o.DNSStrategy) }

// queryStrategy returns s if it is a query strategy Xray knows, or else UseIPv4.
func queryStrategy(s string) string {
	switch s {
	case "UseIP", "UseIPv4", "UseIPv6":
		return s
	}
	return "UseIPv4"
}

// coreAPI opens the core's API when o asks for it: traffic counts (StatsService) and the activation of
// the public listeners (HandlerService), behind an inbound requiring the per-process token.
func coreAPI(cfg obj, o Options) {
	if o.APIPort <= 0 {
		return
	}
	// No API listener of Xray's own, which would serve every program on the computer: an HTTP inbound
	// requiring the per-process token routes to the API alone (apiDial).
	cfg["api"] = obj{"tag": "api", "services": []string{"StatsService", "HandlerService"}}
	cfg["inbounds"] = append(cfg["inbounds"].([]any), exclusiveInbound(obj{
		"tag": tagAPIAuth, "protocol": "http", "listen": "127.0.0.1", "port": o.APIPort,
		"settings": obj{"accounts": []any{obj{"user": apiUser, "pass": o.APIToken}}},
	}))
	routes := cfg["routing"].(obj)
	routes["rules"] = append([]any{obj{"type": "field", "inboundTag": []string{tagAPIAuth}, "outboundTag": "api"}}, routes["rules"].([]any)...)
	statsConfig(cfg)
}

func logLevel(l string) string {
	switch l {
	case "debug", "info", "warning":
		return l
	}
	// Readiness detection relies on the warning-level "Xray ... started" line, so quieter levels are not allowed.
	return "warning"
}

// mixedInbound is the local port: SOCKS5, whose inbound in Xray takes HTTP proxy requests too.
func mixedInbound(o Options) obj {
	return exclusiveInbound(obj{"tag": tagMixed, "protocol": "socks", "listen": ListenAddr(o.AllowLAN), "port": o.Port, "settings": obj{"auth": "noauth", "udp": true},
		"sniffing": obj{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}})
}

// Under TUN, direct traffic also binds to the physical interface.
func outbounds(proxies []any, o Options, strategy string, local *Local) []any {
	sockopt := obj{"domainStrategy": strategy}
	if local != nil {
		sockopt["interface"] = local.Interface
	}
	direct := obj{"tag": tagDirect, "protocol": "freedom", "settings": obj{"finalRules": directFinalRules(o)},
		"streamSettings": obj{"sockopt": sockopt}}
	list := append(proxies,
		// Direct traffic uses Xray's DNS: China domains via the domestic DNS, the rest via the remote DNS.
		direct,
		obj{"tag": tagBlock, "protocol": "blackhole", "settings": obj{}},
	)
	if o.TUN.Enabled {
		list = append(list, obj{"tag": tagDNSOut, "protocol": "dns"})
	}
	// The DNS module's direct queries: resolvers on this computer stay reachable with the local port open
	// to the LAN.
	return append(list, obj{"tag": tagDirectDNS, "protocol": "freedom", "streamSettings": obj{"sockopt": sockopt}})
}

// directFinalRules lets direct traffic reach private addresses, which the routing sends direct
// (geoip:private). With the local port open to the LAN, loopback stays refused, so that other devices
// cannot reach the services of this computer through it. The rules match addresses alone, whoever asks:
// the DNS module's queries leave by tagDirectDNS instead, so that resolvers on this computer stay
// reachable.
func directFinalRules(o Options) []any {
	allow := obj{"action": "allow"}
	if o.AllowLAN {
		return []any{obj{"action": "block", "ip": []string{"127.0.0.0/8", "::1/128"}}, allow}
	}
	return []any{allow}
}

type parsedRules struct{ proxy, direct, block RuleSet }

func parseAllRules(o Options) (parsedRules, error) {
	var p parsedRules
	for _, r := range []struct {
		name string
		text string
		dst  *RuleSet
	}{{"代理规则", o.ProxyRules, &p.proxy}, {"直连规则", o.DirectRules, &p.direct}, {"阻止规则", o.BlockRules, &p.block}} {
		rs, errs := ParseRules(r.text)
		if len(errs) > 0 {
			return p, fmt.Errorf("%s%v", r.name, errs[0])
		}
		*r.dst = rs
	}
	return p, nil
}

// routing returns the routing of the connection, proxy the tag of the outbound reaching its node.
func routing(o Options, r parsedRules, proxy string) obj {
	var rules []any
	add := func(tag string, kv ...any) {
		rule := obj{"type": "field", "outboundTag": tag}
		for i := 0; i+1 < len(kv); i += 2 {
			rule[kv[i].(string)] = kv[i+1]
		}
		rules = append(rules, rule)
	}
	addSet := func(tag string, rs RuleSet) {
		if len(rs.Domains) > 0 {
			add(tag, "domain", rs.Domains)
		}
		if len(rs.IPs) > 0 {
			add(tag, "ip", rs.IPs)
		}
		if len(rs.Processes) > 0 {
			add(tag, "process", rs.Processes)
		}
	}

	if o.TUN.Enabled {
		// DNS queries entering the TUN go to the DNS module.
		add(tagDNSOut, "inboundTag", []string{tagTUN}, "port", "53")
		if o.TUN.BlockQUIC {
			add(tagBlock, "inboundTag", []string{tagTUN}, "network", "udp", "port", "443")
		}
		// No built-in rule matches processes: Xray would look up the owner of every connection entering the
		// TUN, and cannot for protected system processes ("Access is denied"). RayDock's own requests
		// never need one: they go through the proxy port while connected (httpClient), and measurements and
		// certificate pinning leave by the physical interface (localnet).
	}

	// Queries of the DNS module: domestic DNS direct, remote DNS proxied (direct in direct mode).
	remoteTag := proxy
	if o.Mode == ModeDirect {
		remoteTag = tagDirectDNS
	}
	add(tagDirectDNS, "inboundTag", []string{tagDNS})
	add(remoteTag, "inboundTag", []string{tagRemoteDNS})

	addSet(tagBlock, r.block)
	if o.BlockAds {
		add(tagBlock, "domain", []string{"geosite:category-ads-all"})
	}
	switch o.Mode {
	case ModeDirect:
		add(tagDirect, "network", "tcp,udp")
	case ModeGlobal:
		addSet(tagDirect, r.direct)
		addSet(proxy, r.proxy)
		add(tagDirect, "ip", []string{"geoip:private"})
		add(tagDirect, "domain", []string{"geosite:private"})
		add(proxy, "network", "tcp,udp")
	default:
		addSet(proxy, r.proxy)
		addSet(tagDirect, r.direct)
		add(tagDirect, "ip", []string{"geoip:private"})
		add(tagDirect, "domain", []string{"geosite:private"})
		// geosite:cn includes some Google domains served in China; send Google to the proxy first.
		add(proxy, "domain", []string{"geosite:google"})
		add(tagDirect, "domain", []string{"geosite:cn"})
		add(tagDirect, "ip", []string{"geoip:cn"})
		add(proxy, "network", "tcp,udp")
	}
	ds := o.DomainStrategy
	switch ds {
	case "AsIs", "IPIfNonMatch", "IPOnDemand":
	default:
		ds = DefaultOptions().DomainStrategy
	}
	return obj{"domainStrategy": ds, "rules": rules}
}

// dnsConfig asks node-address DNS servers before the general resolvers.
func dnsConfig(o Options, direct, remote dnsServer, directDomains []string, user map[string][]string, nodes []any) obj {
	var servers []any
	if o.TUN.Enabled && o.FakeDNS {
		// FakeDNS answers the queries entering the TUN alone: the core's own lookups skip it.
		servers = append(servers, "fakedns")
	}
	servers = append(servers, nodes...)
	d := direct.entry()
	d["domains"], d["skipFallback"], d["tag"] = directDomains, true, tagDNS
	r := remote.entry()
	r["tag"] = tagRemoteDNS
	servers = append(servers, d, r)
	return obj{
		"tag":           tagDNS,
		"hosts":         dnsHosts(user),
		"servers":       servers,
		"queryStrategy": dnsStrategy(o),
	}
}

// dnsServer is a validated DNS endpoint; routing uses its role, not its address.
type dnsServer struct {
	address string // address given to Xray
	port    int
}

func (s dnsServer) entry() obj {
	e := obj{"address": s.address}
	if s.port > 0 {
		e["port"] = s.port
	}
	return e
}

// parseDNS accepts an IP, IP:port, localhost and the https://, https+local://, tcp://, tcp+local://
// and quic+local:// addresses Xray supports.
func parseDNS(s, def string) (dnsServer, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}
	if strings.ContainsAny(s, " \t") {
		return dnsServer{}, errors.New("地址中不能有空格")
	}
	if s == "localhost" {
		return dnsServer{address: s}, nil
	}
	if scheme, _, ok := strings.Cut(s, "://"); ok {
		switch strings.ToLower(scheme) {
		case "https", "https+local", "tcp", "tcp+local", "quic+local", "h2c":
		default:
			return dnsServer{}, fmt.Errorf("不支持的协议 %s", scheme)
		}
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return dnsServer{}, errors.New("地址格式错误")
		}
		return dnsServer{address: s}, nil
	}
	if isIP(s) {
		return dnsServer{address: s}, nil
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil || !isIP(host) {
		return dnsServer{}, errors.New("请填写 IP、IP:端口，或 https:// 等 DNS 地址")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return dnsServer{}, errors.New("端口无效")
	}
	return dnsServer{address: host, port: port}, nil
}

func ValidateDNS(s string) error {
	_, err := parseDNS(s, "x")
	return err
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(strings.Trim(s, "[]"))
	return err == nil
}

func splitList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
