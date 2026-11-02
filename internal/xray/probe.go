package xray

import (
	"encoding/json"
	"errors"
	"fmt"
)

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

func (l *Local) strategy() string { return queryStrategy(l.Strategy) }

// probeBase is the config of a measurement core: these inbounds, outbounds and rules, at the warning level.
func probeBase(inbounds, outbounds, rules []any) obj {
	return obj{
		"log":       obj{"loglevel": "warning"},
		"inbounds":  inbounds,
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
	inbounds, outbounds, rules, err := portParts(routes)
	if err != nil {
		return nil, err
	}
	cfg := probeBase(inbounds, outbounds, rules)
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

// BuildResolver forwards local DNS queries through the physical interface.
// Strict TUN filtering may prevent non-Xray processes from querying DNS directly.
func BuildResolver(port int, local Local) ([]byte, error) {
	if len(local.DNS) == 0 {
		return nil, errors.New("本地网络没有 DNS 服务器")
	}
	const in = "resolver-in"
	cfg := probeBase([]any{obj{
		"tag": in, "protocol": "dokodemo-door", "listen": "127.0.0.1", "port": port,
		"settings": obj{"address": local.DNS[0], "port": 53, "network": "tcp,udp"},
	}}, []any{obj{"tag": tagDNSOut, "protocol": "dns"}},
		[]any{obj{"type": "field", "inboundTag": []string{in}, "outboundTag": tagDNSOut}})
	if err := local.direct(cfg); err != nil {
		return nil, err
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// portParts returns the parts of a probe serving routes on the loopback, each chain's outbounds apart.
func portParts(routes []PortRoute) (inbounds, outbounds, rules []any, err error) {
	for _, r := range routes {
		prefix := portTag(r.Port) + "-"
		proxies, err := chainOutbounds(r.Chain, prefix)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("端口 %d：%w", r.Port, err)
		}
		inbounds = append(inbounds, portInbound(r.Port, false))
		outbounds = append(outbounds, proxies...)
		rules = append(rules, obj{"type": "field", "inboundTag": []string{portTag(r.Port)}, "outboundTag": prefix + tagProxy})
	}
	return inbounds, outbounds, rules, nil
}
