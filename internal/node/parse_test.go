package node

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestParseVLESSReality(t *testing.T) {
	link := "vless://0b5a8f3e-1111-2222-3333-444455556666@example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.apple.com&fp=chrome&pbk=AbC+def/123&sid=6ba8&type=tcp#%E9%A6%99%E6%B8%AF%2001"
	n, err := Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	want := Node{Protocol: VLESS, Host: "example.com", Port: 443, UUID: "0b5a8f3e-1111-2222-3333-444455556666",
		Flow: "xtls-rprx-vision", Security: "reality", SNI: "www.apple.com", Fingerprint: "chrome",
		PublicKey: "AbC+def/123", ShortID: "6ba8", Network: "tcp", Name: "香港 01"}
	check(t, n, want)
	if n.Encryption != "" || n.Link != link || n.ID == "" {
		t.Errorf("encryption %q link %q id %q", n.Encryption, n.Link, n.ID)
	}
}

func TestParseVLESSWebSocket(t *testing.T) {
	n, err := Parse("vless://uuid-1@[2001:db8::1]:8443?type=ws&security=tls&path=%2Fray%3Fed%3D2048&host=cdn.example.com&alpn=h2%2Chttp%2F1.1")
	if err != nil {
		t.Fatal(err)
	}
	check(t, n, Node{Protocol: VLESS, Host: "2001:db8::1", Port: 8443, UUID: "uuid-1", Network: "ws",
		Security: "tls", Path: "/ray?ed=2048", HostHeader: "cdn.example.com", ALPN: "h2,http/1.1"})
	if n.Address() != "[2001:db8::1]:8443" {
		t.Errorf("address %q", n.Address())
	}
}

func TestParseVMess(t *testing.T) {
	js := `{"v":"2","ps":"日本 02","add":"jp.example.com","port":"443","id":"uuid-2","aid":"0","scy":"auto","net":"ws","type":"none","host":"jp.example.com","path":"/v","tls":"tls","sni":"jp.example.com","fp":"firefox"}`
	n, err := Parse("vmess://" + base64.StdEncoding.EncodeToString([]byte(js)))
	if err != nil {
		t.Fatal(err)
	}
	check(t, n, Node{Protocol: VMess, Name: "日本 02", Host: "jp.example.com", Port: 443, UUID: "uuid-2",
		Cipher: "auto", Network: "ws", HeaderType: "none", HostHeader: "jp.example.com", Path: "/v",
		Security: "tls", SNI: "jp.example.com", Fingerprint: "firefox"})

	// Numeric port, unpadded URL-safe Base64, grpc multi given as type.
	js = `{"ps":"g","add":"1.2.3.4","port":8080,"id":"u","net":"grpc","type":"multi","path":"svc"}`
	n, err = Parse("vmess://" + base64.RawURLEncoding.EncodeToString([]byte(js)))
	if err != nil {
		t.Fatal(err)
	}
	if n.Port != 8080 || n.Network != "grpc" || n.Mode != "multi" || n.HeaderType != "" || n.Path != "svc" {
		t.Errorf("grpc vmess %+v", n)
	}
}

func TestParseShadowsocks(t *testing.T) {
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:p@ss:word"))
	for name, link := range map[string]string{
		"SIP002 Base64": "ss://" + ui + "@1.2.3.4:8388#SS%20A",
		"SIP002 明文":     "ss://aes-256-gcm:p%40ss%3Aword@1.2.3.4:8388/#SS%20A",
		"旧格式":           "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:p@ss:word@1.2.3.4:8388")) + "#SS%20A",
	} {
		n, err := Parse(link)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		check(t, n, Node{Protocol: Shadowsocks, Cipher: "aes-256-gcm", Password: "p@ss:word", Host: "1.2.3.4",
			Port: 8388, Network: "tcp", Security: "none", Name: "SS A"})
	}
	if _, err := Parse("ss://" + ui + "@1.2.3.4:8388?plugin=obfs-local%3Bobfs%3Dhttp"); err == nil {
		t.Error("links with a SIP003 plugin should be rejected")
	}
}

