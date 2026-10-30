// Package xray builds Xray configs, runs the core process and installs the core.
package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"raydock/internal/localnet"
	"raydock/internal/node"
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

func dnsStrategy(o Options) string {
	switch o.DNSStrategy {
	case "UseIP", "UseIPv4", "UseIPv6":
		return o.DNSStrategy
	}
	return "UseIPv4"
}

// coreAPI exposes statistics alone without a token (measurement/test configs). App processes use a
// per-process authenticated inbound, including HandlerService for activating exclusively-owned ports.
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

// Local binds core and measurement traffic to a physical interface and its DNS.
type Local struct {
	Interface string   // interface name for sockopt.interface
	DNS       []string // server IPs
	Strategy  string   // UseIP / UseIPv4 / UseIPv6
}

// direct adds interface-bound freedom and DNS configuration.
func (l *Local) direct(cfg obj) error {
	if len(l.DNS) == 0 {
		return errors.New("本地网络没有 DNS 服务器")
	}
	cfg["outbounds"] = append(cfg["outbounds"].([]any), obj{
		"tag": tagDirect, "protocol": "freedom", "streamSettings": obj{"sockopt": obj{"interface": l.Interface}},
	})
	cfg["dns"] = obj{"tag": tagDNS, "servers": l.DNS, "queryStrategy": l.strategy()}
	routing := cfg["routing"].(obj)
	routing["rules"] = append([]any{obj{"type": "field", "inboundTag": []string{tagDNS}, "outboundTag": tagDirect}},
		routing["rules"].([]any)...)
	return nil
}

func (l *Local) strategy() string {
	switch l.Strategy {
	case "UseIP", "UseIPv4", "UseIPv6":
		return l.Strategy
	}
	return "UseIPv4"
}

func probeBase(inbound obj, outbounds []any, rules []any) obj {
	return obj{
		"log":       obj{"loglevel": "warning"},
		"inbounds":  []any{inbound},
		"outbounds": outbounds,
		"routing":   obj{"rules": rules},
	}
}

// bind applies interface-bound resolution to a chain's first hop; nil leaves it unchanged.
func (l *Local) bind(out any) {
	if l == nil {
		return
	}
	so := sockopt(out.(obj))
	so["interface"], so["domainStrategy"] = l.Interface, l.strategy()
}

