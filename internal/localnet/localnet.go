// Package localnet reaches the network straight through the physical interface, past any TUN (RayDock's
// own or another VPN's), so measurements reflect the local network and never a proxy.
package localnet

import (
	"context"
	"net"
	"net/netip"
)

// FakeDNS pools are shared by configuration generation and network-boundary validation.
const (
	FakeDNS4 = "198.18.0.0/15"
	FakeDNS6 = "fc00::/18"
)

var fakeRanges = [...]netip.Prefix{netip.MustParsePrefix(FakeDNS4), netip.MustParsePrefix(FakeDNS6)}

// IsFakeIP rejects virtual answers, including IPv4-mapped IPv6 addresses.
func IsFakeIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return fakeRanges[0].Contains(ip) || fakeRanges[1].Contains(ip)
}

// Interface is the physical interface of the default route.
type Interface struct {
	Index int
	Name  string           // as net.InterfaceByName and Xray's sockopt.interface know it
	DNS   []netip.AddrPort // its DNS servers, IPv4 first
}

// Dialer returns a dialer whose sockets are bound to iface; with a nil iface routing decides.
func Dialer(iface *Interface) *net.Dialer {
	d := &net.Dialer{}
	if iface != nil {
		d.Control = bind(iface.Index)
	}
	return d
}

// ListenPacket opens a UDP socket (network udp4 or udp6) whose packets leave through iface; with a nil
// iface routing decides.
func ListenPacket(ctx context.Context, iface *Interface, network string) (net.PacketConn, error) {
	lc := net.ListenConfig{}
	if iface != nil {
		lc.Control = bind(iface.Index)
	}
	return lc.ListenPacket(ctx, network, ":0")
}

// Resolver returns a resolver that sends every query to server, connecting with dial.
func Resolver(dial func(ctx context.Context, network, address string) (net.Conn, error), server netip.AddrPort) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dial(ctx, network, server.String())
		},
	}
}
