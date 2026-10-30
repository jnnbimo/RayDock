package xray

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"time"
)

// The core counts the bytes of its outbounds, and ReadTraffic asks its API (Options.APIPort) for the
// counts: Xray's StatsService, over gRPC. A metrics listener would serve them too, but beside Go's
// profiling endpoints, open to every program on the computer. Only the outbounds traffic leaves by count:
// the proxy of the connection and of each dedicated port, and direct. The hops before them in a chain
// carry the same bytes again, wrapped, and blocked or DNS traffic leaves by none.

// Traffic is what a core moved since it started, in bytes. Up is sent to the destination.
type Traffic struct {
	ProxyUp    int64 `json:"proxyUp,omitempty"`
	ProxyDown  int64 `json:"proxyDown,omitempty"`
	DirectUp   int64 `json:"directUp,omitempty"`
	DirectDown int64 `json:"directDown,omitempty"`
}

// Add returns t with u added.
func (t Traffic) Add(u Traffic) Traffic {
	return Traffic{t.ProxyUp + u.ProxyUp, t.ProxyDown + u.ProxyDown, t.DirectUp + u.DirectUp, t.DirectDown + u.DirectDown}
}

// Since returns what was moved since prev, an earlier reading of the same core.
func (t Traffic) Since(prev Traffic) Traffic {
	d := func(cur, old int64) int64 { return max(cur-old, 0) }
	return Traffic{d(t.ProxyUp, prev.ProxyUp), d(t.ProxyDown, prev.ProxyDown), d(t.DirectUp, prev.DirectUp), d(t.DirectDown, prev.DirectDown)}
}

func (t Traffic) Zero() bool { return t == Traffic{} }

// statsConfig makes a core count its outbounds, which its API serves (coreAPI).
func statsConfig(cfg obj) {
	cfg["stats"] = obj{}
	cfg["policy"] = obj{"system": obj{"statsOutboundUplink": true, "statsOutboundDownlink": true}}
}

// trafficKind tells where an outbound's traffic goes: proxy, direct, or neither ("").
func trafficKind(tag string) string {
	switch {
	case strings.HasPrefix(tag, nodePrefix) && strings.HasSuffix(tag, "-"+tagProxy):
		return tagProxy // the end of a chain, the connection's or a dedicated port's (nodeOutbounds)
	case tag == tagDirect:
		return tagDirect
	}
	return ""
}

// StatsClient returns a client for the API of the core started with token (Options.APIToken): gRPC is
// HTTP/2, here without TLS, through the core's authenticated loopback tunnel (apiDial).
func StatsClient(token string) *http.Client {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Timeout: time.Second, Transport: &http.Transport{Protocols: p, DialContext: apiDial(token)}}
}

// queryStats is the method ReadTraffic calls (app/stats/command/command.proto).
const queryStats = "/xray.app.stats.command.StatsService/QueryStats"

// ReadTraffic reads the counts of the core whose API is at 127.0.0.1:port, through client (StatsClient).
func ReadTraffic(ctx context.Context, client *http.Client, port int) (Traffic, error) {
	data, err := rpc(ctx, client, port, queryStats, wireBytes(1, []byte("outbound>>>")))
	if err != nil {
		return Traffic{}, err
	}
	return parseStats(data)
}

// parseStats reads a QueryStatsResponse: Stat messages (field 1), each a name (1) and a value (2).
func parseStats(b []byte) (Traffic, error) {
	var t Traffic
	for len(b) > 0 {
		stat, err := protoField(&b)
		if err != nil {
			return Traffic{}, err
		}
		if stat.num != 1 || stat.bytes == nil {
			continue
		}
		var name string
		var value int64
		for m := stat.bytes; len(m) > 0; {
			f, err := protoField(&m)
			if err != nil {
				return Traffic{}, err
			}
			switch {
			case f.num == 1 && f.bytes != nil:
				name = string(f.bytes)
			case f.num == 2 && f.bytes == nil:
				value = int64(f.varint)
			}
		}
		parts := strings.Split(name, ">>>")
		if len(parts) != 4 || parts[0] != "outbound" || parts[2] != "traffic" {
			continue
		}
		up := parts[3] == "uplink"
		switch trafficKind(parts[1]) {
		case tagProxy:
			if up {
				t.ProxyUp += value
			} else {
				t.ProxyDown += value
			}
		case tagDirect:
			if up {
				t.DirectUp += value
			} else {
				t.DirectDown += value
			}
		}
	}
	return t, nil
}

// protoValue is a protobuf field: its number, and its value, a varint or, for a length-delimited field,
// bytes (not nil).
type protoValue struct {
	num    uint64
	varint uint64
	bytes  []byte
}

var errProto = errors.New("统计接口的回应格式错误")

// protoField reads the field at the start of *b and moves *b past it. Only the wire types of
// QueryStatsResponse are known: varints and length-delimited fields.
func protoField(b *[]byte) (protoValue, error) {
	key, n := binary.Uvarint(*b)
	if n <= 0 {
		return protoValue{}, errProto
	}
	*b = (*b)[n:]
	v := protoValue{num: key >> 3}
	switch key & 7 {
	case 0:
		if v.varint, n = binary.Uvarint(*b); n <= 0 {
			return protoValue{}, errProto
		}
		*b = (*b)[n:]
	case 2:
		size, n := binary.Uvarint(*b)
		if n <= 0 || size > uint64(len(*b)-n) {
			return protoValue{}, errProto
		}
		v.bytes = (*b)[n : n+int(size) : n+int(size)]
		*b = (*b)[n+int(size):]
	default:
		return protoValue{}, errProto
	}
	return v, nil
}
