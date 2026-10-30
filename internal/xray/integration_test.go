//go:build integration

// Integration tests download the official core (cached in %TEMP%\raydock-core or RAYDOCK_CORE_DIR):
//
//	go test -tags integration ./internal/xray -run Integration -v
package xray

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"raydock/internal/certpin"
	"raydock/internal/localnet"
	"raydock/internal/node"
	"raydock/internal/probe"
)

func coreForTest(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("RAYDOCK_CORE_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "raydock-core")
	}
	exe := filepath.Join(dir, ExeName())
	if len(Missing(dir)) == 0 && ReadGeoTag(dir) != "" {
		return exe
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := &http.Client{}
	if len(Missing(dir)) > 0 {
		rel, err := LatestRelease(ctx, client)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("downloading Xray %s", rel.Tag)
		if err := Install(ctx, client, rel, dir, nil); err != nil {
			t.Fatal(err)
		}
		if m := Missing(dir); len(m) > 0 {
			t.Fatalf("still missing after install: %v", m)
		}
	}
	// The package's own rule data has a few categories (no geoip:jp): RayDock runs the core with
	// Loyalsoldier's, as its installer brings (cmd/fetchcore), and so do the tests.
	tag, err := LatestGeo(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("downloading rule data %s", tag)
	if err := InstallGeo(ctx, client, tag, dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, GeoTagFile), []byte(tag+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return exe
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestIntegrationVersionAndTest(t *testing.T) {
	exe := coreForTest(t)
	v, err := Version(exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("xray %s", v)
	// All routing modes (which use geoip/geosite) and protocols needing certificates or real sites: the core must accept the configs.
	nodes := []node.Node{
		{Protocol: node.VLESS, Host: "a.example.com", Port: 443, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Flow: "xtls-rprx-vision",
			Security: "reality", SNI: "www.apple.com", PublicKey: "Z84J2IelR9ch3k8VtlVhhs5ycBUlXA7wHBWcBrjqnAw", ShortID: "6ba85179e30d4fc2"},
		{Protocol: node.VLESS, Host: "a.example.com", Port: 443, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "xhttp", Path: "/x", Mode: "auto", Security: "tls"},
		{Protocol: node.VMess, Host: "v.example.com", Port: 443, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "grpc", Path: "svc", Mode: "multi", Security: "tls", Insecure: true},
		{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Network: "httpupgrade", Path: "/u", Security: "tls"},
		{Protocol: node.VMess, Host: "v.example.com", Port: 80, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", HeaderType: "http", HostHeader: "a.com,b.com"},
		{Protocol: node.Hysteria2, Host: "h.example.com", Port: 443, Password: "p", SNI: "h.example.com"},
		{Protocol: node.Hysteria2, Host: "h.example.com", Port: 443, Password: "p", Obfs: "salamander", ObfsPassword: "o",
			HopPorts: "443,20000-30000", HopInterval: 30, PinSHA256: strings.Repeat("ab:", 31) + "ab"},
		{Protocol: node.VMess, Host: "k.example.com", Port: 9000, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "kcp",
			HeaderType: "wechat-video", Seed: "s"},
		{Protocol: node.VMess, Host: "k.example.com", Port: 9000, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "kcp",
			HeaderType: "dns", HostHeader: "www.baidu.com"},
		{Protocol: node.VLESS, Host: "a.example.com", Port: 443, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "xhttp",
			Mode: "stream-up", Extra: `{"xPaddingBytes":"100-1000","xmux":{"maxConcurrency":"16-32"}}`, Security: "tls",
			VerifyName: "real.example.com", FinalMask: `{"tcp":[{"type":"fragment","settings":{"packets":"tlshello","length":"100-200","delay":"10-20"}}]}`},
		{Protocol: node.VLESS, Host: "g.example.com", Port: 443, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "grpc", Path: "svc",
			Authority: "a.example.com", Security: "tls"},
		{Protocol: node.Shadowsocks, Host: "s.example.com", Port: 443, Cipher: "2022-blake3-aes-128-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAA==",
			Network: "ws", Path: "/s", Security: "tls"},
		{Protocol: node.SOCKS, Host: "s.example.com", Port: 1080, Username: "u", Password: "p"},
		{Protocol: node.HTTP, Host: "h.example.com", Port: 443, Security: "tls"},
		{Protocol: node.WireGuard, Host: "162.159.192.1", Port: 2408, SecretKey: wgKey(t), PublicKey: wgKey(t),
			LocalAddress: "172.16.0.2/32,2606:4700::2/128", Reserved: "1,2,3", MTU: 1280},
		{Protocol: node.MASQUE, Host: "m.example.com", Port: 443, Outbound: `{"protocol":"masque","settings":{"address":"m.example.com","port":443},
			"streamSettings":{"network":"masque","security":"tls","tlsSettings":{"serverName":"m.example.com"},"masqueSettings":{"user":"u","pass":"p"}}}`},
	}
	for _, n := range nodes {
		cfg, err := Build(DefaultOptions(), Run{Chain: []*node.Node{&n}})
		if err != nil {
			t.Fatalf("%s: %v", n.Transport(), err)
		}
		if err := checkConfig(exe, cfg); err != nil {
			t.Errorf("%s %s: core rejected config: %v", n.Protocol, n.Transport(), err)
		}
	}
	for _, mode := range []string{ModeRule, ModeGlobal, ModeDirect} {
		o := DefaultOptions()
		o.Mode = mode
		cfg, err := Build(o, Run{Chain: []*node.Node{&nodes[3]}})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if err := checkConfig(exe, cfg); err != nil {
			t.Errorf("%s: core rejected config: %v", mode, err)
		}
	}
	// Chains through outbounds with their own transports: WireGuard, Hysteria2 and MASQUE (imported JSON).
	for i, chain := range [][]*node.Node{
		{&nodes[12], &nodes[14], &nodes[5]},
		{&nodes[0], &nodes[15]},
		{&nodes[6], &nodes[3], &nodes[14]},
	} {
		cfg, err := Build(DefaultOptions(), Run{Chain: chain})
		if err != nil {
			t.Fatalf("chain %d: %v", i, err)
		}
		if err := checkConfig(exe, cfg); err != nil {
			t.Errorf("chain %d: core rejected config: %v", i, err)
		}
	}
	// The dedicated ports alone, each through its chain.
	{
		o := DefaultOptions()
		o.APIPort, o.APIToken = 23459, "integration-token"
		o.AllowLAN = true
		cfg, err := Build(o, Run{Ports: []PortRoute{{20001, []*node.Node{&nodes[3]}}, {20002, []*node.Node{&nodes[12], &nodes[5]}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := checkConfig(exe, cfg); err != nil {
			t.Errorf("dedicated ports: core rejected config: %v", err)
		}
	}
	// TUN, FakeDNS, DoH and custom rules. -test only validates, so no adapter or admin rights are needed.
	variants := map[string]func(*Options){
		"tun": func(o *Options) { o.TUN.Enabled = true },
		"tun+fakedns+ipv6": func(o *Options) {
			o.TUN.Enabled, o.FakeDNS, o.TUN.IPv6, o.DNSStrategy = true, true, true, "UseIP"
		},
		"doh+rules": func(o *Options) {
			o.DirectDNS, o.RemoteDNS = "https://dns.alidns.com/dns-query", "https://1.1.1.1/dns-query"
			o.ProxyRules = "openai.com\ngeosite:netflix\nprocess:chrome.exe"
			o.DirectRules = "10.0.0.0/8\ngeoip:jp\nfull:intranet.example"
			o.BlockRules = "keyword:tracker"
			o.BlockAds, o.DomainStrategy = true, "IPIfNonMatch"
			o.Hosts = "router.lan 192.168.1.1"
		},
	}
	for name, set := range variants {
		o := DefaultOptions()
		set(&o)
		// Under TUN: the connection's chain and a dedicated port's bound to the physical interface, their
		// addresses resolved by its DNS servers; on either adapter.
		o.APIPort, o.APIToken = 23459, "integration-token"
		r := Run{Chain: []*node.Node{&nodes[12], &nodes[0]}, Ports: []PortRoute{{20001, []*node.Node{&nodes[5]}}}}
		if o.TUN.Enabled {
			r.Local = &Local{Interface: "WLAN", DNS: []string{"192.168.1.1"}}
		}
		for adapter := range TUNNames {
			r.Adapter = adapter
			cfg, err := Build(o, r)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if err := checkConfig(exe, cfg); err != nil {
				t.Errorf("%s on adapter %d: core rejected config: %v", name, adapter, err)
			}
		}
	}
}

// Runs a local Xray server, connects with the generated client config and fetches a local HTTP service through it.
func TestIntegrationEndToEnd(t *testing.T) {
	exe := coreForTest(t)
	const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"

	target := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go target.Serve(l)
	defer target.Close()
	certPEM, keyPEM, certPin := selfSignedCert(t)
	wgServer, wgClient := wgPair(t), wgPair(t)

	ports := map[string]int{}
	for _, k := range []string{"ss", "vmess", "vless", "vless-ws", "trojan", "vmess-grpc", "socks", "http", "vless-kcp", "hy2", "wg", "masque"} {
		ports[k] = freePort(t)
	}
	inbound := func(tag, proto string, port int, settings, stream map[string]any) map[string]any {
		in := map[string]any{"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": proto, "settings": settings}
		if stream != nil {
			in["streamSettings"] = stream
		}
		return in
	}
	server := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			inbound("ss", "shadowsocks", ports["ss"], map[string]any{"method": "aes-128-gcm", "password": "test-pass", "network": "tcp,udp"}, nil),
			inbound("vmess", "vmess", ports["vmess"], map[string]any{"clients": []any{map[string]any{"id": uuid}}}, nil),
			inbound("vless", "vless", ports["vless"], map[string]any{"clients": []any{map[string]any{"id": uuid}}, "decryption": "none"}, nil),
			inbound("vless-ws", "vless", ports["vless-ws"], map[string]any{"clients": []any{map[string]any{"id": uuid}}, "decryption": "none"},
				map[string]any{"network": "ws", "wsSettings": map[string]any{"path": "/ws"}}),
			inbound("trojan", "trojan", ports["trojan"], map[string]any{"clients": []any{map[string]any{"password": "test-pass"}}}, nil),
			inbound("vmess-grpc", "vmess", ports["vmess-grpc"], map[string]any{"clients": []any{map[string]any{"id": uuid}}},
				map[string]any{"network": "grpc", "grpcSettings": map[string]any{"serviceName": "svc"}}),
			inbound("socks", "socks", ports["socks"], map[string]any{"auth": "password", "udp": true,
				"accounts": []any{map[string]any{"user": "u", "pass": "p"}}}, nil),
			inbound("http", "http", ports["http"], map[string]any{"accounts": []any{map[string]any{"user": "u", "pass": "p"}}}, nil),
			// Legacy mKCP server: AES-128-GCM (seed) + wechat header.
			inbound("vless-kcp", "vless", ports["vless-kcp"], map[string]any{"clients": []any{map[string]any{"id": uuid}}, "decryption": "none"},
				map[string]any{"network": "kcp", "finalmask": map[string]any{"udp": []any{
					map[string]any{"type": "mkcp-legacy", "settings": map[string]any{"value": "kcp-seed"}},
					map[string]any{"type": "mkcp-legacy", "settings": map[string]any{"header": "wechat"}},
				}}}),
			inbound("hy2", "hysteria", ports["hy2"], map[string]any{"version": 2, "users": []any{map[string]any{"auth": "hy-pass"}}},
				map[string]any{"network": "hysteria", "security": "tls", "hysteriaSettings": map[string]any{"version": 2},
					"tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificate": strings.Split(certPEM, "\n"), "key": strings.Split(keyPEM, "\n")}}},
					"finalmask":   map[string]any{"udp": []any{map[string]any{"type": "salamander", "settings": map[string]any{"password": "obfs-pass"}}}}}),
			inbound("masque", "masque", ports["masque"], map[string]any{"users": []any{map[string]any{"email": "u", "pass": "mq-pass"}},
				"address": []any{"10.9.0.1/24"}},
				map[string]any{"network": "masque", "security": "tls", "masqueSettings": map[string]any{},
					"tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificate": strings.Split(certPEM, "\n"), "key": strings.Split(keyPEM, "\n")}}}}),
			inbound("wg", "wireguard", ports["wg"], map[string]any{"secretKey": wgServer.priv,
				"peers": []any{map[string]any{"publicKey": wgClient.pub, "allowedIPs": []any{"10.0.0.2/32"}}}}, nil),
		},
		// Since 26.4.15 the server's freedom blocks private addresses by default; the target is local, so allow it.
		"outbounds": []any{
			map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}},
			// The WireGuard server's gVisor stack drops tunneled packets to loopback, so the client targets a TEST-NET
			// address that this outbound redirects to the local service.
			map[string]any{"tag": "wg-out", "protocol": "freedom", "settings": map[string]any{"redirect": l.Addr().String(),
				"finalRules": []any{map[string]any{"action": "allow"}}}},
		},
		"routing": map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []any{"wg", "masque"}, "outboundTag": "wg-out"}}},
	}
	serverCfg, _ := json.Marshal(server)
	dir := t.TempDir()
	var srv Core
	var srvLog syncLog
	srv.OnLog = srvLog.add
	if err := srv.Start(context.Background(), exe, filepath.Join(dir, "server.json"), serverCfg, nil); err != nil {
		t.Fatalf("server failed to start: %v", err)
	}
	defer srv.Stop()

	// WireGuard and MASQUE target a TEST-NET address redirected by the server; the rest target the service directly.
	targets := map[string]string{"wireguard": "192.0.2.1:80", "masque": "192.0.2.1:80"}
	// MASQUE has no share link; import it from outbound JSON as a user would.
	imported, errs := node.ParseJSON(fmt.Sprintf(`{"outbounds": [{"tag": "mq", "protocol": "masque", "settings": {"address": "127.0.0.1", "port": %d},
		"streamSettings": {"network": "masque", "security": "tls", "masqueSettings": {"user": "u", "pass": "mq-pass"},
		"tlsSettings": {"serverName": "localhost", "pinnedPeerCertSha256": %q}}}]}`, ports["masque"], certPin))
	if len(imported) != 1 || len(errs) > 0 {
		t.Fatalf("import MASQUE outbound: %v", errs)
	}
	cases := []struct {
		name string
		n    node.Node
	}{
		{"shadowsocks", node.Node{Protocol: node.Shadowsocks, Host: "127.0.0.1", Port: ports["ss"], Cipher: "aes-128-gcm", Password: "test-pass"}},
		{"vmess", node.Node{Protocol: node.VMess, Host: "127.0.0.1", Port: ports["vmess"], UUID: uuid}},
		{"vless", node.Node{Protocol: node.VLESS, Host: "127.0.0.1", Port: ports["vless"], UUID: uuid}},
		{"vless-ws", node.Node{Protocol: node.VLESS, Host: "127.0.0.1", Port: ports["vless-ws"], UUID: uuid, Network: "ws", Path: "/ws"}},
		{"trojan", node.Node{Protocol: node.Trojan, Host: "127.0.0.1", Port: ports["trojan"], Password: "test-pass", Security: "none"}},
		{"vmess-grpc", node.Node{Protocol: node.VMess, Host: "127.0.0.1", Port: ports["vmess-grpc"], UUID: uuid, Network: "grpc", Path: "svc"}},
		{"socks", node.Node{Protocol: node.SOCKS, Host: "127.0.0.1", Port: ports["socks"], Username: "u", Password: "p"}},
		{"http", node.Node{Protocol: node.HTTP, Host: "127.0.0.1", Port: ports["http"], Username: "u", Password: "p"}},
		{"vless-kcp", node.Node{Protocol: node.VLESS, Host: "127.0.0.1", Port: ports["vless-kcp"], UUID: uuid, Network: "kcp",
			HeaderType: "wechat-video", Seed: "kcp-seed"}},
		{"hysteria2", node.Node{Protocol: node.Hysteria2, Host: "127.0.0.1", Port: ports["hy2"], Password: "hy-pass", SNI: "localhost",
			PinSHA256: certPin, Obfs: "salamander", ObfsPassword: "obfs-pass", HopPorts: fmt.Sprintf("%d-%d", ports["hy2"], ports["hy2"])}},
		{"wireguard", node.Node{Protocol: node.WireGuard, Host: "127.0.0.1", Port: ports["wg"], SecretKey: wgClient.priv,
			PublicKey: wgServer.pub, LocalAddress: "10.0.0.2/32"}},
		{"masque", imported[0]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := freePort(t)
			// The node's outbounds as the core has them, behind an inbound the test can use.
			cfg, err := BuildProbe([]PortRoute{{Port: port, Chain: []*node.Node{&tc.n}}}, nil)
			if err != nil {
				t.Fatal(err)
			}

			var cli Core
			var cliLog syncLog
			cli.OnLog = cliLog.add
			if err := cli.Start(context.Background(), exe, filepath.Join(dir, tc.name+".json"), cfg, nil); err != nil {
				t.Fatalf("client failed to start: %v", err)
			}
			defer cli.Stop()
			if !cli.Running() {
				t.Fatal("client not running")
			}
			target := first(targets[tc.name], l.Addr().String())
			// Through the HTTP side of the probe's inbound.
			d, err := probe.ViaProxy(context.Background(), port, "http://"+target+"/generate_204", 10*time.Second)
			if err != nil {
				t.Fatalf("request through proxy failed: %v\nserver log:\n%s\nclient log:\n%s", err, srvLog.String(), cliLog.String())
			}
			t.Logf("%s round trip %v", tc.name, d.Round(time.Millisecond))
			// Through the SOCKS5 side, as the main core reaches it.
			if err := socksGet(port, target); err != nil {
				t.Fatalf("request through SOCKS5 failed: %v", err)
			}
		})
	}
	if !strings.Contains(srvLog.String(), "accepted") {
		t.Errorf("no forwarding in the server log:\n%s", srvLog.String())
	}

	byName := map[string]*node.Node{}
	for i := range cases {
		byName[cases[i].name] = &cases[i].n
	}
	// SOCKS front → VLESS → Trojan exit: each hop reaches the next through the one before it.
	t.Run("chain", func(t *testing.T) {
		port := freePort(t)
		cfg, err := BuildProbe([]PortRoute{{Port: port, Chain: []*node.Node{byName["socks"], byName["vless"], byName["trojan"]}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var cli Core
		if err := cli.Start(context.Background(), exe, filepath.Join(dir, "chain.json"), cfg, nil); err != nil {
			t.Fatal(err)
		}
		defer cli.Stop()
		if _, err := probe.ViaProxy(context.Background(), port, "http://"+l.Addr().String()+"/generate_204", 10*time.Second); err != nil {
			t.Fatalf("request through the chain failed: %v\nserver log:\n%s", err, srvLog.String())
		}
		for _, hop := range []string{"vless", "trojan"} {
			if want := fmt.Sprintf("accepted tcp:127.0.0.1:%d", ports[hop]); !strings.Contains(srvLog.String(), want) {
				t.Errorf("no %q in the server log: the hop to %s did not go through the previous node", want, hop)
			}
		}
	})

	// Dedicated ports alone: the core reaches the target through each port's node, the second one through
	// a chain. There is no local port.
	t.Run("ports", func(t *testing.T) {
		p1, p2 := freePort(t), freePort(t)
		o := proxiedLocally(t)
		o.APIPort, o.APIToken = freePort(t), "integration-token"
		cfg, err := Build(o, Run{Ports: []PortRoute{
			{p1, []*node.Node{byName["vmess"]}},
			{p2, []*node.Node{byName["socks"], byName["trojan"]}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		startCore(t, exe, dir, "ports", cfg, nil)
		for _, p := range []int{p1, p2} {
			if _, err := probe.ViaProxy(context.Background(), p, "http://"+l.Addr().String()+"/generate_204", 10*time.Second); err != nil {
				t.Fatalf("port %d: %v", p, err)
			}
			if err := socksGet(p, l.Addr().String()); err != nil {
				t.Fatalf("port %d over SOCKS5: %v", p, err)
			}
		}
		if err := socksGet(o.Port, l.Addr().String()); err == nil {
			t.Error("a local port without a connection")
		}
	})

	// A connection: the local port reaches the node, TCP and UDP alike.
	t.Run("connection", func(t *testing.T) {
		o := proxiedLocally(t)
		startConnection(t, exe, dir, "conn", o, Run{Chain: []*node.Node{byName["vless"]}}, nil)
		// Requests the server forwarded to the target.
		targetHits := func() int { return strings.Count(srvLog.String(), "accepted tcp:"+l.Addr().String()) }
		targetBefore := targetHits()
		if _, err := probe.ViaProxy(context.Background(), o.Port, "http://"+l.Addr().String()+"/generate_204", 10*time.Second); err != nil {
			t.Fatalf("local port: %v", err)
		}
		if err := socksGet(o.Port, l.Addr().String()); err != nil {
			t.Fatalf("local port over SOCKS5: %v", err)
		}
		// UDP too (remote DNS queries, QUIC): core → vless → server.
		echo, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer echo.Close()
		go func() {
			buf := make([]byte, 1500)
			for {
				n, from, err := echo.ReadFrom(buf)
				if err != nil {
					return
				}
				echo.WriteTo(buf[:n], from)
			}
		}()
		if err := socksUDPEcho(o.Port, echo.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatalf("UDP through the local port: %v\nserver log:\n%s", err, srvLog.String())
		}
		time.Sleep(200 * time.Millisecond)
		// Both requests reached the server: the core has no other way out than the node.
		if n := targetHits() - targetBefore; n != 2 {
			t.Errorf("the server should have forwarded 2 requests, got %d\n%s", n, srvLog.String())
		}
	})
	// A restart: the new core starts without public listeners, then activates them exclusively once the
	// old core stops. TCP and UDP remain supported without letting an unrelated socket join.
	t.Run("overlap", func(t *testing.T) {
		if !Overlap {
			t.Skip("the old core stops first")
		}
		o := proxiedLocally(t)
		port := freePort(t)
		r := Run{Chain: []*node.Node{byName["vless"]}, Ports: []PortRoute{{port, []*node.Node{byName["vmess"]}}}}
		o.APIPort, o.APIToken = freePort(t), "integration-old-token"
		old := startCore(t, exe, dir, "old", mustBuild(t, o, r), nil)
		o.APIPort, o.APIToken = freePort(t), "integration-new-token"
		r.Deferred = true
		next := startCore(t, exe, dir, "new", mustBuild(t, o, r), nil)
		if !old.Running() || !next.Running() {
			t.Fatal("both cores should run")
		}
		old.Stop()
		r.Deferred = false
		client := StatsClient(o.APIToken)
		defer client.CloseIdleConnections()
		if err := ActivateListeners(context.Background(), client, o.APIPort, mustBuild(t, o, r)); err != nil {
			t.Fatal(err)
		}
		for _, p := range []int{o.Port, port} {
			if _, err := probe.ViaProxy(context.Background(), p, "http://"+l.Addr().String()+"/generate_204", 10*time.Second); err != nil {
				t.Fatalf("port %d after the old core stopped: %v", p, err)
			}
		}
		echo, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer echo.Close()
		go func() {
			buf := make([]byte, 1500)
			for {
				n, from, err := echo.ReadFrom(buf)
				if err != nil {
					return
				}
				echo.WriteTo(buf[:n], from)
			}
		}()
		if err := socksUDPEcho(o.Port, echo.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatalf("UDP through the new core: %v", err)
		}
	})
	// Speed test: a probe core with its own port, download and upload through a node.
	t.Run("speed", func(t *testing.T) {
		speed := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				io.Copy(io.Discard, r.Body)
				return
			}
			w.Write(make([]byte, 16<<20))
		})}
		sl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go speed.Serve(sl)
		defer speed.Close()
		port := freePort(t)
		cfg, err := BuildProbe([]PortRoute{{Port: port, Chain: []*node.Node{byName["vless"]}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var cli Core
		if err := cli.Start(context.Background(), exe, filepath.Join(dir, "speed.json"), cfg, nil); err != nil {
			t.Fatal(err)
		}
		defer cli.Stop()
		client := probe.ProxyClient(port)
		base := "http://" + sl.Addr().String()
		down, err := probe.Download(context.Background(), client, base+"/down", 2*time.Second)
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		up, err := probe.Upload(context.Background(), client, base+"/up", 2*time.Second)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		t.Logf("through VLESS: down %.1f MB/s, up %.1f MB/s", down/(1<<20), up/(1<<20))
	})

	// Rules of the connection, process rules among them: the core sees the program of each connection.
	t.Run("rules", func(t *testing.T) {
		get := func(port int) error {
			_, err := probe.ViaProxy(context.Background(), port, "http://"+l.Addr().String()+"/generate_204", 10*time.Second)
			return err
		}
		for _, c := range []struct {
			rules string
			ok    bool
		}{{"127.0.0.1/32", false}, {"process:" + filepath.Base(os.Args[0]), false}, {"process:other.exe", true}} {
			o := proxiedLocally(t)
			o.BlockRules = c.rules
			core := startConnection(t, exe, dir, "rules", o, Run{Chain: []*node.Node{byName["vless"]}}, nil)
			if err := get(o.Port); (err == nil) != c.ok {
				t.Errorf("block %q: %v", c.rules, err)
			}
			core.Stop()
		}
	})

	// Latency of nodes reached over UDP: one probe core serves each on its own port, and a request is timed
	// over the session the first one set up.
	// The certificate pinned for an insecure link is the one Xray's Hysteria2 server presents, read through
	// its Salamander obfuscation; pinned, the node connects.
	t.Run("pin", func(t *testing.T) {
		hy := *byName["hysteria2"]
		hy.PinSHA256, hy.Insecure = "", true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pin, err := certpin.Fetch(ctx, &hy, nil, nil)
		if err != nil || !strings.EqualFold(pin, strings.ReplaceAll(certPin, ":", "")) {
			t.Fatalf("%q %v, want %s", pin, err, certPin)
		}
		hy.AutoPin = pin
		port := freePort(t)
		cfg, err := BuildProbe([]PortRoute{{Port: port, Chain: []*node.Node{&hy}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var cli Core
		if err := cli.Start(context.Background(), exe, filepath.Join(dir, "pin.json"), cfg, nil); err != nil {
			t.Fatal(err)
		}
		defer cli.Stop()
		if _, err := probe.ViaProxy(ctx, port, "http://"+l.Addr().String()+"/generate_204", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		// Beside a name to verify the certificate against, the certificate seen once is not pinned: Xray would
		// accept it without that verification, which the self-signed one fails.
		hy.VerifyName = "localhost"
		port = freePort(t)
		if cfg, err = BuildProbe([]PortRoute{{Port: port, Chain: []*node.Node{&hy}}}, nil); err != nil {
			t.Fatal(err)
		}
		var named Core
		if err := named.Start(context.Background(), exe, filepath.Join(dir, "pin-name.json"), cfg, nil); err != nil {
			t.Fatal(err)
		}
		defer named.Stop()
		if _, err := probe.ViaProxy(ctx, port, "http://"+l.Addr().String()+"/generate_204", 3*time.Second); err == nil {
			t.Fatal("the pin seen once passed a certificate the name does not verify")
		}
	})

	t.Run("udp latency", func(t *testing.T) {
		var routes []PortRoute
		names := []string{"hysteria2", "wireguard", "vless-kcp", "masque"}
		for _, name := range names {
			if !byName[name].UDP() {
				t.Fatalf("%s should count as UDP", name)
			}
			routes = append(routes, PortRoute{Port: freePort(t), Chain: []*node.Node{byName[name]}})
		}
		cfg, err := BuildProbe(routes, nil)
		if err != nil {
			t.Fatal(err)
		}
		var cli Core
		if err := cli.Start(context.Background(), exe, filepath.Join(dir, "latency.json"), cfg, nil); err != nil {
			t.Fatal(err)
		}
		defer cli.Stop()
		for i, name := range names {
			target := first(targets[name], l.Addr().String())
			d, err := probe.Through(context.Background(), routes[i].Port, "http://"+target+"/generate_204", 10*time.Second)
			if err != nil {
				t.Fatalf("%s: %v\nserver log:\n%s", name, err, srvLog.String())
			}
			t.Logf("%s latency %v", name, d.Round(time.Millisecond))
		}
	})
}

// socksGet sends a minimal HTTP request through SOCKS5 CONNECT.
func socksGet(proxyPort int, target string) error {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	host, portStr, _ := net.SplitHostPort(target)
	var port int
	fmt.Sscan(portStr, &port)
	ip := net.ParseIP(host).To4()
	buf := make([]byte, 512)
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	if _, err := c.Read(buf[:2]); err != nil || buf[1] != 0 {
		return fmt.Errorf("handshake failed %v %v", buf[:2], err)
	}
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return err
	}
	if _, err := c.Read(buf[:10]); err != nil || buf[1] != 0 {
		return fmt.Errorf("CONNECT failed %v %v", buf[:10], err)
	}
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(buf[:n]), "HTTP/1.1 204") {
		return fmt.Errorf("response %q", buf[:n])
	}
	return nil
}

// socksUDPEcho sends a datagram to target through SOCKS5 UDP ASSOCIATE and expects it back.
func socksUDPEcho(proxyPort int, target *net.UDPAddr) error {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 512)
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[1] != 0 {
		return fmt.Errorf("handshake failed %v %v", buf[:2], err)
	}
	if _, err := c.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, buf[:10]); err != nil || buf[1] != 0 || buf[3] != 1 {
		return fmt.Errorf("UDP ASSOCIATE failed %v %v", buf[:10], err)
	}
	relay := &net.UDPAddr{IP: net.IP(buf[4:8]).To4(), Port: int(buf[8])<<8 | int(buf[9])}
	if relay.IP.IsUnspecified() {
		relay.IP = net.IPv4(127, 0, 0, 1)
	}
	u, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		return err
	}
	defer u.Close()
	_ = u.SetDeadline(time.Now().Add(10 * time.Second))
	payload := []byte("raydock udp")
	pkt := append([]byte{0, 0, 0, 1}, target.IP.To4()...)
	pkt = append(pkt, byte(target.Port>>8), byte(target.Port))
	if _, err := u.Write(append(pkt, payload...)); err != nil {
		return err
	}
	n, err := u.Read(buf)
	if err != nil {
		return err
	}
	if n < 10 || string(buf[10:n]) != string(payload) {
		return fmt.Errorf("reply %q", buf[:n])
	}
	return nil
}

// selfSignedCert creates a localhost certificate and returns the PEMs and its SHA-256 for pinning.
func selfSignedCert(t *testing.T) (certPEM, keyPEM, pin string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalPKCS8PrivateKey(key)
	sum := sha256.Sum256(der)
	return strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))),
		strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}))), hex.EncodeToString(sum[:])
}

