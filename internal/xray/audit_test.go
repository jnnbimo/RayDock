package xray

import (
	"raydock/internal/node"
	"strings"
	"testing"
)

func TestStructuredTLSReachesOutbound(t *testing.T) {
	ns, errs := node.ParseClash("proxies: [{type: http, server: localhost, port: 443, tls: true, alpn: [h2], client-fingerprint: firefox}]")
	if len(errs) != 0 || len(ns) != 1 {
		t.Fatal(errs)
	}
	cfg := decodeNode(t, ns[0])
	if at(cfg, "outbounds.0.streamSettings.tlsSettings.alpn.0") != "h2" || at(cfg, "outbounds.0.streamSettings.tlsSettings.fingerprint") != "firefox" {
		t.Fatalf("Clash TLS did not reach core: %v", cfg)
	}
	pin := strings.Repeat("ab", 32)
	n, err := node.Normalize(node.Node{Protocol: node.VMess, Host: "localhost", Port: 443, UUID: "u", Network: "grpc", Security: "tls", PinSHA256: pin, Authority: "service.test"})
	if err != nil {
		t.Fatal(err)
	}
	cfg = decodeNode(t, n)
	if at(cfg, "outbounds.0.streamSettings.tlsSettings.pinnedPeerCertSha256") != pin || at(cfg, "outbounds.0.streamSettings.grpcSettings.authority") != "service.test" {
		t.Fatalf("VMess TLS did not reach core: %v", cfg)
	}
}
