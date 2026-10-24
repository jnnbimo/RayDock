package node

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Parse parses one share link: ss, vmess, vless, trojan, hysteria2 (hy2), socks (socks5), http, https
// and wireguard (wg). VLESS/VMess/Trojan follow the Xray share link standard (XTLS/Xray-core#716);
// socks, http and wireguard have no standard and follow v2rayN.
func Parse(link string) (Node, error) {
	link = strings.TrimSpace(link)
	scheme, _, ok := strings.Cut(link, "://")
	if !ok {
		return Node{}, errors.New("不是分享链接")
	}
	scheme = strings.ToLower(scheme)
	var (
		n   Node
		err error
	)
	switch scheme {
	case "ss":
		n, err = parseSS(link)
	case "vmess":
		n, err = parseVMess(link)
	case "vless":
		n, err = parseVLESS(link)
	case "trojan":
		n, err = parseTrojan(link)
	case "hysteria2", "hy2":
		n, err = parseHysteria2(link)
	case "socks", "socks5", "socks5h":
		n, err = parseSOCKS(link)
	case "http", "https":
		n, err = parseHTTP(link, scheme == "https")
	case "wireguard", "wg":
		n, err = parseWireGuard(link)
	default:
		return Node{}, fmt.Errorf("不支持的协议 %s", scheme)
	}
	if err != nil {
		return Node{}, fmt.Errorf("%s 链接无效：%w", scheme, err)
	}
	if n, err = normalizeFields(n); err != nil {
		return Node{}, fmt.Errorf("%s 链接无效：%w", scheme, err)
	}
	n.ID = NewID()
	n.Link = link
	return n, nil
}

// ParseMany parses pasted or subscribed text: a Clash / Mihomo config (YAML with proxies), Xray JSON
// when it starts with { or [, otherwise one share link per line. It returns the nodes and one error per
// failed entry.
func ParseMany(text string) (nodes []Node, errs []error) {
	if isClash(text) {
		return ParseClash(text)
	}
	if isJSON(text) {
		return ParseJSON(text)
	}
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n, err := Parse(line)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, errs
}

func isJSON(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

// DecodeSubscription turns a subscription body (Base64, plain links, Xray JSON or a Clash / Mihomo
// config) into text for ParseMany.
func DecodeSubscription(body []byte) (string, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "", errors.New("订阅内容为空")
	}
	if strings.Contains(text, "://") || isJSON(text) || isClash(text) {
		return text, nil
	}
	decoded, err := decodeBase64(strings.Join(strings.Fields(text), ""))
	if err != nil {
		return "", errors.New("订阅内容既不是分享链接、Xray JSON、Clash 配置，也不是 Base64 编码")
	}
	if !strings.Contains(decoded, "://") {
		return "", errors.New("订阅解码后没有分享链接")
	}
	return decoded, nil
}

// splitLink splits scheme://userinfo@host:port/path?query#fragment by the last @, since Base64
// credentials may contain / + = that confuse url.Parse.
func splitLink(link string) (userinfo, hostport, path string, q url.Values, name string) {
	_, rest, _ := strings.Cut(link, "://")
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		name = unescape(rest[i+1:])
		rest = rest[:i]
	}
	q = url.Values{}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		q = parseQuery(rest[i+1:])
		rest = rest[:i]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		userinfo, rest = rest[:at], rest[at+1:]
	}
	hostport = rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostport, path = rest[:i], rest[i:]
	}
	return
}

// credentials parses user:pass, plain or Base64-encoded.
func credentials(userinfo string) (user, pass string) {
	if dec, err := decodeBase64(userinfo); err == nil && strings.Contains(dec, ":") {
		userinfo = dec
	} else {
		userinfo = unescape(userinfo)
	}
	user, pass, _ = strings.Cut(userinfo, ":")
	return
}

func parseSS(link string) (Node, error) {
	userinfo, hostport, path, q, name := splitLink(link)
	if q.Get("plugin") != "" {
		return Node{}, errors.New("Xray 不支持 SIP003 插件")
	}
	n := Node{Protocol: Shadowsocks, Name: name}
	if userinfo == "" {
		// Legacy form: Base64(method:password@host:port), which may contain /.
		dec, err := decodeBase64(strings.TrimSuffix(hostport+path, "/"))
		if err != nil {
			return Node{}, errors.New("无法解码")
		}
		at := strings.LastIndexByte(dec, '@')
		if at < 0 {
			return Node{}, errors.New("缺少服务器地址")
		}
		userinfo, hostport = dec[:at], dec[at+1:]
	}
	method, password := credentials(userinfo)
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return Node{}, err
	}
	n.Cipher, n.Password, n.Host, n.Port = method, password, host, port
	// Panels such as 3x-ui append Xray transport parameters.
	return n, applyStream(&n, q, "none")
}

