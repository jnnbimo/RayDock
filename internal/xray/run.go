package xray

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"raydock/internal/node"
)

// RayDock runs one core, which reads its config at start only: any change restarts it. It serves the
// connection, if any (the local port, under TUN the TUN, the routing through the connected chain), and the
// dedicated ports, each through its own chain.
//
// A restart starts the new core beside the old one, which stops once the new one runs (Overlap): under
// TUN, the system's routes always lead to one of the two adapters (TUNNames), so that no traffic leaves
// around them while the core restarts. Public listeners activate only after the old core exits.

// Overlap tells whether a candidate starts beside the active core, on another TUN adapter. Public
// listeners activate only after the active core exits. Elsewhere the old core stops first.
const Overlap = runtime.GOOS == "windows"

// Run is what the core serves besides the options.
type Run struct {
	// Chain is the connected node with its group's proxies: the first dialed directly, each later one
	// through the one before. Without one there is no connection: the core serves the dedicated ports
	// alone, without TUN.
	Chain []*node.Node
	Ports []PortRoute // the dedicated ports
	// Local, under TUN, is the physical interface the core's own connections leave by, the TUN's routes
	// would take them otherwise, and whose DNS servers resolve the nodes' addresses (nodeDNS).
	Local *Local
	// Adapter, under TUN, picks one of TUNNames: the core restarting starts on the other one.
	Adapter  int
	Deferred bool // start without public listeners; ActivateListeners adds them after the old core stops
}

// Build returns the config of the core serving r.
func Build(o Options, r Run) ([]byte, error) {
	if o.APIPort > 0 && o.APIToken == "" {
		return nil, errors.New("核心接口缺少认证令牌")
	}
	if r.Deferred && o.APIPort <= 0 {
		return nil, errors.New("延后启用端口需要核心接口")
	}
	if r.Chain == nil {
		return buildPorts(o, r)
	}
	if o.Port <= 0 || o.Port > 65535 {
		return nil, fmt.Errorf("本地端口 %d 无效", o.Port)
	}
	if o.TUN.Enabled {
		if r.Local == nil {
			return nil, errors.New("TUN 模式缺少本地网络")
		}
		if r.Adapter < 0 || r.Adapter >= len(TUNNames) {
			return nil, errors.New("TUN 虚拟网卡编号无效")
		}
	}
	rules, err := parseAllRules(o)
	if err != nil {
		return nil, err
	}
	hosts, herrs := ParseHosts(o.Hosts)
	if len(herrs) > 0 {
		return nil, fmt.Errorf("Hosts %v", herrs[0])
	}
	direct, err := parseDNS(o.DirectDNS, "223.5.5.5")
	if err != nil {
		return nil, fmt.Errorf("国内 DNS：%w", err)
	}
	remote, err := parseDNS(o.RemoteDNS, "8.8.8.8")
	if err != nil {
		return nil, fmt.Errorf("远程 DNS：%w", err)
	}
	strategy := dnsStrategy(o)
	proxies, proxyTag, err := nodeOutbounds(r.Chain, r.Local)
	if err != nil {
		return nil, err
	}
	// The proxy outbound comes first, as the default outbound; every routing ends in a rule of its own.
	proxies = append(proxies[len(proxies)-1:], proxies[:len(proxies)-1]...)
	ports, err := dedicated(r.Ports, o.AllowLAN, r.Local)
	if err != nil {
		return nil, err
	}
	chains := [][]*node.Node{r.Chain}
	for _, p := range r.Ports {
		chains = append(chains, p.Chain)
	}
	nodeServers, viaDirect := nodeDNS(firstHosts(chains...), r.Local)
	directDomains := append([]string{"geosite:cn", "geosite:private"}, rules.direct.Domains...)
	inbounds := []any{mixedInbound(o)}
	if o.TUN.Enabled {
		inbounds = append(inbounds, tunInbound(o, r.Adapter))
	}
	routes := routing(o, rules, proxyTag)
	// A dedicated port's connections go through its chain before any other rule.
	routes["rules"] = append(ports.rules, routes["rules"].([]any)...)
	cfg := obj{
		"log":       obj{"loglevel": logLevel(o.LogLevel)},
		"inbounds":  append(inbounds, ports.inbounds...),
		"outbounds": outbounds(addOutbounds(proxies, ports.outbounds), o, strategy, r.Local),
		"routing":   routes,
		"dns":       dnsConfig(o, direct, remote, append(directDomains, viaDirect...), hosts, nodeServers),
	}
	if o.TUN.Enabled && o.FakeDNS {
		pools := []any{obj{"ipPool": fakePool4, "poolSize": 65535}}
		if strategy != "UseIPv4" {
			pools = append(pools, obj{"ipPool": fakePool6, "poolSize": 65535})
		}
		cfg["fakedns"] = pools
	}
	finishRun(cfg, o, r.Deferred)
	return json.MarshalIndent(cfg, "", "  ")
}

// buildPorts returns the config of the core serving the dedicated ports alone, without a connection: of
// the options it uses LAN access and the log level.
func buildPorts(o Options, r Run) ([]byte, error) {
	if len(r.Ports) == 0 {
		return nil, errors.New("没有专用端口")
	}
	ports, err := dedicated(r.Ports, o.AllowLAN, nil)
	if err != nil {
		return nil, err
	}
	// Nothing but the ports' connections comes in; whatever no rule takes is dropped.
	cfg := obj{
		"log":       obj{"loglevel": logLevel(o.LogLevel)},
		"inbounds":  ports.inbounds,
		"outbounds": append([]any{obj{"tag": tagBlock, "protocol": "blackhole", "settings": obj{}}}, ports.outbounds...),
		"routing":   obj{"rules": ports.rules},
	}
	finishRun(cfg, o, r.Deferred)
	return json.MarshalIndent(cfg, "", "  ")
}

