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

// The core counts its outbounds and serves the counts on its API, when it has one, and on nothing else: no
// metrics listener, which would serve Go's profiling endpoints too, and no API listener without the
// per-process token. So does the core serving the dedicated ports alone.
func TestStatsConfig(t *testing.T) {
	n := &node.Node{Protocol: node.Trojan, Host: "t.example.com", Port: 443, Password: "p", Security: "tls"}
	builds := map[string]func(Options) ([]byte, error){
		"connection": func(o Options) ([]byte, error) { return Build(o, Run{Chain: []*node.Node{n}}) },
		"ports": func(o Options) ([]byte, error) {
			return Build(o, Run{Ports: []PortRoute{{Port: 20001, Chain: []*node.Node{n}}}})
		},
	}
	for name, build := range builds {
		o := DefaultOptions()
		o.APIPort = 23458
		if _, err := build(o); err == nil {
			t.Fatalf("%s: an API without a token", name)
		}
		o.APIToken = "token"
		data, err := build(o)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Stats    map[string]any
			Metrics  any
			Inbounds []configInbound
			API      struct {
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
		auth := slices.IndexFunc(cfg.Inbounds, func(in configInbound) bool {
			return in.Tag == tagAPIAuth && in.Listen == "127.0.0.1" && in.Port == 23458 &&
				len(in.Settings.Accounts) == 1 && in.Settings.Accounts[0].Pass == "token"
		})
		if cfg.Stats == nil || cfg.Metrics != nil || cfg.API.Listen != "" || auth < 0 || !slices.Contains(cfg.API.Services, "StatsService") ||
			!cfg.Policy.System["statsOutboundUplink"] || !cfg.Policy.System["statsOutboundDownlink"] {
			t.Fatalf("%s: %s", name, data)
		}
		var plain map[string]any
		if data, _ := build(DefaultOptions()); json.Unmarshal(data, &plain) != nil || plain["stats"] != nil || plain["api"] != nil {
			t.Fatalf("%s: no API, no counting", name)
		}
	}
	// The hops before the end of a chain carry the same bytes again: they do not count.
	if kind := trafficKind(nodePrefix + "0a1b2c3d-chain-0"); kind != "" {
		t.Errorf("a hop counted as %q", kind)
	}
}

// configInbound is the part of an inbound TestStatsConfig reads.
type configInbound struct {
	Tag, Listen string
	Port        int
	Settings    struct{ Accounts []struct{ User, Pass string } }
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

// plainClient reaches statsServer, which stands for the API behind the authenticated tunnel.
func plainClient() *http.Client {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: p}}
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

// Only the outbounds traffic leaves by count: a chain's earlier hops carry it again, wrapped. The chains
// of the connection and of each dedicated port count alike.
func TestReadTraffic(t *testing.T) {
	port := statsServer(t, "0", map[string]int64{
		"inbound>>>mixed-in>>>traffic>>>uplink":               9,
		"outbound>>>node-0a1b2c3d-proxy>>>traffic>>>uplink":   100,
		"outbound>>>node-0a1b2c3d-proxy>>>traffic>>>downlink": 1000,
		"outbound>>>node-0a1b2c3d-chain-0>>>traffic>>>uplink": 120,
		"outbound>>>node-99887766-proxy>>>traffic>>>uplink":   10,
		"outbound>>>node-99887766-proxy>>>traffic>>>downlink": 20,
		"outbound>>>node-99887766-chain-0>>>traffic>>>uplink": 11,
		"outbound>>>direct>>>traffic>>>uplink":                5,
		"outbound>>>direct>>>traffic>>>downlink":              50,
		"outbound>>>block>>>traffic>>>downlink":               1,
		"outbound>>>dns-out>>>traffic>>>uplink":               3,
	})
	got, err := ReadTraffic(context.Background(), plainClient(), port)
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
	if _, err := ReadTraffic(context.Background(), plainClient(), port); err == nil {
		t.Fatal("gRPC status 14 read as counts")
	}
	if _, err := parseStats([]byte{0x0a, 0x05, 1}); err == nil {
		t.Fatal("truncated message read")
	}
}
