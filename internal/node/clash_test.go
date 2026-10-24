package node

import (
	"strings"
	"testing"
)

const clashConfig = `
mixed-port: 7890
proxies:
  - name: "🇺🇸 US|68|http"
    type: http
    server: 203.0.113.68
    port: 443
    tls: true
    skip-cert-verify: true
    udp: true
  - {name: auth-http, type: http, server: h.example.com, port: 8443, username: u, password: "p@ss", tls: true, sni: proxy.example.com}
  - {name: plain-http, type: http, server: 10.0.0.1, port: 8080}
  - name: reality
    type: vless
    server: r.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    flow: xtls-rprx-vision
    network: tcp
    tls: true
    servername: www.amazon.com
    client-fingerprint: chrome
    reality-opts:
      public-key: hDRm-rHVu-ClNhBhPfp-JUGtFM91NGjlhVf-JjnPCnI
      short-id: 0123
  - name: vless-ws
    type: vless
    server: w.example.com
    port: 443
    uuid: u-ws
    tls: true
    skip-cert-verify: true
    network: ws
    ws-opts: {path: /ray, headers: {host: cdn.example.com}}
  - {name: vless-xhttp, type: vless, server: x.example.com, port: 443, uuid: u-x, tls: true, network: xhttp,
     alpn: [h2, http/1.1], xhttp-opts: {path: /x, mode: stream-up, host: x.cdn.com}}
  - {name: trojan-grpc, type: trojan, server: t.example.com, port: 443, password: "123456", sni: t.example.com,
     network: grpc, grpc-opts: {grpc-service-name: svc}}
  - {name: vmess-http, type: vmess, server: v.example.com, port: 80, uuid: u-v, alterId: 0, cipher: auto,
     network: http, http-opts: {method: GET, path: [/a, /b], headers: {Host: [v.cdn.com]}}}
  - {name: ss, type: ss, server: s.example.com, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: ss-obfs, type: ss, server: s.example.com, port: 8389, cipher: aes-128-gcm, password: pw, plugin: obfs}
  - {name: hy2, type: hysteria2, server: hy.example.com, ports: "20000-30000,443", hop-interval: 30s, password: pw,
     obfs: salamander, obfs-password: ob, sni: hy.example.com, skip-cert-verify: true}
  - {name: socks-tls, type: socks5, server: k.example.com, port: 1080, username: a, password: b, tls: true, skip-cert-verify: true}
  - name: wg
    type: wireguard
    private-key: c2VjcmV0
    ip: 172.16.0.2
    ipv6: fd01::2
    mtu: 1280
    peers:
      - {server: 162.159.192.1, port: 2408, public-key: cHVi, reserved: U4An}
  - {name: tuic, type: tuic, server: q.example.com, port: 443}
rules:
  - MATCH,DIRECT
`