func parseVMess(link string) (Node, error) {
	raw, err := decodeBase64(strings.TrimSpace(link[len("vmess://"):]))
	if err != nil {
		return Node{}, errors.New("无法解码")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Node{}, errors.New("内容不是 JSON")
	}
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return strings.TrimSpace(v)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		return ""
	}
	num := func(k string) int { v, _ := strconv.Atoi(str(k)); return v }
	n := Node{
		Protocol:    VMess,
		Name:        str("ps"),
		Host:        str("add"),
		Port:        num("port"),
		UUID:        str("id"),
		AlterID:     num("aid"),
		Cipher:      str("scy"),
		Path:        str("path"),
		HostHeader:  str("host"),
		HeaderType:  str("type"),
		SNI:         str("sni"),
		Fingerprint: str("fp"),
		ALPN:        str("alpn"),
		Insecure:    truthy(str("allowInsecure")) || truthy(str("insecure")),
	}
	if id, ok := m["id"].(string); ok {
		n.UUID = id
	} // authentication IDs are opaque
	if n.Network, err = network(str("net")); err != nil {
		return Node{}, err
	}
	if strings.EqualFold(str("tls"), "tls") {
		n.Security = "tls"
	}
	// v2rayN reuses fields: type is gun/multi for grpc and the mode for xhttp, path is the mKCP seed.
	switch n.Network {
	case "grpc":
		n.Mode, n.HeaderType = str("type"), ""
	case "kcp":
		n.Seed, n.Path = n.Path, ""
	case "xhttp":
		n.Mode, n.HeaderType = str("type"), ""
	}
	return n, nil
}

func parseVLESS(link string) (Node, error) {
	u, q, err := parseURL(link)
	if err != nil {
		return Node{}, err
	}
	n := Node{Protocol: VLESS, UUID: u.User.Username(), Name: u.Fragment}
	if n.Host, n.Port, err = urlHostPort(u, 0); err != nil {
		return Node{}, err
	}
	n.Flow = q.Get("flow")
	if enc := q.Get("encryption"); enc != "none" {
		n.Encryption = enc
	}
	return n, applyStream(&n, q, "none")
}

func parseTrojan(link string) (Node, error) {
	u, q, err := parseURL(link)
	if err != nil {
		return Node{}, err
	}
	n := Node{Protocol: Trojan, Password: userSecret(u), Name: u.Fragment}
	if n.Host, n.Port, err = urlHostPort(u, 443); err != nil {
		return Node{}, err
	}
	return n, applyStream(&n, q, "tls")
}

// parseHysteria2 follows the official Hysteria 2 URI, whose port may be a hop range
// (443,20000-30000), and accepts v2rayN's mport.
func parseHysteria2(link string) (Node, error) {
	userinfo, hostport, _, q, name := splitLink(link)
	n := Node{
		Protocol:  Hysteria2,
		Password:  unescape(userinfo),
		Name:      name,
		Security:  "tls",
		SNI:       first(q.Get("sni"), q.Get("peer")),
		ALPN:      q.Get("alpn"),
		Insecure:  truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")),
		PinSHA256: first(q.Get("pinSHA256"), q.Get("pcs"), q.Get("pinnedPeerCertSha256")),
		FinalMask: q.Get("fm"),
	}
	host, ports, err := splitHostPorts(hostport)
	if err != nil {
		return Node{}, err
	}
	n.Host = host
	if n.Port, err = firstPort(ports); err != nil {
		return Node{}, err
	}
	if strings.ContainsAny(ports, ",-") {
		n.HopPorts = ports
	}
	if mport := q.Get("mport"); mport != "" {
		if _, err := firstPort(mport); err != nil {
			return Node{}, fmt.Errorf("mport %w", err)
		}
		n.HopPorts = mport
	}
	if iv := first(q.Get("hop_interval"), q.Get("hopInterval")); iv != "" {
		sec, err := strconv.Atoi(strings.TrimSuffix(iv, "s"))
		if err != nil || sec <= 0 {
			return Node{}, fmt.Errorf("跳跃间隔 %q 无效", iv)
		}
		n.HopInterval = sec
	}
	n.Obfs = strings.ToLower(q.Get("obfs"))
	n.ObfsPassword = first(q.Get("obfs-password"), q.Get("obfs_password"), q.Get("obfsPassword"))
	if n.Port == 0 {
		n.Port = 443
	}
	return n, nil
}