func TestParseTrojanAndHysteria2(t *testing.T) {
	n, err := Parse("trojan://pa%3Ass@t.example.com?sni=s.example.com&allowInsecure=1#T")
	if err != nil {
		t.Fatal(err)
	}
	check(t, n, Node{Protocol: Trojan, Password: "pa:ss", Host: "t.example.com", Port: 443, Security: "tls",
		SNI: "s.example.com", Insecure: true, Network: "tcp", Name: "T"})

	n, err = Parse("hy2://secret@h.example.com:8443/?sni=h.example.com&insecure=1&pinSHA256=AA:BB#H")
	if err != nil {
		t.Fatal(err)
	}
	check(t, n, Node{Protocol: Hysteria2, Password: "secret", Host: "h.example.com", Port: 8443, Security: "tls",
		SNI: "h.example.com", Insecure: true, PinSHA256: "AA:BB", Name: "H"})

	// Hop range in the port position (official form) and salamander obfuscation.
	n, err = Parse("hysteria2://a:b@[2001:db8::1]:443,20000-30000/?obfs=salamander&obfs-password=gawr&hop_interval=45s#J")
	if err != nil {
		t.Fatal(err)
	}
	check(t, n, Node{Protocol: Hysteria2, Password: "a:b", Host: "2001:db8::1", Port: 443, Security: "tls",
		HopPorts: "443,20000-30000", HopInterval: 45, Obfs: "salamander", ObfsPassword: "gawr", Name: "J"})
	// v2rayN mport; the port defaults to 443.
	n, err = Parse("hy2://s@h.example.com?mport=20000-30000")
	if err != nil || n.Port != 443 || n.HopPorts != "20000-30000" {
		t.Fatalf("%+v %v", n, err)
	}
	for _, bad := range []string{
		"hysteria2://s@h.example.com:443?obfs=gecko&obfs-password=x",
		"hysteria2://s@h.example.com:443?obfs=salamander",
		"hysteria2://s@h.example.com:30000-20000",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

// Newer parameters of the Xray share link standard: mKCP, fm, XHTTP extra, ECH, pcs/vcn, pqv, gRPC authority.
func TestParseStreamParams(t *testing.T) {
	n, err := Parse("vless://u@k.example.com:9000?type=mkcp&headerType=wechat-video&seed=s3cr3t#K")
	if err != nil {
		t.Fatal(err)
	}
	if n.Network != "kcp" || n.HeaderType != "wechat-video" || n.Seed != "s3cr3t" || !n.UDP() {
		t.Errorf("mKCP %+v", n)
	}
	fm := `{"udp":[{"type":"header-dtls"}]}`
	n, err = Parse("vless://u@x.example.com:443?type=splithttp&security=tls&path=%2Fx&mode=stream-up&extra=" +
		url.QueryEscape(`{"xPaddingBytes":"100-1000"}`) + "&fm=" + url.QueryEscape(fm) +
		"&ech=AEn%2B&pcs=AB%3ACD&vcn=real.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if n.Network != "xhttp" || n.Mode != "stream-up" || n.Extra != `{"xPaddingBytes":"100-1000"}` || n.FinalMask != fm ||
		n.ECHConfig != "AEn+" || n.PinSHA256 != "AB:CD" || n.VerifyName != "real.example.com" {
		t.Errorf("xhttp %+v", n)
	}
	n, err = Parse("vless://u@g.example.com:443?type=grpc&serviceName=svc&authority=a.example.com&security=reality&pbk=k&pqv=PQ")
	if err != nil || n.Path != "svc" || n.Authority != "a.example.com" || n.MLDSA65 != "PQ" {
		t.Errorf("grpc %+v %v", n, err)
	}
	js := `{"add":"k.example.com","port":"9000","id":"u","net":"kcp","type":"srtp","path":"seed1"}`
	n, err = Parse("vmess://" + base64.StdEncoding.EncodeToString([]byte(js)))
	if err != nil || n.Seed != "seed1" || n.Path != "" || n.HeaderType != "srtp" {
		t.Errorf("vmess mKCP %+v %v", n, err)
	}
	for _, bad := range []string{
		"vless://u@h:443?type=h2",
		"vless://u@h:443?type=quic",
		"vless://u@h:443?type=xhttp&extra=notjson",
		"trojan://p@h:443?fm=%7B",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestParseSOCKSAndHTTP(t *testing.T) {
	cred := base64.StdEncoding.EncodeToString([]byte("user:pa/ss"))
	for link, want := range map[string]Node{
		"socks://" + cred + "@1.2.3.4:1080#S": {Protocol: SOCKS, Username: "user", Password: "pa/ss", Host: "1.2.3.4", Port: 1080,
			Network: "tcp", Security: "none", Name: "S"},
		"socks5://u%40x:p@[::1]:1080": {Protocol: SOCKS, Username: "u@x", Password: "p", Host: "::1", Port: 1080,
			Network: "tcp", Security: "none"},
		"socks://" + base64.StdEncoding.EncodeToString([]byte("1.2.3.4:1080")): {Protocol: SOCKS, Host: "1.2.3.4", Port: 1080,
			Network: "tcp", Security: "none"},
		"http://u:p@proxy.example.com:3128/#H": {Protocol: HTTP, Username: "u", Password: "p", Host: "proxy.example.com",
			Port: 3128, Network: "tcp", Security: "none", Name: "H"},
		"https://proxy.example.com:443": {Protocol: HTTP, Host: "proxy.example.com", Port: 443, Network: "tcp", Security: "tls"},
	} {
		n, err := Parse(link)
		if err != nil {
			t.Errorf("%s: %v", link, err)
			continue
		}
		check(t, n, want)
	}
	// Subscription URLs must not be taken for HTTP proxies.
	for _, link := range []string{"https://sub.example.com/api/v1/subscribe?token=x", "https://sub.example.com", "http://a.com:80/sub"} {
		if _, err := Parse(link); err == nil {
			t.Errorf("%q taken for an HTTP proxy", link)
		}
	}
}

func TestParseWireGuard(t *testing.T) {
	n, err := Parse("wireguard://cGsrL2s%3D@162.159.192.1:2408/?publickey=bmo%2BcQ%3D%3D&address=172.16.0.2%2F32%2C%202606%3A4700%3A%3A2%2F128&reserved=1%2C2%2C3&mtu=1280#WARP")
	if err != nil {
		t.Fatal(err)
	}
	check(t, n, Node{Protocol: WireGuard, SecretKey: "cGsrL2s=", PublicKey: "bmo+cQ==", Host: "162.159.192.1", Port: 2408,
		LocalAddress: "172.16.0.2/32,2606:4700::2/128", Reserved: "1,2,3", MTU: 1280, Name: "WARP"})
	if !n.UDP() || n.Transport() != "udp" {
		t.Errorf("UDP %v transport %q", n.UDP(), n.Transport())
	}
	for _, bad := range []string{
		"wireguard://k@h:51820",                       // no peer public key
		"wg://k@h:51820?publickey=p&reserved=1,2",     // reserved is not 3 bytes
		"wg://k@h:51820?publickey=p&reserved=1,2,300", // out of byte range
		"wg://k@h:51820?publickey=p&mtu=abc",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestParseJSON(t *testing.T) {
	// A list of full configs (v2rayN custom subscription): only proxy outbounds count.
	text := `[
	  {"remarks": "MQ", "outbounds": [
	    {"tag": "proxy", "protocol": "masque", "settings": {"address": "m.example.com", "port": 443},
	     "streamSettings": {"network": "masque", "security": "tls", "tlsSettings": {"alpn": ["h3"]}}},
	    {"tag": "direct", "protocol": "freedom"}]},
	  {"protocol": "wireguard", "tag": "wg", "settings": {"secretKey": "k", "peers": [{"publicKey": "p", "endpoint": "[2001:db8::2]:51820"}]}},
	  {"protocol": "vless", "settings": {"vnext": [{"address": "v.example.com", "port": "8443", "users": [{"id": "u"}]}]},
	   "streamSettings": {"network": "raw", "security": "reality"}},
	  {"protocol": "tuic", "settings": {}},
	  {"hello": 1}
	]`
	nodes, errs := ParseMany(text)
	if len(nodes) != 3 || len(errs) != 2 {
		t.Fatalf("%d nodes %v", len(nodes), errs)
	}
	m, w, v := nodes[0], nodes[1], nodes[2]
	if m.Protocol != MASQUE || m.Name != "MQ" || m.Host != "m.example.com" || m.Port != 443 || m.Transport() != "masque · tls" || !m.UDP() {
		t.Errorf("masque %+v", m)
	}
	if strings.Contains(m.Outbound, `"tag"`) || !strings.Contains(m.Outbound, `"masque"`) {
		t.Errorf("outbound %s", m.Outbound)
	}
	if w.Protocol != WireGuard || w.Name != "wg" || w.Host != "2001:db8::2" || w.Port != 51820 {
		t.Errorf("wireguard %+v", w)
	}
	if v.Protocol != VLESS || v.Port != 8443 || v.Transport() != "tcp · reality" || v.ID == "" || v.ID == m.ID {
		t.Errorf("vless %+v", v)
	}
	// A JSON subscription body is passed to ParseMany as is.
	if got, err := DecodeSubscription([]byte("  " + text)); err != nil || got != strings.TrimSpace(text) {
		t.Errorf("JSON subscription %v", err)
	}
	if _, errs := ParseMany("{bad json"); len(errs) != 1 {
		t.Errorf("invalid JSON should fail: %v", errs)
	}
}

func TestParseErrors(t *testing.T) {
	for _, link := range []string{"", "hello", "http://example.com", "vless://@host:443", "vless://u@host", "vmess://!!!", "trojan://@h:1"} {
		if _, err := Parse(link); err == nil {
			t.Errorf("%q should fail", link)
		}
	}
}

func TestSubscription(t *testing.T) {
	links := "vless://u@a.example.com:443?security=tls#A\r\ntrojan://p@b.example.com:443#B\nbad-line\n\n"
	for name, body := range map[string]string{
		"明文":     links,
		"Base64": base64.StdEncoding.EncodeToString([]byte(links)),
		"折行":     strings.Join(chunks(base64.StdEncoding.EncodeToString([]byte(links)), 20), "\n"),
	} {
		text, err := DecodeSubscription([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		nodes, errs := ParseMany(text)
		if len(nodes) != 2 || len(errs) != 1 || nodes[0].Name != "A" || nodes[1].Name != "B" {
			t.Errorf("%s: %d nodes, %d errors", name, len(nodes), len(errs))
		}
	}
	if _, err := DecodeSubscription([]byte("   ")); err == nil {
		t.Error("empty subscription should fail")
	}
	if _, err := DecodeSubscription([]byte("<html>not found</html>")); err == nil {
		t.Error("non-subscription content should fail")
	}
}

func check(t *testing.T, got, want Node) {
	t.Helper()
	got.ID, got.Link = "", ""
	if got != want {
		t.Errorf("\n got %+v\nwant %+v", got, want)
	}
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}
