package certpin

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"raydock/internal/localnet"
	"raydock/internal/node"
)

func testResolver(t *testing.T, ips ...string) *net.Resolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	var addrs []netip.Addr
	for _, s := range ips {
		addrs = append(addrs, netip.MustParseAddr(s))
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil {
				continue
			}
			m.Header.Response = true
			m.Header.RecursionAvailable = true
			for _, q := range m.Questions {
				for _, ip := range addrs {
					rr := dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, Type: q.Type, TTL: 1}}
					switch {
					case q.Type == dnsmessage.TypeA && ip.Is4():
						rr.Body = &dnsmessage.AResource{A: ip.As4()}
					case q.Type == dnsmessage.TypeAAAA && ip.Is6():
						rr.Body = &dnsmessage.AAAAResource{AAAA: ip.As16()}
					default:
						continue
					}
					m.Answers = append(m.Answers, rr)
				}
			}
			out, _ := m.Pack()
			pc.WriteTo(out, from)
		}
	}()
	return localnet.Resolver((&net.Dialer{}).DialContext, netip.MustParseAddrPort(pc.LocalAddr().String()))
}

func TestResolveRejectsBothFakePools(t *testing.T) {
	r := testResolver(t, "198.18.0.7", "fc00::7")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, host := range []string{"198.18.0.7", "fc00::7", "::ffff:198.18.0.7", "fake.test"} {
		if got, err := resolve(ctx, r, host, 443); err == nil {
			t.Errorf("accepted virtual address %s for %s", got, host)
		}
	}
	r = testResolver(t, "198.18.0.7", "fc00::7", "127.0.0.1")
	if got, err := resolve(ctx, r, "real.test", 443); err != nil || got.Addr() != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("real answer: %s %v", got, err)
	}
}

func TestFetchUsesExplicitLocalResolver(t *testing.T) {
	cert, pin := selfSigned(t)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); c.(*tls.Conn).Handshake() }()
		}
	}()
	r := testResolver(t, "127.0.0.1")
	n := node.Node{Protocol: node.Trojan, Host: "server.invalid", Port: l.Addr().(*net.TCPAddr).Port, Security: "tls", Insecure: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := Fetch(ctx, &n, nil, r)
	if err != nil || got != pin {
		t.Fatalf("local resolver not used: %q %v", got, err)
	}
	// With a selected interface but no DNS, do not fall back to the system resolver.
	if _, err := Fetch(ctx, &n, &localnet.Interface{}, nil); err == nil {
		t.Fatal("missing physical DNS silently fell back")
	}
}
