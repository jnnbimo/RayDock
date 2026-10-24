// Package certpin reads the certificate a node's server presents, to pin nodes whose share links ask to
// skip certificate verification (insecure / allowInsecure). Xray 26 removed that option; pinning the
// certificate seen now (pinnedPeerCertSha256) takes its place and is stricter: a server presenting
// another certificate later is refused.
package certpin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/quic-go/quic-go"
	"golang.org/x/crypto/blake2b"

	"raydock/internal/localnet"
	"raydock/internal/node"
)

// Needed reports whether n asks to skip certificate verification without a pin or a name to verify the
// certificate against (node.Node.Pin), on a TLS layer Fetch can reach: Hysteria2 (QUIC, salamander
// obfuscation at most), or TLS over TCP.
func Needed(n *node.Node) bool {
	if !n.Insecure || n.Pin() != "" || n.VerifyName != "" || n.Outbound != "" || n.FinalMask != "" {
		return false
	}
	switch n.Protocol {
	case node.Hysteria2:
		return n.Obfs == "" || n.Obfs == "salamander"
	case node.VLESS, node.VMess, node.Trojan:
		return n.Security == "tls" && n.Network != "kcp"
	case node.HTTP, node.SOCKS: // HTTPS proxies, and SOCKS over TLS
		return n.Security == "tls"
	}
	return false
}

// Fetch completes a TLS handshake with n's server as its outbound would, through iface (routing decides
// when nil), and returns the SHA-256 of the certificate presented, in hex. The caller can supply a
// resolver core for strict WFP; otherwise DNS is bound to iface, never the current TUN's system DNS.
func Fetch(ctx context.Context, n *node.Node, iface *localnet.Interface, resolver *net.Resolver) (string, error) {
	sni := n.SNI
	if sni == "" && n.Protocol != node.Hysteria2 && (n.Network == "ws" || n.Network == "xhttp" || n.Network == "httpupgrade") {
		sni = n.HostHeader // as the outbound does behind a CDN
	}
	if sni == "" {
		sni = n.Host
	}
	conf := &tls.Config{ServerName: sni, InsecureSkipVerify: true} // the certificate is what is wanted
	for p := range strings.SplitSeq(n.ALPN, ",") {
		if p = strings.TrimSpace(p); p != "" {
			conf.NextProtos = append(conf.NextProtos, p)
		}
	}
	if resolver == nil {
		resolver = net.DefaultResolver
		if iface != nil {
			if len(iface.DNS) == 0 {
				return "", errors.New("本地网络没有 DNS 服务器")
			}
			resolver = localnet.Resolver(localnet.Dialer(iface).DialContext, iface.DNS[0])
		}
	}
	addr, err := resolve(ctx, resolver, n.Host, n.Port)
	if err != nil {
		return "", err
	}
	var certs []*tlsCert
	if n.Protocol == node.Hysteria2 {
		if len(conf.NextProtos) == 0 {
			conf.NextProtos = []string{"h3"}
		}
		certs, err = quicCerts(ctx, addr, conf, n, iface)
	} else {
		certs, err = tcpCerts(ctx, addr, conf, iface)
	}
	if err != nil {
		return "", err
	}
	if len(certs) == 0 {
		return "", errors.New("服务器没有提供证书")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

type tlsCert struct{ Raw []byte }

func certsOf(s tls.ConnectionState) []*tlsCert {
	out := make([]*tlsCert, len(s.PeerCertificates))
	for i, c := range s.PeerCertificates {
		out[i] = &tlsCert{Raw: c.Raw}
	}
	return out
}

func tcpCerts(ctx context.Context, addr netip.AddrPort, conf *tls.Config, iface *localnet.Interface) ([]*tlsCert, error) {
	d := tls.Dialer{NetDialer: localnet.Dialer(iface), Config: conf}
	c, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return certsOf(c.(*tls.Conn).ConnectionState()), nil
}

func quicCerts(ctx context.Context, addr netip.AddrPort, conf *tls.Config, n *node.Node, iface *localnet.Interface) ([]*tlsCert, error) {
	network := "udp4"
	if addr.Addr().Is6() {
		network = "udp6"
	}
	pc, err := localnet.ListenPacket(ctx, iface, network)
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	// quic-go treats a *net.UDPConn specially (ECN, segmentation offload); on Windows that handshake got no
	// answer where the plain path did, so it only gets a net.PacketConn.
	pc = plainConn{pc}
	if n.Obfs == "salamander" {
		pc = &salamander{PacketConn: pc, key: []byte(n.ObfsPassword)}
	}
	c, err := quic.Dial(ctx, pc, net.UDPAddrFromAddrPort(addr), conf, &quic.Config{})
	if err != nil {
		return nil, err
	}
	defer c.CloseWithError(0, "")
	return certsOf(c.ConnectionState().TLS), nil
}

// plainConn hides the concrete type of a PacketConn.
type plainConn struct{ net.PacketConn }

// resolve only accepts real server addresses, whether literal or returned by the supplied resolver.
func resolve(ctx context.Context, resolver *net.Resolver, host string, port int) (netip.AddrPort, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if localnet.IsFakeIP(ip) {
			return netip.AddrPort{}, errors.New("服务器地址是 FakeDNS 虚拟地址")
		}
		return netip.AddrPortFrom(ip.Unmap(), uint16(port)), nil
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	for _, ip := range ips {
		if ip = ip.Unmap(); !localnet.IsFakeIP(ip) {
			return netip.AddrPortFrom(ip, uint16(port)), nil
		}
	}
	return netip.AddrPort{}, errors.New("无法解析 " + host + " 的真实地址")
}

// salamander is Hysteria's packet obfuscation: each packet is an 8-byte salt, then the payload XORed
// with BLAKE2b-256(password + salt), repeated.
type salamander struct {
	net.PacketConn
	key []byte
}

const saltLen = 8

func (s *salamander) mask(salt, data []byte) {
	k := blake2b.Sum256(append(append([]byte{}, s.key...), salt...))
	for i := range data {
		data[i] ^= k[i%len(k)]
	}
}

func (s *salamander) WriteTo(p []byte, addr net.Addr) (int, error) {
	buf := make([]byte, saltLen+len(p))
	if _, err := rand.Read(buf[:saltLen]); err != nil {
		return 0, err
	}
	copy(buf[saltLen:], p)
	s.mask(buf[:saltLen], buf[saltLen:])
	if _, err := s.PacketConn.WriteTo(buf, addr); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *salamander) ReadFrom(p []byte) (int, net.Addr, error) {
	buf := make([]byte, saltLen+len(p))
	for {
		n, addr, err := s.PacketConn.ReadFrom(buf)
		if err != nil {
			return 0, addr, err
		}
		if n <= saltLen {
			continue // not a packet of the server's
		}
		s.mask(buf[:saltLen], buf[saltLen:n])
		return copy(p, buf[saltLen:n]), addr, nil
	}
}

// Describe names a node and its server for logs.
func Describe(n *node.Node) string {
	return n.Label() + "（" + net.JoinHostPort(n.Host, strconv.Itoa(n.Port)) + "）"
}
