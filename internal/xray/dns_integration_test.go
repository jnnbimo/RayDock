//go:build integration

package xray

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"raydock/internal/node"
	"raydock/internal/probe"
)

// Resolve an unmatched domain through local cores only. The resolvers share an
// IP, and optionally the very same endpoint; only a functioning proxy may carry
// remote queries in rule/global modes. Direct mode intentionally sends them direct.
func TestIntegrationDNSResolverRoles(t *testing.T) {
	exe := coreForTest(t)
	for _, sameEndpoint := range []bool{false, true} {
		for _, mode := range []string{ModeRule, ModeGlobal, ModeDirect} {
			for _, available := range []bool{false, true} {
				t.Run(fmt.Sprintf("same=%v/%s/proxy=%v", sameEndpoint, mode, available), func(t *testing.T) {
					remote, err := net.ListenPacket("udp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					defer remote.Close()
					directAddr := remote.LocalAddr().String()
					if !sameEndpoint {
						direct, err := net.ListenPacket("udp", "127.0.0.1:0")
						if err != nil {
							t.Fatal(err)
						}
						defer direct.Close()
						directAddr = direct.LocalAddr().String()
					}
					port := freePort(t)
					var serverLog syncLog
					if available {
						cfg := []byte(fmt.Sprintf(`{"log":{"loglevel":"warning"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"udp":true}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}]}`, port))
						startCore(t, exe, t.TempDir(), "dns-proxy", cfg, &serverLog)
					}
					o := DefaultOptions()
					o.Port, o.Mode, o.DomainStrategy = freePort(t), mode, "IPOnDemand"
					o.DirectDNS, o.RemoteDNS = directAddr, remote.LocalAddr().String()
					n := node.Node{Protocol: node.SOCKS, Host: "127.0.0.1", Port: port}
					startConnection(t, exe, t.TempDir(), "dns", o, Backend{Chain: []*node.Node{&n}}, nil)
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					done := make(chan struct{})
					go func() {
						defer close(done)
						probe.ViaProxy(ctx, o.Port, "http://unmatched.raydock-audit.org/", 3*time.Second)
					}()
					remote.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
					buf := make([]byte, 2048)
					count, _, err := remote.ReadFrom(buf)
					cancel()
					<-done
					want := available || mode == ModeDirect
					if (err == nil) != want {
						t.Fatalf("remote DNS received=%v (%d bytes), want=%v: %v\n%s", err == nil, count, want, err, serverLog.String())
					}
					if available && mode != ModeDirect {
						waitFor(t, func() bool { return strings.Contains(serverLog.String(), "accepted udp:") })
					}
				})
			}
		}
	}
}

// A domestic resolver on this computer answers with the local port open to the LAN, where direct traffic
// may not reach loopback.
func TestIntegrationLoopbackResolverWithLAN(t *testing.T) {
	exe := coreForTest(t)
	for _, lan := range []bool{false, true} {
		t.Run(fmt.Sprint("lan=", lan), func(t *testing.T) {
			resolver, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer resolver.Close()
			o := DefaultOptions()
			o.Port, o.Mode, o.DomainStrategy, o.AllowLAN = freePort(t), ModeDirect, "IPOnDemand", lan
			o.DirectDNS, o.RemoteDNS = resolver.LocalAddr().String(), resolver.LocalAddr().String()
			n := node.Node{Protocol: node.SOCKS, Host: "127.0.0.1", Port: freePort(t)}
			startConnection(t, exe, t.TempDir(), "dns", o, Backend{Chain: []*node.Node{&n}}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				probe.ViaProxy(ctx, o.Port, "http://unmatched.raydock-audit.org/", 3*time.Second)
			}()
			resolver.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err = resolver.ReadFrom(make([]byte, 2048))
			cancel()
			<-done
			if err != nil {
				t.Fatalf("the resolver on loopback was not asked: %v", err)
			}
		})
	}
}
