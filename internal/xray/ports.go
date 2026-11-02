package xray

import (
	"fmt"

	"raydock/internal/node"
)

// A dedicated port is a mixed (SOCKS5 and HTTP) inbound, listening like the local port, whose connections
// go through the port's chain before any other rule (dedicated).

// portTag tags a dedicated port's inbound.
func portTag(port int) string { return fmt.Sprintf("port-%d", port) }

func portInbound(port int, lan bool) obj {
	return exclusiveInbound(obj{"tag": portTag(port), "protocol": "socks", "listen": ListenAddr(lan), "port": port,
		"settings": obj{"auth": "noauth", "udp": true}})
}

// ListenAddr is where a proxy listener listens: on the loopback, or with lan on every interface.
func ListenAddr(lan bool) string {
	if lan {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// PortRoute sends everything arriving on a local port through a chain.
type PortRoute struct {
	Port  int
	Chain []*node.Node
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
