package node

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Clash / Mihomo configs list their nodes under proxies, with more than share links can say: a
// subscription's link format may drop that an HTTP proxy runs over TLS with a certificate to accept,
// which its Clash format keeps (tls, skip-cert-verify). Field names follow the Mihomo wiki
// (wiki.metacubex.one/config/proxies).

var clashProxies = regexp.MustCompile(`(?m)^proxies:`)

// isClash reports whether text is a Clash / Mihomo config: YAML with a top-level proxies list.
func isClash(text string) bool { return clashProxies.MatchString(text) }

// ParseClash parses the proxies of a Clash / Mihomo config. It returns the nodes and one error per entry
// that is not a proxy Xray can run.
func ParseClash(text string) (nodes []Node, errs []error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, []error{fmt.Errorf("Clash 配置格式错误：%w", err)}
	}
	if err := yamlStrings(&doc, 0); err != nil {
		return nil, []error{err}
	}
	var body struct {
		Proxies []any `yaml:"proxies"`
	}
	// Decode only proxies using yaml's cycle and alias-expansion guards (including merge keys).
	if err := doc.Decode(&body); err != nil {
		return nil, []error{fmt.Errorf("Clash 配置格式错误：%w", err)}
	}
	list := body.Proxies
	if len(list) == 0 {
		return nil, []error{errors.New("Clash 配置中没有 proxies")}
	}
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			errs = append(errs, errors.New("Clash 节点不是键值对"))
			continue
		}
		c := clashMap(m)
		n, err := fromClash(c)
		if err == nil {
			// Validate the full model; the optional share link is never an input to normalization.
			n.ID = NewID()
			n, err = Normalize(n)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("Clash 节点「%s」（%s）：%w", c.str("name"), c.str("type"), err))
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, errs
}