type wgKeys struct{ priv, pub string }

// wgPair generates a Base64 WireGuard (X25519) key pair.
func wgPair(t *testing.T) wgKeys {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return wgKeys{base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())}
}

func wgKey(t *testing.T) string { return wgPair(t).pub }

// Through the local network: the resolver core answers from the local DNS servers, the bound dialer
// reaches a real site, and the core accepts a probe config bound to the interface.
func TestIntegrationLocalNetwork(t *testing.T) {
	exe := coreForTest(t)
	iface, err := localnet.Find()
	if err != nil {
		t.Skipf("no local network: %v", err)
	}
	if iface == nil {
		t.Skip("routes decide on this OS")
	}
	local := Local{Interface: iface.Name, Strategy: "UseIPv4"}
	for _, s := range iface.DNS {
		local.DNS = append(local.DNS, s.Addr().String())
	}
	port := freePort(t)
	cfg, err := BuildResolver(port, local)
	if err != nil {
		t.Fatal(err)
	}
	var core Core
	var coreLog strings.Builder
	core.OnLog = func(line string) { coreLog.WriteString(line + "\n") }
	if err := core.Start(context.Background(), exe, filepath.Join(t.TempDir(), "resolver.json"), cfg, nil); err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	r := localnet.Resolver(localnet.Dialer(nil).DialContext, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := r.LookupNetIP(ctx, "ip4", "www.baidu.com")
	if err != nil || len(ips) == 0 {
		t.Fatalf("lookup %v %v\n%s", ips, err, coreLog.String())
	}
	d, err := probe.TCP(ctx, localnet.Dialer(iface), netip.AddrPortFrom(ips[0], 443), 5*time.Second)
	if err != nil {
		t.Fatalf("TCP through %s: %v", iface.Name, err)
	}
	t.Logf("www.baidu.com %v via %s: %v", ips[0], iface.Name, d.Round(time.Millisecond))

	n := node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Security: "tls"}
	probeCfg, err := BuildProbe([]PortRoute{{Port: freePort(t), Chain: []*node.Node{&n}}}, &local)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(exe, probeCfg); err != nil {
		t.Errorf("core rejected the probe config: %v", err)
	}
}