func TestParseClash(t *testing.T) {
	text, err := DecodeSubscription([]byte(clashConfig))
	if err != nil {
		t.Fatal(err)
	}
	nodes, errs := ParseMany(text)
	byName := map[string]Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	if len(errs) != 2 || !strings.Contains(errs[0].Error(), "ss-obfs") || !strings.Contains(errs[1].Error(), "tuic") {
		t.Fatalf("errors %v", errs)
	}
	checks := []struct {
		name string
		want Node
	}{
		// What a link format of the same subscription drops: TLS, and a certificate to accept.
		{"🇺🇸 US|68|http", Node{Protocol: HTTP, Host: "203.0.113.68", Port: 443, Network: "tcp", Security: "tls", Insecure: true}},
		{"auth-http", Node{Protocol: HTTP, Host: "h.example.com", Port: 8443, Network: "tcp", Security: "tls", Username: "u", Password: "p@ss", SNI: "proxy.example.com"}},
		{"plain-http", Node{Protocol: HTTP, Host: "10.0.0.1", Port: 8080, Network: "tcp", Security: "none"}},
		{"reality", Node{Protocol: VLESS, Host: "r.example.com", Port: 443, UUID: "11111111-2222-3333-4444-555555555555",
			Flow: "xtls-rprx-vision", Network: "tcp", Security: "reality", SNI: "www.amazon.com", Fingerprint: "chrome",
			PublicKey: "hDRm-rHVu-ClNhBhPfp-JUGtFM91NGjlhVf-JjnPCnI", ShortID: "0123"}},
		{"vless-ws", Node{Protocol: VLESS, Host: "w.example.com", Port: 443, UUID: "u-ws", Network: "ws", Path: "/ray",
			HostHeader: "cdn.example.com", Security: "tls", Insecure: true}},
		{"vless-xhttp", Node{Protocol: VLESS, Host: "x.example.com", Port: 443, UUID: "u-x", Network: "xhttp", Path: "/x",
			Mode: "stream-up", HostHeader: "x.cdn.com", Security: "tls", ALPN: "h2,http/1.1"}},
		{"trojan-grpc", Node{Protocol: Trojan, Host: "t.example.com", Port: 443, Password: "123456", Network: "grpc", Path: "svc",
			Security: "tls", SNI: "t.example.com"}},
		{"vmess-http", Node{Protocol: VMess, Host: "v.example.com", Port: 80, UUID: "u-v", Cipher: "auto", Network: "tcp",
			HeaderType: "http", Path: "/a", HostHeader: "v.cdn.com"}},
		{"ss", Node{Protocol: Shadowsocks, Host: "s.example.com", Port: 8388, Cipher: "aes-128-gcm", Password: "pw", Network: "tcp", Security: "none"}},
		{"hy2", Node{Protocol: Hysteria2, Host: "hy.example.com", Port: 20000, Password: "pw", Security: "tls", SNI: "hy.example.com",
			Insecure: true, HopPorts: "20000-30000,443", HopInterval: 30, Obfs: "salamander", ObfsPassword: "ob"}},
		{"socks-tls", Node{Protocol: SOCKS, Host: "k.example.com", Port: 1080, Username: "a", Password: "b", Network: "tcp", Security: "tls", Insecure: true}},
		{"wg", Node{Protocol: WireGuard, Host: "162.159.192.1", Port: 2408, SecretKey: "c2VjcmV0", PublicKey: "cHVi",
			LocalAddress: "172.16.0.2/32,fd01::2/128", Reserved: "83,128,39", MTU: 1280}},
	}
	for _, c := range checks {
		got, ok := byName[c.name]
		if !ok {
			t.Errorf("%s: missing", c.name)
			continue
		}
		if got.ID == "" || got.Link == "" {
			t.Errorf("%s: ID %q link %q", c.name, got.ID, got.Link)
		}
		c.want.Name = c.name
		if !SameConfig(&got, &c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
		// The share link written for it reads back to the same settings.
		back, err := Parse(got.Link)
		if err != nil || !SameConfig(&back, &got) {
			t.Errorf("%s: link %s reads back as %+v (%v)", c.name, got.Link, back, err)
		}
	}
}

// fingerprint, the SHA-256 of the server's certificate, is pinned alike on every type over TLS, in hex with
// or without colons; one that is no SHA-256 rejects the node rather than leave it unpinned.
func TestClashCertificatePin(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	colons := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, typ := range []string{"http", "socks5", "trojan", "vless", "vmess", "hysteria2"} {
		for _, fp := range []string{pin, colons} {
			ns, errs := ParseClash("proxies: [{name: p, type: " + typ + ", server: localhost, port: 443, password: x, " +
				"uuid: 11111111-2222-3333-4444-555555555555, tls: true, skip-cert-verify: true, fingerprint: '" + fp + "'}]")
			if len(errs) != 0 || len(ns) != 1 || ns[0].PinSHA256 != pin {
				t.Fatalf("%s %s: %+v %v", typ, fp, ns, errs)
			}
			// VMess links have no field for it: such a node gets no link rather than one without the pin.
			if typ == "vmess" {
				if ns[0].Link != "" {
					t.Errorf("vmess link %s drops the pin", ns[0].Link)
				}
			} else if back, err := Parse(ns[0].Link); err != nil || back.PinSHA256 != pin {
				t.Errorf("%s: link %s reads back pin %q (%v)", typ, ns[0].Link, back.PinSHA256, err)
			}
		}
		if ns, errs := ParseClash("proxies: [{name: p, type: " + typ + ", server: localhost, port: 443, password: x, " +
			"uuid: 11111111-2222-3333-4444-555555555555, tls: true, fingerprint: abcd}]"); len(ns) != 0 || len(errs) != 1 {
			t.Errorf("%s: a short fingerprint gives %+v %v", typ, ns, errs)
		}
	}
	// Without TLS there is no certificate to pin.
	if ns, _ := ParseClash("proxies: [{name: p, type: http, server: localhost, port: 80, fingerprint: '" + pin + "'}]"); len(ns) != 1 || ns[0].PinSHA256 != "" {
		t.Errorf("plain HTTP: %+v", ns)
	}
}

// An HTTPS proxy link carries its TLS parameters; a plain HTTP one, or anything else, still has none, so
// subscription URLs are not taken for proxies.
func TestHTTPSLinkParameters(t *testing.T) {
	n, err := Parse("https://u:p@1.2.3.4:443?sni=proxy.example.com&insecure=1#x")
	if err != nil || n.Security != "tls" || !n.Insecure || n.SNI != "proxy.example.com" {
		t.Fatalf("%+v %v", n, err)
	}
	for _, bad := range []string{"http://1.2.3.4:443?insecure=1", "https://1.2.3.4:443?token=abc", "https://sub.example.com:443/api?token=abc"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
