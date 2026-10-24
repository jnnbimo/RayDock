package node

import (
	"fmt"
	"strings"
	"testing"
)

func TestClashAliasSafety(t *testing.T) {
	for _, text := range []string{
		"proxies: &p [*p]",
		"proxies: [{type: socks5, server: localhost, port: 1080, extra: &loop [*loop]}]",
		"proxies: []\nunused: " + strings.Repeat("[", 140) + "x" + strings.Repeat("]", 140),
	} {
		if _, errs := ParseClash(text); len(errs) == 0 {
			t.Errorf("unsafe YAML accepted: %.120s", text)
		}
	}
	var b strings.Builder
	b.WriteString("a0: &a0 [value]\n")
	for i := 1; i < 8; i++ {
		fmt.Fprintf(&b, "a%d: &a%d [%s]\n", i, i, strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*a%d,", i-1), 10), ","))
	}
	b.WriteString("proxies: [{type: socks5, server: localhost, port: 1080, unused: *a7}]\n")
	if _, errs := ParseClash(b.String()); len(errs) == 0 {
		t.Fatal("excessive alias expansion accepted")
	}
	// An unrelated cycle is not traversed at all; merge keys within proxies use the library decoder.
	text := "unused: &cycle [*cycle]\nbase: &base {type: trojan, server: localhost, port: 443, password: 00123}\nproxies:\n - {<<: *base, name: merged}\n"
	ns, errs := ParseClash(text)
	if len(errs) != 0 || len(ns) != 1 || ns[0].Password != "00123" {
		t.Fatalf("safe anchors/merge/scalars: %+v %v", ns, errs)
	}
}

func TestClashOpaqueCredentials(t *testing.T) {
	for _, entry := range []string{
		"{type: ss, server: localhost, port: 443, cipher: aes-128-gcm, password: ' secret '}",
		"{type: trojan, server: localhost, port: 443, password: ' secret '}",
		"{type: hysteria2, server: localhost, port: 443, password: ' secret ', obfs: salamander, obfs-password: ' mask '}",
		"{type: socks5, server: localhost, port: 443, username: ' user ', password: ' secret '}",
		"{type: http, server: localhost, port: 443, username: ' user ', password: ' secret ', tls: true}",
	} {
		ns, errs := ParseClash("proxies: [" + entry + "]")
		if len(errs) != 0 || len(ns) != 1 {
			t.Fatalf("%s: %v", entry, errs)
		}
		n := ns[0]
		if n.Password != " secret " {
			t.Errorf("password changed: %q", n.Password)
		}
		if n.Protocol == HTTP || n.Protocol == SOCKS {
			if n.Username != " user " {
				t.Errorf("username changed: %q", n.Username)
			}
		}
		if n.Protocol == Hysteria2 && n.ObfsPassword != " mask " {
			t.Errorf("obfs password changed: %q", n.ObfsPassword)
		}
	}
}

func TestClashOpaqueIdentifiers(t *testing.T) {
	for _, protocol := range []string{"vless", "vmess"} {
		ns, errs := ParseClash("proxies: [{type: " + protocol + ", server: localhost, port: 443, uuid: ' user '}]")
		if len(errs) != 0 || len(ns) != 1 || ns[0].UUID != " user " {
			t.Fatalf("authentication ID changed: %+v %v", ns, errs)
		}
		back, err := Parse(ns[0].Link)
		if err != nil || back.UUID != " user " {
			t.Fatalf("authentication ID changed on export: %+v %v", back, err)
		}
	}
	ns, errs := ParseClash("proxies: [{type: wireguard, server: localhost, port: 443, private-key: ' private ', public-key: ' public ', pre-shared-key: ' shared '}]")
	if len(errs) != 0 || len(ns) != 1 {
		t.Fatal(errs)
	}
	if ns[0].SecretKey != " private " || ns[0].PublicKey != " public " || ns[0].PreSharedKey != " shared " {
		t.Fatalf("key data changed: %+v", ns[0])
	}
}

func TestNormalizeRejectsMalformedHosts(t *testing.T) {
	for _, host := range []string{"a?query", "a/path", "a#fragment", "user@host", "host:443", "[::1]", "a\nb"} {
		if _, err := Normalize(Node{Protocol: SOCKS, Host: host, Port: 1080}); err == nil {
			t.Errorf("malformed host accepted: %q", host)
		}
	}
}

func TestNormalizeCompleteTLSModel(t *testing.T) {
	ns, errs := ParseClash("proxies: [{type: http, server: localhost, port: 443, tls: true, alpn: [h2], client-fingerprint: firefox}]")
	if len(errs) != 0 || len(ns) != 1 {
		t.Fatal(errs)
	}
	if ns[0].ALPN != "h2" || ns[0].Fingerprint != "firefox" {
		t.Fatalf("Clash TLS lost: %+v", ns[0])
	}
	for _, n := range []Node{
		ns[0],
		{Protocol: VMess, Host: "localhost", Port: 443, UUID: "u", Network: "grpc", Security: "tls", PinSHA256: strings.Repeat("ab", 32), VerifyName: "cert.test", ECHConfig: "ech", Authority: "service.test", FinalMask: "{}"},
	} {
		m, err := Normalize(n)
		if err != nil {
			t.Fatal(err)
		}
		if !SameConfig(&n, &m) {
			t.Fatalf("normalization changed config: %+v -> %+v", n, m)
		}
		if m.Link != "" {
			t.Fatal("advanced TLS settings must not be exported in a format that cannot express them")
		}
	}
	n := Node{Protocol: HTTP, Host: "localhost", Port: 443, Network: "tcp", Security: "tls", FinalMask: "{}"}
	m, err := Normalize(n)
	if err != nil || m.FinalMask != "{}" || m.Link != "" {
		t.Fatalf("unrepresentable settings were dropped/exported: %+v %v", m, err)
	}
}
