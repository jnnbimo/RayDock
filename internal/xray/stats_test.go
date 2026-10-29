package xray

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"raydock/internal/node"
)

// Both kinds of backend core count their outbounds and serve the counts on their API, when they have
// one, and on nothing else: no metrics listener, which would serve Go's profiling endpoints too.
func TestStatsConfig(t *testing.T) {
	n := &node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Security: "tls"}
	for _, b := range []Backend{
		{Chain: []*node.Node{n}, Mixed: 23457},
		{Ports: []PortRoute{{Port: 20001, Chain: []*node.Node{n}}}, Mixed: 23457},
	} {
		o := DefaultOptions()
		o.APIPort = 23458
		data, err := BuildBackend(o, b)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Stats   map[string]any
			Metrics any
			API     struct {
				Listen   string
				Services []string
			}
			Policy struct {
				System map[string]bool
			}
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Stats == nil || cfg.Metrics != nil || cfg.API.Listen != "127.0.0.1:23458" || !slices.Contains(cfg.API.Services, "StatsService") ||
			!cfg.Policy.System["statsOutboundUplink"] || !cfg.Policy.System["statsOutboundDownlink"] {
			t.Fatalf("connected %v: %s", b.Connected(), data)
		}
		var plain map[string]any
		if data, _ := BuildBackend(DefaultOptions(), b); json.Unmarshal(data, &plain) != nil || plain["stats"] != nil || plain["api"] != nil {
			t.Fatal("no API, no counting")
		}
	}
}

// protoBytes encodes a length-delimited protobuf field.
func protoBytes(num int, b []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(num)<<3|2)
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

// stat encodes a Stat message of a QueryStatsResponse.
func stat(name string, value int64) []byte {
	m := protoBytes(1, []byte(name))
	m = binary.AppendUvarint(m, 2<<3) // field 2, wire type 0 (varint)
	m = binary.AppendUvarint(m, uint64(value))
	return protoBytes(1, m)
}

// statsServer answers QueryStats as a core's API does, over HTTP/2 without TLS, with the counters given.
func statsServer(t *testing.T, status string, counters map[string]int64) int {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.ProtoMajor != 2 || r.URL.Path != queryStats || r.Header.Get("Content-Type") != "application/grpc" ||
			len(body) < 5 || !strings.Contains(string(body[5:]), "outbound>>>") {
			t.Errorf("request %s %s %q", r.Proto, r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
		var msg []byte
		for name, v := range counters {
			msg = append(msg, stat(name, v)...)
		}
		w.Write(binary.BigEndian.AppendUint32([]byte{0}, uint32(len(msg))))
		w.Write(msg)
		w.Header().Set("Grpc-Status", status)
	}))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// Only the outbounds traffic leaves by count: a chain's earlier hops carry it again, wrapped.
func TestReadTraffic(t *testing.T) {
	port := statsServer(t, "0", map[string]int64{
		"inbound>>>relay>>>traffic>>>uplink":               9,
		"outbound>>>proxy>>>traffic>>>uplink":              100,
		"outbound>>>proxy>>>traffic>>>downlink":            1000,
		"outbound>>>chain-0>>>traffic>>>uplink":            120,
		"outbound>>>port-20001-proxy>>>traffic>>>uplink":   10,
		"outbound>>>port-20001-proxy>>>traffic>>>downlink": 20,
		"outbound>>>port-20001-chain-0>>>traffic>>>uplink": 11,
		"outbound>>>direct>>>traffic>>>uplink":             5,
		"outbound>>>direct>>>traffic>>>downlink":           50,
		"outbound>>>block>>>traffic>>>downlink":            1,
		"outbound>>>dns-out>>>traffic>>>uplink":            3,
	})
	got, err := ReadTraffic(context.Background(), StatsClient(), port)
	if want := (Traffic{ProxyUp: 110, ProxyDown: 1020, DirectUp: 5, DirectDown: 50}); err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	if d := (Traffic{ProxyUp: 5}).Since(Traffic{ProxyUp: 9, DirectUp: 1}); d != (Traffic{}) {
		t.Fatalf("counts never go back: %+v", d)
	}
}

// A failed call is an error, not zero traffic.
func TestReadTrafficError(t *testing.T) {
	port := statsServer(t, "14", nil)
	if _, err := ReadTraffic(context.Background(), StatsClient(), port); err == nil {
		t.Fatal("gRPC status 14 read as counts")
	}
	if _, err := parseStats([]byte{0x0a, 0x05, 1}); err == nil {
		t.Fatal("truncated message read")
	}
}