// A Stop issued while Start is still bringing the core up waits for it and ends it, freeing its ports:
// a TUN core started next can take them.
func TestIntegrationStopDuringStart(t *testing.T) {
	exe := coreForTest(t)
	port := freePort(t)
	cfg := []byte(fmt.Sprintf(`{"log":{"loglevel":"warning"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth"}}],"outbounds":[{"protocol":"freedom"}]}`, port))
	dir := t.TempDir()
	for i := range 5 {
		var c Core
		started := make(chan error, 1)
		go func() {
			started <- c.Start(context.Background(), exe, filepath.Join(dir, fmt.Sprint(i, ".json")), cfg, nil)
		}()
		time.Sleep(time.Duration(5+i*20) * time.Millisecond) // Stop comes after Start began
		c.Stop()
		<-started
		if c.Running() {
			t.Fatalf("round %d: still running after Stop", i)
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("round %d: port still taken after Stop: %v", i, err)
		}
		l.Close()
	}
}

// syncLog collects a core's output, written by the goroutine reading it.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) add(s string) { l.mu.Lock(); l.b.WriteString(s + "\n"); l.mu.Unlock() }
func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startCore(t *testing.T, exe, dir, name string, cfg []byte, log *syncLog) *Core {
	t.Helper()
	c := &Core{}
	if log != nil {
		c.OnLog = log.add
	}
	if err := c.Start(context.Background(), exe, filepath.Join(dir, name+".json"), cfg, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c
}

// proxiedLocally returns options proxying the local targets of the tests, which global mode would
// otherwise send direct.
func proxiedLocally(t *testing.T) Options {
	o := DefaultOptions()
	o.Port, o.Mode, o.ProxyRules = freePort(t), ModeGlobal, "127.0.0.1/32"
	return o
}

// startConnection starts a connection's core built with o and c, its API on a port of its own.
func startConnection(t *testing.T, exe, dir, name string, o Options, r Run, log *syncLog) *Core {
	t.Helper()
	o.APIPort, o.APIToken = freePort(t), "integration-token"
	cfg, err := Build(o, r)
	if err != nil {
		t.Fatal(err)
	}
	return startCore(t, exe, dir, name, cfg, log)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
	}
}

// checkConfig checks with "xray run -test" whether the core accepts config.
func checkConfig(exe string, config []byte) error {
	f, err := os.CreateTemp("", "raydock-test-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(config); err != nil {
		f.Close()
		return err
	}
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "run", "-test", "-config", f.Name())
	cmd.Env = assetEnv(exe)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, lastLines(strings.TrimSpace(string(out)), 6))
	}
	return nil
}
