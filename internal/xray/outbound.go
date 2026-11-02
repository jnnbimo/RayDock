package xray

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"raydock/internal/node"
)

// Check reports why n cannot be made into an outbound.
func Check(n *node.Node) error {
	_, err := outbound(n)
	return err
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
			"tlsSettings":      tlsSettings(n, cmp.Or(n.SNI, n.Host), false),
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
	network := strings.ToLower(cmp.Or(n.Network, "tcp"))
	security := strings.ToLower(cmp.Or(n.Security, "none"))
	s := obj{"network": network, "security": security}

	switch security {
	case "none":
	case "tls":
		// Behind a CDN, ws/xhttp/httpupgrade use the Host header as SNI when none is given.
		sni := n.SNI
		if sni == "" && (network == "ws" || network == "xhttp" || network == "httpupgrade") {
			sni = n.HostHeader
		}
		s["tlsSettings"] = tlsSettings(n, cmp.Or(sni, n.Host), true)
	case "reality":
		if n.PublicKey == "" {
			return nil, fmt.Errorf("REALITY 节点缺少公钥 pbk")
		}
		r := obj{
			"serverName":  cmp.Or(n.SNI, n.Host),
			"fingerprint": cmp.Or(n.Fingerprint, "chrome"),
			"publicKey":   n.PublicKey,
			"shortId":     n.ShortID,
			"spiderX":     cmp.Or(n.SpiderX, "/"),
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
			req := obj{"path": []string{cmp.Or(n.Path, "/")}}
			if hosts := splitList(n.HostHeader); len(hosts) > 0 {
				req["headers"] = obj{"Host": hosts}
			}
			s["tcpSettings"] = obj{"header": obj{"type": "http", "request": req}}
		}
	case "kcp":
		masks = kcpMasks(n)
	case "ws":
		s["wsSettings"] = obj{"path": cmp.Or(n.Path, "/"), "host": n.HostHeader}
	case "httpupgrade":
		s["httpupgradeSettings"] = obj{"path": cmp.Or(n.Path, "/"), "host": n.HostHeader}
	case "grpc":
		g := obj{"serviceName": n.Path, "multiMode": n.Mode == "multi"}
		if n.Authority != "" {
			g["authority"] = n.Authority
		}
		s["grpcSettings"] = g
	case "xhttp":
		x := obj{"path": cmp.Or(n.Path, "/")}
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
		t["fingerprint"] = cmp.Or(n.Fingerprint, "chrome")
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
