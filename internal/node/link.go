package node

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ShareLink returns a link only when Parse reads back the same connection settings.
// It returns "" for JSON nodes or settings the protocol's share format cannot express losslessly.
func ShareLink(n *Node) string {
	if n.Outbound != "" {
		return ""
	}
	canonical, err := normalizeFields(*n)
	if err != nil {
		return ""
	}
	link := shareLink(&canonical)
	back, err := Parse(link)
	if err != nil || !SameConfig(&canonical, &back) {
		return "" // never export a link that silently weakens or changes the configuration
	}
	return link
}

func shareLink(n *Node) string {
	if n.Outbound != "" {
		return ""
	}
	hostport := net.JoinHostPort(n.Host, strconv.Itoa(n.Port))
	name := ""
	if n.Name != "" {
		name = "#" + escape(n.Name)
	}
	switch n.Protocol {
	case VLESS:
		q := streamQuery(n)
		q.add("encryption", first(n.Encryption, "none"))
		q.add("flow", n.Flow)
		return "vless://" + escape(n.UUID) + "@" + hostport + q.String() + name
	case Trojan:
		return "trojan://" + escape(n.Password) + "@" + hostport + streamQuery(n).String() + name
	case Shadowsocks:
		user := base64.RawURLEncoding.EncodeToString([]byte(n.Cipher + ":" + n.Password))
		return "ss://" + user + "@" + hostport + extraStream(n).String() + name
	case VMess:
		return vmessLink(n)
	case Hysteria2:
		var q query
		q.add("sni", n.SNI)
		q.add("alpn", n.ALPN)
		if n.Insecure {
			q.add("insecure", "1")
		}
		q.add("pinSHA256", n.PinSHA256)
		q.add("obfs", n.Obfs)
		q.add("obfs-password", n.ObfsPassword)
		q.add("mport", n.HopPorts)
		if n.HopInterval > 0 {
			q.add("hop_interval", strconv.Itoa(n.HopInterval))
		}
		q.add("fm", n.FinalMask)
		return "hysteria2://" + escape(n.Password) + "@" + hostport + "/" + q.String() + name
	case SOCKS:
		user := ""
		if n.Username != "" || n.Password != "" {
			user = base64.RawURLEncoding.EncodeToString([]byte(n.Username+":"+n.Password)) + "@"
		}
		return "socks://" + user + hostport + extraStream(n).String() + name
	case HTTP:
		scheme := "http"
		var q query
		if n.Security == "tls" {
			scheme = "https"
			q.add("sni", n.SNI)
			if n.Insecure {
				q.add("insecure", "1")
			}
			q.add("pcs", n.PinSHA256)
		}
		user := ""
		if n.Username != "" || n.Password != "" {
			// The colon keeps the credentials from reading as Base64, which Parse tries first.
			user = escape(n.Username) + ":" + escape(n.Password) + "@"
		}
		return scheme + "://" + user + hostport + q.String() + name
	case WireGuard:
		var q query
		q.add("publickey", n.PublicKey)
		q.add("presharedkey", n.PreSharedKey)
		q.add("address", n.LocalAddress)
		q.add("reserved", n.Reserved)
		if n.MTU > 0 {
			q.add("mtu", strconv.Itoa(n.MTU))
		}
		return "wireguard://" + escape(n.SecretKey) + "@" + hostport + "/" + q.String() + name
	}
	return ""
}

// vmessLink writes v2rayN's Base64 JSON, reusing its fields as parseVMess reads them.
func vmessLink(n *Node) string {
	m := map[string]string{
		"v": "2", "ps": n.Name, "add": n.Host, "port": strconv.Itoa(n.Port), "id": n.UUID,
		"aid": strconv.Itoa(n.AlterID), "scy": n.Cipher, "net": first(n.Network, "tcp"), "type": n.HeaderType,
		"host": n.HostHeader, "path": n.Path, "sni": n.SNI, "alpn": n.ALPN, "fp": n.Fingerprint,
	}
	switch n.Network {
	case "grpc", "xhttp":
		m["type"] = n.Mode
	case "kcp":
		m["path"] = n.Seed
	}
	if n.Security == "tls" {
		m["tls"] = "tls"
	}
	if n.Insecure {
		m["allowInsecure"] = "1"
	}
	b, _ := json.Marshal(m)
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

// streamQuery writes the transport and security parameters applyStream reads (XTLS/Xray-core#716).
func streamQuery(n *Node) query {
	var q query
	q.add("type", n.Network)
	q.add("security", n.Security)
	q.add("sni", n.SNI)
	q.add("fp", n.Fingerprint)
	q.add("alpn", n.ALPN)
	if n.Insecure {
		q.add("allowInsecure", "1")
	}
	q.add("pbk", n.PublicKey)
	q.add("sid", n.ShortID)
	q.add("spx", n.SpiderX)
	q.add("pqv", n.MLDSA65)
	q.add("pcs", n.PinSHA256)
	q.add("vcn", n.VerifyName)
	q.add("ech", n.ECHConfig)
	q.add("host", n.HostHeader)
	q.add("headerType", n.HeaderType)
	q.add("fm", n.FinalMask)
	switch n.Network {
	case "grpc":
		q.add("serviceName", n.Path)
		q.add("mode", n.Mode)
		q.add("authority", n.Authority)
	case "xhttp":
		q.add("path", n.Path)
		q.add("mode", n.Mode)
		q.add("extra", n.Extra)
	case "kcp":
		q.add("seed", n.Seed)
	default:
		q.add("path", n.Path)
	}
	return q
}

// extraStream writes the transport parameters of protocols that usually go without (Shadowsocks and
// SOCKS, which panels such as 3x-ui may extend): none for plain TCP.
func extraStream(n *Node) query {
	if first(n.Network, "tcp") == "tcp" && first(n.Security, "none") == "none" && n.HeaderType == "" && n.FinalMask == "" {
		return nil
	}
	return streamQuery(n)
}

// query is a link's query in the order written; empty values are left out.
type query []string

func (q *query) add(key, value string) {
	if value != "" {
		*q = append(*q, key+"="+escape(value))
	}
}

func (q query) String() string {
	if len(q) == 0 {
		return ""
	}
	return "?" + strings.Join(q, "&")
}

// escape percent-encodes everything but unreserved characters, spaces included: Parse unescapes
// without turning "+" into a space.
func escape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