func parseSOCKS(link string) (Node, error) {
	userinfo, hostport, path, q, name := splitLink(link)
	n := Node{Protocol: SOCKS, Name: name}
	if userinfo == "" && !strings.Contains(hostport, ":") {
		// Legacy form: Base64(user:pass@host:port), which may contain /.
		if dec, err := decodeBase64(strings.TrimSuffix(hostport+path, "/")); err == nil {
			if at := strings.LastIndexByte(dec, '@'); at >= 0 {
				userinfo, hostport = dec[:at], dec[at+1:]
			} else {
				hostport = dec
			}
		}
	}
	if userinfo != "" {
		n.Username, n.Password = credentials(userinfo)
	}
	var err error
	if n.Host, n.Port, err = splitHostPort(hostport); err != nil {
		return Node{}, err
	}
	return n, applyStream(&n, q, "none")
}

// parseHTTP reads an HTTP proxy link (https:// reaches the proxy over TLS, whose parameters it may
// carry as other links do: sni, insecure, pcs). To tell it apart from a subscription URL it needs an
// explicit port, no path, and no other query.
func parseHTTP(link string, tls bool) (Node, error) {
	userinfo, hostport, path, q, name := splitLink(link)
	tlsOnly := true
	for k := range q {
		switch k {
		case "sni", "insecure", "allowInsecure", "pcs", "pinSHA256":
			tlsOnly = tlsOnly && tls
		default:
			tlsOnly = false
		}
	}
	if (path != "" && path != "/") || !tlsOnly || !strings.Contains(strings.TrimPrefix(hostport, "["), ":") {
		return Node{}, errors.New("HTTP 代理链接应为 http(s)://[用户名:密码@]主机:端口；订阅地址请通过“添加分组”添加为订阅分组")
	}
	n := Node{Protocol: HTTP, Name: name, Network: "tcp", Security: "none"}
	if tls {
		n.Security = "tls"
		n.SNI = q.Get("sni")
		n.Insecure = truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure"))
		n.PinSHA256 = first(q.Get("pcs"), q.Get("pinSHA256"))
	}
	if userinfo != "" {
		n.Username, n.Password = credentials(userinfo)
	}
	var err error
	n.Host, n.Port, err = splitHostPort(hostport)
	return n, err
}

// parseWireGuard reads v2rayN's wireguard://key@host:port?publickey=…&address=…&reserved=…&mtu=…
func parseWireGuard(link string) (Node, error) {
	userinfo, hostport, _, q, name := splitLink(link)
	n := Node{
		Protocol:     WireGuard,
		Name:         name,
		SecretKey:    unescape(userinfo),
		PublicKey:    first(q.Get("publickey"), q.Get("publicKey"), q.Get("peer_public_key"), q.Get("public_key")),
		PreSharedKey: first(q.Get("presharedkey"), q.Get("preSharedKey"), q.Get("pre_shared_key"), q.Get("psk")),
		LocalAddress: strings.ReplaceAll(first(q.Get("address"), q.Get("ip")), " ", ""),
		Reserved:     strings.ReplaceAll(q.Get("reserved"), " ", ""),
	}
	if n.SecretKey == "" {
		n.SecretKey = first(q.Get("privatekey"), q.Get("private_key"), q.Get("secretKey"))
	}
	if mtu := q.Get("mtu"); mtu != "" {
		v, err := strconv.Atoi(mtu)
		if err != nil || v < 576 || v > 65535 {
			return Node{}, fmt.Errorf("MTU %q 无效", mtu)
		}
		n.MTU = v
	}
	var err error
	n.Host, n.Port, err = splitHostPort(hostport)
	return n, err
}

