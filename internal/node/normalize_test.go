package node

import (
	"strings"
	"testing"
)

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