// BuildProbe maps each local mixed port to its chain; local binds first-hop dialing and DNS.
func BuildProbe(routes []PortRoute, local *Local) ([]byte, error) {
	inbounds, outbounds, rules, err := portParts(routes, false)
	if err != nil {
		return nil, err
	}
	cfg := obj{"log": obj{"loglevel": "warning"}, "inbounds": inbounds, "outbounds": outbounds, "routing": obj{"rules": rules}}
	if local != nil {
		if err := local.direct(cfg); err != nil {
			return nil, err
		}
		// portParts lists each chain's outbounds in order: a route's first is the one after the last route's.
		first := 0
		for _, r := range routes {
			local.bind(outbounds[first])
			first += len(r.Chain)
		}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// Check reports why n cannot be made into an outbound.
func Check(n *node.Node) error {
	_, err := outbound(n)
	return err
}

// BuildResolver forwards local DNS queries through the physical interface.
// Strict TUN filtering may prevent non-Xray processes from querying DNS directly.
func BuildResolver(port int, local Local) ([]byte, error) {
	if len(local.DNS) == 0 {
		return nil, errors.New("本地网络没有 DNS 服务器")
	}
	const in = "resolver-in"
	cfg := probeBase(obj{
		"tag": in, "protocol": "dokodemo-door", "listen": "127.0.0.1", "port": port,
		"settings": obj{"address": local.DNS[0], "port": 53, "network": "tcp,udp"},
	}, []any{obj{"tag": tagDNSOut, "protocol": "dns"}},
		[]any{obj{"type": "field", "inboundTag": []string{in}, "outboundTag": tagDNSOut}})
	if err := local.direct(cfg); err != nil {
		return nil, err
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// PortRoute sends everything arriving on a local port through a chain.
type PortRoute struct {
	Port  int
	Chain []*node.Node
}

func portParts(routes []PortRoute, allowLAN bool) (inbounds, outbounds, rules []any, err error) {
	listen := "127.0.0.1"
	if allowLAN {
		listen = "0.0.0.0"
	}
	for _, r := range routes {
		in, prefix := fmt.Sprintf("port-%d", r.Port), fmt.Sprintf("port-%d-", r.Port)
		proxies, err := chainOutbounds(r.Chain, prefix)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("端口 %d：%w", r.Port, err)
		}
		inbounds = append(inbounds, obj{
			"tag": in, "protocol": "socks", "listen": listen, "port": r.Port,
			"settings": obj{"auth": "noauth", "udp": true},
		})
		outbounds = append(outbounds, proxies...)
		rules = append(rules, obj{"type": "field", "inboundTag": []string{in}, "outboundTag": prefix + tagProxy})
	}
	return inbounds, outbounds, rules, nil
}

// chainOutbounds tags the final hop as prefix+proxy and dials each hop through its predecessor.
func chainOutbounds(chain []*node.Node, prefix string) ([]any, error) {
	if len(chain) == 0 {
		return nil, errors.New("没有要连接的节点")
	}
	list := make([]any, len(chain))
	prev := ""
	for i, n := range chain {
		out, err := outbound(n)
		if err != nil {
			if len(chain) > 1 {
				return nil, fmt.Errorf("%s：%w", n.Label(), err)
			}
			return nil, err
		}
		tag := chainTags(prefix, len(chain))[i]
		out["tag"] = tag
		if prev != "" {
			dialThrough(out, prev)
		}
		list[i], prev = out, tag
	}
	return list, nil
}

func chainTags(prefix string, n int) []string {
	tags := make([]string, n)
	for i := range n - 1 {
		tags[i] = fmt.Sprintf("%schain-%d", prefix, i)
	}
	tags[n-1] = prefix + tagProxy
	return tags
}

func dialThrough(out obj, via string) { sockopt(out)["dialerProxy"] = via }

func sockopt(out obj) obj {
	stream, _ := out["streamSettings"].(obj)
	if stream == nil {
		stream = obj{}
		out["streamSettings"] = stream
	}
	so, _ := stream["sockopt"].(obj)
	if so == nil {
		so = obj{}
		stream["sockopt"] = so
	}
	return so
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
	listen := "127.0.0.1"
	if o.AllowLAN {
		listen = "0.0.0.0"
	}
	return exclusiveInbound(obj{"tag": tagMixed, "protocol": "socks", "listen": listen, "port": o.Port, "settings": obj{"auth": "noauth", "udp": true},
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

func outbound(n *node.Node) (obj, error) {
	if n.Outbound != "" {
		// Imported from Xray JSON: use it verbatim with RayDock's tag.
		var out obj
		if err := json.Unmarshal([]byte(n.Outbound), &out); err != nil {
			return nil, fmt.Errorf("节点的出站 JSON 无效：%w", err)
		}
		if out == nil {
			return nil, errors.New("节点的出站 JSON 必须是对象")
		}
		protocol, _ := out["protocol"].(string)
		if strings.TrimSpace(protocol) == "" {
			return nil, errors.New("节点的出站 JSON 缺少 protocol")
		}
		out["tag"] = tagProxy
		return out, nil
	}
	out := obj{"tag": tagProxy}
	switch n.Protocol {
	case node.Shadowsocks:
		out["protocol"] = "shadowsocks"
		out["settings"] = obj{"servers": []any{obj{
			"address": n.Host, "port": n.Port, "method": n.Cipher, "password": n.Password,
		}}}
	case node.VMess:
		security := n.Cipher
		if security == "" {
			security = "auto"
		}
		out["protocol"] = "vmess"
		out["settings"] = obj{"vnext": []any{obj{
			"address": n.Host, "port": n.Port,
			"users": []any{obj{"id": n.UUID, "alterId": n.AlterID, "security": security}},
		}}}
	case node.VLESS:
		user := obj{"id": n.UUID, "encryption": "none"}
		if n.Encryption != "" {
			user["encryption"] = n.Encryption
		}
		if n.Flow != "" {
			user["flow"] = n.Flow
		}
		out["protocol"] = "vless"
		out["settings"] = obj{"vnext": []any{obj{"address": n.Host, "port": n.Port, "users": []any{user}}}}
	case node.Trojan:
		out["protocol"] = "trojan"
		out["settings"] = obj{"servers": []any{obj{"address": n.Host, "port": n.Port, "password": n.Password}}}
	case node.SOCKS, node.HTTP:
		settings := obj{"address": n.Host, "port": n.Port}
		if n.Username != "" || n.Password != "" {
			settings["user"], settings["pass"] = n.Username, n.Password
		}
		out["protocol"] = n.Protocol
		out["settings"] = settings
	case node.WireGuard:
		return wireguard(n)
	case node.Hysteria2:
		out["protocol"] = "hysteria"
		out["settings"] = obj{"version": 2, "address": n.Host, "port": n.Port}
		stream := obj{
			"network":          "hysteria",
			"security":         "tls",
			"tlsSettings":      tlsSettings(n, first(n.SNI, n.Host), false),
			"hysteriaSettings": obj{"version": 2, "auth": n.Password},
		}
		var masks []any
		if n.Obfs == "salamander" {
			masks = append(masks, obj{"type": "salamander", "settings": obj{"password": n.ObfsPassword}})
		}
		if n.HopPorts != "" {
			// Like the official Hysteria client: periodically switch to a new local port and a random server
			// port in the range. udphop must be the last layer.
			hop := obj{"mode": "intervalLocal,intervalRemote", "remotePorts": n.HopPorts}
			if n.HopInterval > 0 {
				hop["interval"] = n.HopInterval
			}
			masks = append(masks, obj{"type": "udphop", "settings": hop})
		}
		if err := setFinalMask(stream, n.FinalMask, masks); err != nil {
			return nil, err
		}
		out["streamSettings"] = stream
		return out, nil
	case node.MASQUE:
		return nil, fmt.Errorf("MASQUE 节点只能从 Xray 出站 JSON 导入")
	default:
		return nil, fmt.Errorf("不支持的协议 %q", n.Protocol)
	}
	stream, err := streamSettings(n)
	if err != nil {
		return nil, err
	}
	if err := checkPlaintext(n, stream); err != nil {
		return nil, err
	}
	out["streamSettings"] = stream
	return out, nil
}

// checkPlaintext refuses what Xray refuses to start with (infra/conf validateOutboundTransportSecurity):
// VLESS without encryption and Trojan, with neither TLS nor REALITY, to an address that is not private.
func checkPlaintext(n *node.Node, stream obj) error {
	if stream["security"] != "none" || !publicAddress(n.Host) {
		return nil
	}
	switch {
	case n.Protocol == node.VLESS && (n.Encryption == "" || n.Encryption == "none"):
		return errors.New("Xray 26 不允许 VLESS 在没有 TLS / REALITY 或 VLESS 加密时连接公网地址，该节点无法使用")
	case n.Protocol == node.Trojan:
		return errors.New("Xray 26 不允许 Trojan 在没有 TLS / REALITY 时连接公网地址，该节点无法使用")
	}
	return nil
}

// Domains Xray takes as private (common/geodata GetPrivateDomainMatcher).
var privateDomains = []string{"lan", "localdomain", "example", "invalid", "localhost", "test", "local", "home.arpa", "internal"}

// publicAddress reports whether Xray requires transport security to host (infra/conf
// requiresTransportSecurity): since 26.10.29 an IP is private when private, loopback or link-local,
// shared (100.64.0.0/10), benchmarking and documentation ranges no longer.
func publicAddress(host string) bool {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ip = ip.Unmap()
		return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	}
	d := strings.TrimSuffix(strings.ToLower(host), ".")
	if !strings.Contains(d, ".") {
		return false // dotless names
	}
	return !slices.ContainsFunc(privateDomains, func(p string) bool { return d == p || strings.HasSuffix(d, "."+p) })
}

// wireguard builds a WireGuard outbound, which has its own transport and no streamSettings.
func wireguard(n *node.Node) (obj, error) {
	peer := obj{"publicKey": n.PublicKey, "endpoint": n.Address()}
	if n.PreSharedKey != "" {
		peer["preSharedKey"] = n.PreSharedKey
	}
	settings := obj{"secretKey": n.SecretKey, "peers": []any{peer}}
	if addrs := splitList(n.LocalAddress); len(addrs) > 0 {
		settings["address"] = addrs
	}
	if n.MTU > 0 {
		settings["mtu"] = n.MTU
	}
	if n.Reserved != "" {
		var reserved []int
		for _, v := range splitList(n.Reserved) {
			b, err := strconv.Atoi(v)
			if err != nil || b < 0 || b > 255 {
				return nil, fmt.Errorf("WireGuard reserved %q 无效", n.Reserved)
			}
			reserved = append(reserved, b)
		}
		if len(reserved) != 3 {
			return nil, fmt.Errorf("WireGuard reserved 应为三个字节")
		}
		settings["reserved"] = reserved
	}
	return obj{"tag": tagProxy, "protocol": "wireguard", "settings": settings}, nil
}

// Keep explicit finalmask layers unchanged; otherwise derive them from legacy fields.
func setFinalMask(stream obj, fm string, udp []any) error {
	if fm != "" {
		var m obj
		if err := json.Unmarshal([]byte(fm), &m); err != nil {
			return fmt.Errorf("finalmask（fm）不是 JSON 对象")
		}
		stream["finalmask"] = m
		return nil
	}
	if len(udp) > 0 {
		stream["finalmask"] = obj{"udp": udp}
	}
	return nil
}

func streamSettings(n *node.Node) (obj, error) {
	network := strings.ToLower(first(n.Network, "tcp"))
	security := strings.ToLower(first(n.Security, "none"))
	s := obj{"network": network, "security": security}

	switch security {
	case "none":
	case "tls":
		// Behind a CDN, ws/xhttp/httpupgrade use the Host header as SNI when none is given.
		sni := n.SNI
		if sni == "" && (network == "ws" || network == "xhttp" || network == "httpupgrade") {
			sni = n.HostHeader
		}
		s["tlsSettings"] = tlsSettings(n, first(sni, n.Host), true)
	case "reality":
		if n.PublicKey == "" {
			return nil, fmt.Errorf("REALITY 节点缺少公钥 pbk")
		}
		r := obj{
			"serverName":  first(n.SNI, n.Host),
			"fingerprint": first(n.Fingerprint, "chrome"),
			"publicKey":   n.PublicKey,
			"shortId":     n.ShortID,
			"spiderX":     first(n.SpiderX, "/"),
		}
		if n.MLDSA65 != "" {
			r["mldsa65Verify"] = n.MLDSA65
		}
		s["realitySettings"] = r
	default:
		return nil, fmt.Errorf("不支持的安全层 %q", security)
	}

	var masks []any
	switch network {
	case "tcp", "raw":
		if n.HeaderType == "http" {
			req := obj{"path": []string{first(n.Path, "/")}}
			if hosts := splitList(n.HostHeader); len(hosts) > 0 {
				req["headers"] = obj{"Host": hosts}
			}
			s["tcpSettings"] = obj{"header": obj{"type": "http", "request": req}}
		}
	case "kcp":
		masks = kcpMasks(n)
	case "ws":
		s["wsSettings"] = obj{"path": first(n.Path, "/"), "host": n.HostHeader}
	case "httpupgrade":
		s["httpupgradeSettings"] = obj{"path": first(n.Path, "/"), "host": n.HostHeader}
	case "grpc":
		g := obj{"serviceName": n.Path, "multiMode": n.Mode == "multi"}
		if n.Authority != "" {
			g["authority"] = n.Authority
		}
		s["grpcSettings"] = g
	case "xhttp":
		x := obj{"path": first(n.Path, "/")}
		if n.HostHeader != "" {
			x["host"] = n.HostHeader
		}
		if n.Mode != "" {
			x["mode"] = n.Mode
		}
		if n.Extra != "" {
			var extra obj
			if err := json.Unmarshal([]byte(n.Extra), &extra); err != nil {
				return nil, fmt.Errorf("XHTTP extra 不是 JSON 对象")
			}
			x["extra"] = extra
		}
		s["xhttpSettings"] = x
	default:
		return nil, fmt.Errorf("不支持的传输方式 %q", network)
	}
	if err := setFinalMask(s, n.FinalMask, masks); err != nil {
		return nil, err
	}
	return s, nil
}

// Legacy mKCP layers are applied encryption-first, header-last, matching their on-wire order.
func kcpMasks(n *node.Node) []any {
	security := obj{"type": "mkcp-legacy"}
	if n.Seed != "" {
		security["settings"] = obj{"value": n.Seed}
	}
	masks := []any{security}
	switch h := strings.ToLower(n.HeaderType); h {
	case "", "none":
	case "dns":
		masks = append(masks, obj{"type": "mkcp-legacy", "settings": obj{"header": h, "value": n.HostHeader}})
	default:
		masks = append(masks, obj{"type": "mkcp-legacy", "settings": obj{"header": strings.TrimSuffix(h, "-video")}})
	}
	return masks
}

// tlsSettings never emits removed allowInsecure settings; use pinning or name verification.
// uTLS fingerprints apply only to TCP transports.
func tlsSettings(n *node.Node, sni string, utls bool) obj {
	t := obj{"serverName": sni}
	if pin := n.Pin(); pin != "" {
		t["pinnedPeerCertSha256"] = pin
	}
	if n.VerifyName != "" {
		t["verifyPeerCertByName"] = n.VerifyName
	}
	if n.ECHConfig != "" {
		t["echConfigList"] = n.ECHConfig
	}
	if alpn := splitList(n.ALPN); len(alpn) > 0 {
		t["alpn"] = alpn
	} else if n.Protocol == node.HTTP {
		// An HTTPS proxy takes CONNECT over HTTP/1.1, as curl and Clash send it. Offered h2 as well (Xray's
		// default), a server speaking it for its own sites gets Xray's HTTP/2 CONNECT and refuses it (404).
		t["alpn"] = []string{"http/1.1"}
	}
	if utls {
		t["fingerprint"] = first(n.Fingerprint, "chrome")
	}
	return t
}

// Warnings lists node settings the current Xray cannot honor.
func Warnings(n *node.Node) []string {
	var w []string
	if n.Insecure && n.Pin() == "" && n.VerifyName == "" && (n.Security == "tls" || n.Protocol == node.Hysteria2) {
		w = append(w, "节点要求跳过证书验证，但 Xray 26 已移除 allowInsecure，且尚未取得服务器证书，将按正常证书校验连接；"+
			"服务器使用自签名证书时会失败，取得证书后会自动固定并重新连接，也可以在编辑节点时填写证书指纹")
	}
	return w
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

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