// applyStream reads transport and security parameters (XTLS/Xray-core#716).
func applyStream(n *Node, q url.Values, defaultSecurity string) error {
	var err error
	if n.Network, err = network(first(q.Get("type"), q.Get("network"))); err != nil {
		return err
	}
	n.Security = strings.ToLower(first(q.Get("security"), defaultSecurity))
	n.SNI = first(q.Get("sni"), q.Get("servername"), q.Get("peer"))
	n.Fingerprint = q.Get("fp")
	n.ALPN = q.Get("alpn")
	n.Insecure = truthy(q.Get("allowInsecure")) || truthy(q.Get("insecure"))
	n.PublicKey, n.ShortID, n.SpiderX, n.MLDSA65 = q.Get("pbk"), q.Get("sid"), q.Get("spx"), q.Get("pqv")
	n.PinSHA256 = first(q.Get("pcs"), q.Get("pinSHA256"))
	n.VerifyName, n.ECHConfig = q.Get("vcn"), q.Get("ech")
	n.HostHeader = q.Get("host")
	n.HeaderType = q.Get("headerType")
	n.FinalMask = q.Get("fm")
	switch n.Network {
	case "grpc":
		n.Path = first(q.Get("serviceName"), q.Get("path"))
		n.Mode = q.Get("mode")
		n.Authority = q.Get("authority")
	case "xhttp":
		n.Path = q.Get("path")
		n.Mode = q.Get("mode")
		n.Extra = q.Get("extra")
	case "kcp":
		n.Seed = q.Get("seed")
	default:
		n.Path = q.Get("path")
	}
	return nil
}

// network normalizes transport names; HTTP/2 and QUIC, removed in Xray 26, are errors.
func network(s string) (string, error) {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "", "tcp", "raw", "original":
		return "tcp", nil
	case "ws", "websocket":
		return "ws", nil
	case "xhttp", "splithttp":
		return "xhttp", nil
	case "kcp", "mkcp":
		return "kcp", nil
	case "grpc", "httpupgrade":
		return s, nil
	case "h2", "h3", "http", "quic":
		return "", fmt.Errorf("Xray 已移除 %s 传输，请让服务端改用 XHTTP", s)
	}
	return "", fmt.Errorf("不支持的传输方式 %q", s)
}

// validJSON checks that a parameter such as fm or extra is a JSON object; empty means unset.
func validJSON(name, s string) error {
	if s == "" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil || m == nil {
		return fmt.Errorf("参数 %s 不是 JSON 对象", name)
	}
	return nil
}

func parseURL(link string) (*url.URL, url.Values, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, nil, errors.New("地址格式错误")
	}
	return u, parseQuery(u.RawQuery), nil
}

// parseQuery splits a query without turning "+" into spaces, since keys in share links often contain "+".
func parseQuery(raw string) url.Values {
	q := url.Values{}
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		q.Set(unescape(k), unescape(v))
	}
	return q
}

func urlHostPort(u *url.URL, defaultPort int) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, errors.New("缺少服务器地址")
	}
	if u.Port() == "" {
		if defaultPort == 0 {
			return "", 0, errors.New("缺少端口")
		}
		return host, defaultPort, nil
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return "", 0, errors.New("端口无效")
	}
	return host, port, nil
}

// userSecret returns everything before @, as trojan and hysteria2 passwords may contain colons.
func userSecret(u *url.URL) string {
	if u.User == nil {
		return ""
	}
	s := u.User.Username()
	if p, ok := u.User.Password(); ok {
		s += ":" + p
	}
	return s
}

func splitHostPort(s string) (string, int, error) {
	host, p, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil || host == "" {
		return "", 0, errors.New("服务器地址格式错误")
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, errors.New("端口无效")
	}
	return host, port, nil
}

// splitHostPorts splits host:ports, where ports may be a list of ports and ranges, or empty.
func splitHostPorts(s string) (host, ports string, err error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", "", errors.New("服务器地址格式错误")
		}
		host, s = s[1:end], s[end+1:]
		ports = strings.TrimPrefix(s, ":")
	} else {
		host, ports, _ = strings.Cut(s, ":")
	}
	if host == "" {
		return "", "", errors.New("缺少服务器地址")
	}
	return host, ports, nil
}

// firstPort validates a port list (443,20000-30000) and returns its first port, or 0 when empty.
func firstPort(list string) (int, error) {
	if list == "" {
		return 0, nil
	}
	firstPort := 0
	for part := range strings.SplitSeq(list, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err1 := strconv.Atoi(lo)
		b, err2 := a, error(nil)
		if isRange {
			b, err2 = strconv.Atoi(hi)
		}
		if err1 != nil || err2 != nil || a <= 0 || b > 65535 || a > b {
			return 0, fmt.Errorf("端口 %q 无效", part)
		}
		if firstPort == 0 {
			firstPort = a
		}
	}
	return firstPort, nil
}

// decodeBase64 accepts standard and URL alphabets, with or without padding.
func decodeBase64(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("-", "+", "_", "/").Replace(s)
	s = strings.TrimRight(s, "=")
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unescape(s string) string {
	if v, err := url.PathUnescape(s); err == nil {
		return v
	}
	return s
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
