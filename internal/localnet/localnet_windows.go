package localnet

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strconv"
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
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_UNSPEC, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	links := map[uint64]*windows.IpAdapterAddresses{}
	for aa := first; aa != nil; aa = aa.Next {
		if aa.OperStatus == windows.IfOperStatusUp && physical[aa.IfType] {
			links[aa.Luid] = aa
		}
	}
	best := defaultLink(links, table.Rows())
	if best == nil {
		return nil, errors.New("没有可用的本地网络默认路由")
	}
	iface := &Interface{Index: int(best.IfIndex), Index6: int(best.Ipv6IfIndex), Name: windows.UTF16PtrToString(best.FriendlyName)}
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
		case !siteLocal.Contains(ip):
			if ip.IsLinkLocalUnicast() {
				ip = ip.WithZone(strconv.Itoa(iface.Index6))
			}
			v6 = append(v6, netip.AddrPortFrom(ip, 53))
		}
	}
	iface.DNS = append(iface.DNS, v6...)
	return iface, nil
}

// Windows adds the default route's metric to the interface metric. Prefer the best
// IPv4 physical route, or the best IPv6 route on an IPv6-only network.
func defaultLink(links map[uint64]*windows.IpAdapterAddresses, rows []windows.MibIpForwardRow2) *windows.IpAdapterAddresses {
	var best4, best6 *windows.IpAdapterAddresses
	var cost4, cost6 uint64
	for _, row := range rows {
		a := links[row.InterfaceLuid]
		if a == nil || row.DestinationPrefix.PrefixLength != 0 || row.Loopback != 0 || row.ValidLifetime == 0 {
			continue
		}
		switch row.DestinationPrefix.Prefix.Family {
		case windows.AF_INET:
			cost := uint64(row.Metric) + uint64(a.Ipv4Metric)
			if best4 == nil || cost < cost4 || cost == cost4 && a.IfIndex < best4.IfIndex {
				best4, cost4 = a, cost
			}
		case windows.AF_INET6:
			cost := uint64(row.Metric) + uint64(a.Ipv6Metric)
			if best6 == nil || cost < cost6 || cost == cost6 && a.Ipv6IfIndex < best6.Ipv6IfIndex {
				best6, cost6 = a, cost
			}
		}
	}
	if best4 != nil {
		return best4
	}
	return best6
}

var siteLocal = netip.MustParsePrefix("fec0::/10")

// IP_UNICAST_IF and IPV6_UNICAST_IF send a socket's packets out of one interface whatever the routes say.
const (
	ipUnicastIF   = 31
	ipv6UnicastIF = 31
)

func bind(iface *Interface) func(network, address string, c syscall.RawConn) error {
	return func(network, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			h := windows.Handle(fd)
			switch network {
			case "tcp6", "udp6":
				serr = windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIF, iface.Index6)
			default:
				// IPv4 takes the index in network byte order.
				var b [4]byte
				binary.BigEndian.PutUint32(b[:], uint32(iface.Index))
				serr = windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIF, int(binary.NativeEndian.Uint32(b[:])))
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}
