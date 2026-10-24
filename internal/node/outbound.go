package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// outboundProtocols maps Xray outbound protocols to node protocols. Others (freedom, blackhole,
// dns, loopback) are not proxies and are skipped.
var outboundProtocols = map[string]string{
	"vless":       VLESS,
	"vmess":       VMess,
	"trojan":      Trojan,
	"shadowsocks": Shadowsocks,
	"socks":       SOCKS,
	"http":        HTTP,
	"hysteria":    Hysteria2,
	"wireguard":   WireGuard,
	"masque":      MASQUE,
}

// ParseJSON parses Xray JSON: an outbound, a list of outbounds, a full config (its proxy outbounds)
// or a list of full configs (v2rayN custom subscriptions). Outbounds are kept verbatim, so every
// field Xray supports survives, including MASQUE, which has no share link format.
func ParseJSON(text string) (nodes []Node, errs []error) {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return nil, []error{fmt.Errorf("JSON 格式错误：%w", err)}
	}
	var walk func(v any, name string)
	walk = func(v any, name string) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e, name)
			}
		case map[string]any:
			if list, ok := x["outbounds"].([]any); ok {
				remarks, _ := x["remarks"].(string)
				for _, o := range list {
					walk(o, remarks)
				}
				return
			}
			if _, ok := x["protocol"]; !ok {
				errs = append(errs, errors.New("JSON 中既没有 outbounds，也不是出站（缺少 protocol）"))
				return
			}
			n, ok, err := fromOutbound(x, name)
			switch {
			case err != nil:
				errs = append(errs, err)
			case ok:
				nodes = append(nodes, n)
			}
		default:
			errs = append(errs, errors.New("JSON 中没有出站"))
		}
	}
	walk(v, "")
	return nodes, errs
}

// fromOutbound converts an outbound; ok is false for non-proxy outbounds.
func fromOutbound(o map[string]any, name string) (n Node, ok bool, err error) {
	proto := strings.ToLower(str(o["protocol"]))
	n.Protocol = outboundProtocols[proto]
	if n.Protocol == "" {
		switch proto {
		case "freedom", "direct", "blackhole", "block", "dns", "loopback":
			return n, false, nil
		}
		return n, false, fmt.Errorf("不支持的出站协议 %q", proto)
	}
	tag := str(o["tag"])
	n.Name = first(name, tag)
	if name != "" && tag != "" && tag != "proxy" {
		n.Name = name + " · " + tag
	}

	s, _ := o["settings"].(map[string]any)
	server := s
	for _, key := range []string{"vnext", "servers", "peers"} {
		if list, _ := s[key].([]any); len(list) > 0 {
			server, _ = list[0].(map[string]any)
			break
		}
	}
	n.Host, n.Port = str(server["address"]), num(server["port"])
	if ep := str(server["endpoint"]); ep != "" {
		if n.Host, n.Port, err = splitHostPort(ep); err != nil {
			return n, false, fmt.Errorf("%s 出站的 endpoint 无效", proto)
		}
	}
	if n.Host == "" || n.Port <= 0 || n.Port > 65535 {
		return n, false, fmt.Errorf("%s 出站缺少有效的地址或端口", proto)
	}

	// Transport and security only serve display and the latency test.
	if ss, _ := o["streamSettings"].(map[string]any); ss != nil {
		raw := strings.ToLower(first(str(ss["network"]), str(ss["method"])))
		if n.Network, err = network(raw); err != nil {
			n.Network, err = raw, nil // protocol-specific transports such as hysteria and masque
		}
		n.Security = strings.ToLower(str(ss["security"]))
		if tls, _ := ss["tlsSettings"].(map[string]any); tls != nil {
			n.ALPN = joinList(tls["alpn"])
		}
	}

	delete(o, "tag") // RayDock sets its own tag
	b, err := json.Marshal(o)
	if err != nil {
		return n, false, err
	}
	n.Outbound = string(b)
	n.ID = NewID()
	return n, true, nil
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func joinList(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, e := range x {
			parts = append(parts, str(e))
		}
		return strings.Join(parts, ",")
	}
	return ""
}