// yamlStrings preserves scalar text (0123 IDs and numeric passwords), without expanding aliases.
// The syntax tree's Content edges are acyclic; yaml.Decode handles Alias edges with its own guards.
func yamlStrings(n *yaml.Node, depth int) error {
	if depth > 128 {
		return errors.New("Clash 配置嵌套过深")
	}
	if n.Kind == yaml.ScalarNode && n.ShortTag() != "!!null" && n.ShortTag() != "!!merge" {
		n.Tag = "!!str"
	}
	for _, child := range n.Content {
		if err := yamlStrings(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// clashMap reads a proxy entry or one of its option maps.
type clashMap map[string]any

func (c clashMap) str(keys ...string) string {
	for _, k := range keys {
		if s, ok := c[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// secret reads opaque credentials verbatim, including meaningful whitespace.
func (c clashMap) secret(key string) string {
	s, _ := c[key].(string)
	return s
}

func (c clashMap) num(key string) int {
	v, _ := strconv.Atoi(c.str(key))
	return v
}

func (c clashMap) flag(key string) bool { return truthy(c.str(key)) }

func (c clashMap) sub(key string) clashMap {
	m, _ := c[key].(map[string]any)
	return m
}

// list reads a list, or a single value, as strings.
func (c clashMap) list(key string) []string {
	switch v := c[key].(type) {
	case string:
		if v = strings.TrimSpace(v); v != "" {
			return []string{v}
		}
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

// header reads a header from a headers map, whatever its case, as one value.
func (c clashMap) header(name string) string {
	for k := range c {
		if strings.EqualFold(k, name) {
			return strings.Join(c.list(k), ",")
		}
	}
	return ""
}

func fromClash(c clashMap) (Node, error) {
	n := Node{Name: c.str("name"), Host: c.str("server"), Port: c.num("port")}
	switch typ := strings.ToLower(c.str("type")); typ {
	case "ss":
		if c.str("plugin") != "" {
			return Node{}, errors.New("Xray 不支持 SIP003 插件")
		}
		n.Protocol, n.Cipher, n.Password = Shadowsocks, c.str("cipher"), c.secret("password")
		if n.Cipher == "" || n.Password == "" {
			return Node{}, errors.New("缺少加密方法或密码")
		}
		n.Network, n.Security = "tcp", "none"
	case "vmess":
		n.Protocol, n.UUID, n.AlterID, n.Cipher = VMess, c.secret("uuid"), c.num("alterId"), c.str("cipher")
		if n.UUID == "" {
			return Node{}, errors.New("缺少用户 ID")
		}
		if err := clashStream(&n, c, "none"); err != nil {
			return Node{}, err
		}
	case "vless":
		n.Protocol, n.UUID, n.Flow = VLESS, c.secret("uuid"), c.str("flow")
		if n.UUID == "" {
			return Node{}, errors.New("缺少用户 ID")
		}
		if enc := c.str("encryption"); enc != "none" {
			n.Encryption = enc
		}
		if err := clashStream(&n, c, "none"); err != nil {
			return Node{}, err
		}
	case "trojan":
		n.Protocol, n.Password = Trojan, c.secret("password")
		if n.Password == "" {
			return Node{}, errors.New("缺少密码")
		}
		if err := clashStream(&n, c, "tls"); err != nil { // Trojan always runs over TLS
			return Node{}, err
		}
	case "hysteria2":
		if err := clashHysteria2(&n, c); err != nil {
			return Node{}, err
		}
	case "socks5":
		n.Protocol, n.Username, n.Password, n.Network = SOCKS, c.secret("username"), c.secret("password"), "tcp"
		if err := clashTLS(&n, c, "none"); err != nil {
			return Node{}, err
		}
	case "http":
		n.Protocol, n.Username, n.Password, n.Network = HTTP, c.secret("username"), c.secret("password"), "tcp"
		if err := clashTLS(&n, c, "none"); err != nil {
			return Node{}, err
		}
	case "wireguard":
		if err := clashWireGuard(&n, c); err != nil {
			return Node{}, err
		}
	default:
		return Node{}, fmt.Errorf("Xray 不支持该类型")
	}
	return n, nil
}

// clashTLS reads the TLS options shared by every type; tls: true (or REALITY) turns security on over
// defaultSecurity.
func clashTLS(n *Node, c clashMap, defaultSecurity string) error {
	n.Security = defaultSecurity
	if c.flag("tls") {
		n.Security = "tls"
	}
	if r := c.sub("reality-opts"); r != nil {
		n.Security, n.PublicKey, n.ShortID = "reality", r.secret("public-key"), r.secret("short-id")
	}
	if n.Security == "none" {
		return nil
	}
	n.SNI = c.str("servername", "sni")
	n.ALPN = strings.Join(c.list("alpn"), ",")
	n.Fingerprint = c.str("client-fingerprint")
	n.Insecure = c.flag("skip-cert-verify")
	if n.Security != "tls" {
		return nil
	}
	var err error
	n.PinSHA256, err = clashPin(c)
	return err
}

// clashPin reads fingerprint, the SHA-256 of a certificate of the server (client-fingerprint is the uTLS
// one), which Xray pins alike: the leaf certificate is accepted as it is, a CA verifies the chain.
func clashPin(c clashMap) (string, error) {
	fp := c.str("fingerprint")
	if fp == "" {
		return "", nil
	}
	pin := strings.ToLower(strings.ReplaceAll(fp, ":", ""))
	if b, err := hex.DecodeString(pin); err != nil || len(b) != sha256.Size {
		return "", fmt.Errorf("证书指纹 fingerprint %q 无效", fp)
	}
	return pin, nil
}

// clashStream reads the transport (network and its options) and TLS of VMess, VLESS and Trojan.
func clashStream(n *Node, c clashMap, defaultSecurity string) error {
	if err := clashTLS(n, c, defaultSecurity); err != nil {
		return err
	}
	switch nw := strings.ToLower(c.str("network")); nw {
	case "", "tcp":
		n.Network = "tcp"
	case "http": // TCP with an HTTP header, not the removed HTTP/2 transport
		o := c.sub("http-opts")
		n.Network, n.HeaderType = "tcp", "http"
		if paths := o.list("path"); len(paths) > 0 {
			n.Path = paths[0]
		}
		n.HostHeader = o.sub("headers").header("Host")
	case "ws":
		o := c.sub("ws-opts")
		n.Network, n.Path, n.HostHeader = "ws", o.str("path"), o.sub("headers").header("Host")
		if o.flag("v2ray-http-upgrade") {
			n.Network = "httpupgrade"
		}
	case "grpc":
		n.Network, n.Path = "grpc", c.sub("grpc-opts").str("grpc-service-name")
	case "xhttp":
		o := c.sub("xhttp-opts")
		n.Network, n.Path, n.Mode = "xhttp", o.str("path"), o.str("mode")
		n.HostHeader = first(o.str("host"), o.sub("headers").header("Host"))
	default:
		var err error
		if n.Network, err = network(nw); err != nil {
			return err
		}
	}
	return nil
}

func clashHysteria2(n *Node, c clashMap) error {
	n.Protocol, n.Password, n.Security = Hysteria2, c.secret("password"), "tls"
	n.SNI = c.str("sni", "servername")
	n.ALPN = strings.Join(c.list("alpn"), ",")
	n.Insecure = c.flag("skip-cert-verify")
	var err error
	if n.PinSHA256, err = clashPin(c); err != nil {
		return err
	}
	if ports := c.str("ports"); ports != "" {
		p, err := firstPort(ports)
		if err != nil {
			return err
		}
		if n.Port == 0 {
			n.Port = p
		}
		n.HopPorts = ports
	}
	if iv := c.str("hop-interval"); iv != "" {
		sec, err := strconv.Atoi(strings.TrimSuffix(iv, "s"))
		if err != nil || sec <= 0 {
			return fmt.Errorf("跳跃间隔 %q 无效", iv)
		}
		n.HopInterval = sec
	}
	switch obfs := strings.ToLower(c.str("obfs")); obfs {
	case "", "none":
	case "salamander":
		n.Obfs, n.ObfsPassword = obfs, c.secret("obfs-password")
		if n.ObfsPassword == "" {
			return errors.New("salamander 混淆缺少密码 obfs-password")
		}
	default:
		return fmt.Errorf("Xray 不支持混淆 %q", obfs)
	}
	return nil
}

// clashWireGuard reads the simplified single-peer form, or the first of peers, which overrides it.
func clashWireGuard(n *Node, c clashMap) error {
	n.Protocol, n.SecretKey, n.MTU = WireGuard, c.secret("private-key"), c.num("mtu")
	var addrs []string
	for _, a := range []struct{ ip, bits string }{{c.str("ip"), "/32"}, {c.str("ipv6"), "/128"}} {
		if a.ip == "" {
			continue
		}
		if !strings.Contains(a.ip, "/") {
			a.ip += a.bits
		}
		addrs = append(addrs, a.ip)
	}
	n.LocalAddress = strings.Join(addrs, ",")
	peer := c
	if peers, _ := c["peers"].([]any); len(peers) > 0 {
		if p, ok := peers[0].(map[string]any); ok {
			peer = p
			n.Host, n.Port = peer.str("server"), peer.num("port")
		}
	}
	n.PublicKey, n.PreSharedKey = peer.secret("public-key"), peer.secret("pre-shared-key")
	if n.SecretKey == "" || n.PublicKey == "" {
		return errors.New("缺少本端私钥或对端公钥")
	}
	reserved, err := clashReserved(peer)
	if err != nil {
		return err
	}
	n.Reserved = reserved
	return nil
}

// clashReserved reads reserved as three bytes, [209,98,59], or their Base64 ("U4An").
func clashReserved(c clashMap) (string, error) {
	vals := c.list("reserved")
	switch {
	case len(vals) == 0:
		return "", nil
	case len(vals) == 1 && !strings.Contains(vals[0], ","):
		b, err := decodeBase64(vals[0])
		if err != nil || len(b) != 3 {
			return "", fmt.Errorf("reserved %q 无效", vals[0])
		}
		return fmt.Sprintf("%d,%d,%d", b[0], b[1], b[2]), nil
	}
	parts := strings.Split(strings.Join(vals, ","), ",")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if v, err := strconv.Atoi(p); err != nil || v < 0 || v > 255 || len(parts) != 3 {
			return "", fmt.Errorf("reserved %q 应为三个 0–255 的数", strings.Join(vals, ","))
		}
		parts[i] = p
	}
	return strings.Join(parts, ","), nil
}
