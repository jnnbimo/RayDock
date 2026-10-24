package localnet

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"
)

func TestFind(t *testing.T) {
	iface, err := Find()
	if runtime.GOOS != "windows" {
		if iface != nil || err != nil {
			t.Fatalf("%+v %v", iface, err)
		}
		return
	}
	if err != nil {
		t.Skipf("no local network here: %v", err)
	}
	sys, err := net.InterfaceByIndex(iface.Index)
	if err != nil || sys.Name != iface.Name {
		t.Fatalf("interface %+v, system %+v %v", iface, sys, err)
	}
	t.Logf("local network: %s (#%d), DNS %v", iface.Name, iface.Index, iface.DNS)
}

// The resolver sends queries to its server only, through the dialer given.
func TestResolver(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go serveA(pc, netip.MustParseAddr("192.0.2.7"))
	server := netip.MustParseAddrPort(pc.LocalAddr().String())
	var dialed string
	r := Resolver(func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = address
		return Dialer(nil).DialContext(ctx, network, address)
	}, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := r.LookupNetIP(ctx, "ip4", "node.example")
	if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.7" || dialed != server.String() {
		t.Fatalf("ips %v err %v dialed %q", ips, err, dialed)
	}
}

// serveA answers every DNS query with one A record (and nothing for other types).
func serveA(pc net.PacketConn, ip netip.Addr) {
	buf := make([]byte, 512)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		q := buf[:n]
		// Header: ID, flags, counts; then the question up to its terminating zero label, type and class.
		end := 12
		for end < n && q[end] != 0 {
			end += int(q[end]) + 1
		}
		end += 5
		if end > n {
			continue
		}
		qtype := uint16(q[end-4])<<8 | uint16(q[end-3])
		resp := append([]byte{q[0], q[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}, q[12:end]...)
		if qtype == 1 {
			resp[7] = 1
			a := ip.As4()
			resp = append(resp, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, a[0], a[1], a[2], a[3])
		}
		pc.WriteTo(resp, from)
	}
}
