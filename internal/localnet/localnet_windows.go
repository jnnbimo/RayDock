package localnet

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// physical lists the interface types of local links: Ethernet, PPP (PPPoE dial-up), Wi-Fi and mobile
// broadband. TUN adapters such as Wintun are virtual and so never chosen.
var physical = map[uint32]bool{6: true, 23: true, 71: true, 243: true, 244: true}

// Find returns the physical interface that is up, has a gateway and the lowest metric.
func Find() (*Interface, error) {
	size := uint32(16 << 10)
	var first *windows.IpAdapterAddresses
	for {
		buf := make([]byte, size)
		first = (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST, 0, first, &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return nil, err
		}
	}
	var best *windows.IpAdapterAddresses
	for aa := first; aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp || aa.FirstGatewayAddress == nil || !physical[aa.IfType] {
			continue
		}
		if best == nil || aa.Ipv4Metric < best.Ipv4Metric {
			best = aa
		}
	}
	if best == nil {
		return nil, errors.New("没有可用的本地网络")
	}
	iface := &Interface{Index: int(best.IfIndex), Name: windows.UTF16PtrToString(best.FriendlyName)}
	if iface.Index == 0 {
		iface.Index = int(best.Ipv6IfIndex) // IPv6-only link
	}
	var v6 []netip.AddrPort
	for s := best.FirstDnsServerAddress; s != nil; s = s.Next {
		ip, ok := netip.AddrFromSlice(s.Address.IP())
		if !ok {
			continue
		}
		ip = ip.Unmap()
		switch {
		case ip.Is4():
			iface.DNS = append(iface.DNS, netip.AddrPortFrom(ip, 53))
		case !ip.IsLinkLocalUnicast() && !siteLocal.Contains(ip): // these need a zone or are deprecated
			v6 = append(v6, netip.AddrPortFrom(ip, 53))
		}
	}
	iface.DNS = append(iface.DNS, v6...)
	return iface, nil
}

var siteLocal = netip.MustParsePrefix("fec0::/10")

// IP_UNICAST_IF and IPV6_UNICAST_IF send a socket's packets out of one interface whatever the routes say.
const (
	ipUnicastIF   = 31
	ipv6UnicastIF = 31
)

func bind(index int) func(network, address string, c syscall.RawConn) error {
	return func(network, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			h := windows.Handle(fd)
			switch network {
			case "tcp6", "udp6":
				serr = windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIF, index)
			default:
				// IPv4 takes the index in network byte order.
				var b [4]byte
				binary.BigEndian.PutUint32(b[:], uint32(index))
				serr = windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIF, int(binary.NativeEndian.Uint32(b[:])))
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}