// ports are the parts serving the dedicated ports.
type ports struct{ inbounds, outbounds, rules []any }

// dedicated returns the parts serving routes, their chains' first hops bound to local, if any. A chain
// the same as another's shares its outbounds (nodeOutbounds).
func dedicated(routes []PortRoute, lan bool, local *Local) (ports, error) {
	var p ports
	for _, r := range routes {
		chain, tag, err := nodeOutbounds(r.Chain, local)
		if err != nil {
			return ports{}, fmt.Errorf("端口 %d：%w", r.Port, err)
		}
		p.outbounds = addOutbounds(p.outbounds, chain)
		p.inbounds = append(p.inbounds, portInbound(r.Port, lan))
		p.rules = append(p.rules, obj{"type": "field", "inboundTag": []string{portTag(r.Port)}, "outboundTag": tag})
	}
	return p, nil
}

// addOutbounds appends those of outs whose tag list lacks.
func addOutbounds(list, outs []any) []any {
	for _, o := range outs {
		if !slices.ContainsFunc(list, func(x any) bool { return tagOf(x) == tagOf(o) }) {
			list = append(list, o)
		}
	}
	return list
}

// TUNNames are the two adapters a restarting core alternates between: the new core's adapter takes the
// routes while the old one's is still there. Each has addresses of its own.
var TUNNames = [2]string{"RayDock", "RayDock 2"}

var tunAddrs = [2]struct{ gateway4, dns4, gateway6 string }{
	{"172.19.0.1/30", "172.19.0.2", "fdfe:dcba:9876::1/126"},
	{"172.19.0.5/30", "172.19.0.6", "fdfe:dcba:9876::5/126"},
}

func tunInbound(o Options, adapter int) obj {
	t, addrs := o.TUN, tunAddrs[adapter]
	mtu := t.MTU
	if mtu < 1280 || mtu > 9000 {
		mtu = defaultMTU
	}
	// Two halves rather than a default route: Xray picks the interface its own connections leave by among
	// the default routes (autoOutboundsInterface), and must not pick the other core's adapter.
	gateway, routes := []string{addrs.gateway4}, []string{"0.0.0.0/1", "128.0.0.0/1"}
	if t.IPv6 {
		gateway, routes = append(gateway, addrs.gateway6), append(routes, "::/1", "8000::/1")
	}
	settings := obj{
		"mtu":                    mtu,
		"gateway":                gateway,
		"dns":                    []string{addrs.dns4},
		"autoSystemRoutingTable": routes,
		// Binds the core's own connections to the physical interface.
		"autoOutboundsInterface": "auto",
	}
	if runtime.GOOS == "windows" {
		settings["name"] = TUNNames[adapter]
		if t.Strict {
			// The rules permit xray.exe by its path: the other core's connections as well.
			settings["autoSystemWfpBlockLeak"] = []string{"dns", "misconfigtun"}
		}
	}
	destOverride := []string{"http", "tls", "quic"}
	if o.FakeDNS {
		destOverride = append(destOverride, "fakedns")
	}
	return obj{"tag": tagTUN, "protocol": "tun", "settings": settings,
		"sniffing": obj{"enabled": true, "destOverride": destOverride, "metadataOnly": false}}
}

// exclusiveInbound prevents another process, even using SO_REUSEADDR, from joining a proxy listener.
func exclusiveInbound(in obj) obj {
	if Overlap {
		in["streamSettings"] = obj{"sockopt": obj{"customSockopt": []any{obj{
			"system": "windows", "level": "65535", "opt": "-5", "value": "1", "type": "int",
		}}}}
	}
	return in
}

// nodePrefix starts the tags of the outbounds reaching nodes. A chain's are named after its config, so
// that the same chain of the connection and dedicated ports has one set of outbounds: one WireGuard
// device, for one.
const nodePrefix = "node-"

func tagOf(o any) string {
	m, _ := o.(map[string]any)
	s, _ := m["tag"].(string)
	return s
}

// nodeOutbounds returns chain's outbounds, under TUN the first bound to the physical interface, and
// the tag of the last, which reaches the final node.
func nodeOutbounds(chain []*node.Node, local *Local) ([]any, string, error) {
	plain, err := chainOutbounds(chain, "")
	if err != nil {
		return nil, "", err
	}
	local.bind(plain[0])
	data, err := json.Marshal(plain)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	prefix := fmt.Sprintf("%s%x-", nodePrefix, sum[:4])
	outs, err := chainOutbounds(chain, prefix)
	if err != nil {
		return nil, "", err
	}
	local.bind(outs[0])
	return outs, prefix + tagProxy, nil
}

// firstHosts returns the domains of the first nodes of chains, lower-cased, sorted and unique.
func firstHosts(chains ...[]*node.Node) []string {
	var out []string
	for _, chain := range chains {
		if len(chain) > 0 && chain[0].Host != "" && !isIP(chain[0].Host) {
			out = append(out, strings.ToLower(chain[0].Host))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// nodeDNS resolves the first nodes' addresses (hosts) directly through the physical network's DNS. The
// resolvers' tag tells these queries from proxied ones, even to the same IP.
func nodeDNS(hosts []string, l *Local) (servers []any, viaDirect []string) {
	if l == nil || len(hosts) == 0 {
		return nil, nil
	}
	var domains []string
	for _, h := range hosts {
		domains = append(domains, "full:"+h)
	}
	for _, ip := range l.DNS {
		servers = append(servers, obj{"address": ip, "tag": tagDNS, "domains": domains, "skipFallback": true, "queryStrategy": l.strategy()})
	}
	if len(servers) == 0 {
		return nil, domains // no physical resolver: the domestic resolver bootstraps the nodes
	}
	return servers, nil
}
