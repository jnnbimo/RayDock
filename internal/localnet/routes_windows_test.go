package localnet

import (
	"golang.org/x/sys/windows"
	"testing"
)

func TestDefaultLinkUsesRouteAndInterfaceMetrics(t *testing.T) {
	a := &windows.IpAdapterAddresses{IfIndex: 1, Ipv6IfIndex: 10, Ipv4Metric: 5, Ipv6Metric: 40}
	b := &windows.IpAdapterAddresses{IfIndex: 2, Ipv6IfIndex: 20, Ipv4Metric: 15, Ipv6Metric: 5}
	row := func(id uint64, family uint16, metric uint32) windows.MibIpForwardRow2 {
		return windows.MibIpForwardRow2{InterfaceLuid: id, DestinationPrefix: windows.IpAddressPrefix{Prefix: windows.RawSockaddrInet{Family: family}}, ValidLifetime: 1, Metric: metric}
	}
	links := map[uint64]*windows.IpAdapterAddresses{1: a, 2: b}
	rows := []windows.MibIpForwardRow2{row(1, windows.AF_INET, 50), row(2, windows.AF_INET, 10), row(3, windows.AF_INET, 0)}
	if defaultLink(links, rows) != b {
		t.Fatal("did not compare total metric or considered a virtual adapter")
	}
	rows = []windows.MibIpForwardRow2{row(1, windows.AF_INET6, 1), row(2, windows.AF_INET6, 7)}
	if defaultLink(links, rows) != b {
		t.Fatal("IPv6-only route chosen using IPv4 metric")
	}
}
