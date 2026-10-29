package xray

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A backend core counts the bytes of its outbounds, and ReadTraffic asks its API (Options.APIPort) for the
// counts: Xray's StatsService, over gRPC. A metrics listener would serve them too, but beside Go's
// profiling endpoints, open to every program on the computer. Only the outbounds traffic leaves by count:
// the proxy of the connection and of each dedicated port, and direct. The hops before them in a chain
// carry the same bytes again, wrapped, and blocked or DNS traffic leaves by none.

// Traffic is what a backend core moved since it started, in bytes. Up is sent to the destination.
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

// statsConfig makes a core count its outbounds, which its API serves (backendAPI).
func statsConfig(cfg obj) {
	cfg["stats"] = obj{}
	cfg["policy"] = obj{"system": obj{"statsOutboundUplink": true, "statsOutboundDownlink": true}}
}

// trafficKind tells where an outbound's traffic goes: proxy, direct, or neither ("").
func trafficKind(tag string) string {
	switch {
	case tag == tagProxy:
		return tagProxy
	case tag == tagDirect:
		return tagDirect
	case strings.HasPrefix(tag, portPrefix) && strings.HasSuffix(tag, "-"+tagProxy):
		return tagProxy // the end of a dedicated port's chain
	}
	return ""
}

// StatsClient returns a client for ReadTraffic: gRPC is HTTP/2, here without TLS, to 127.0.0.1.
func StatsClient() *http.Client {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Timeout: time.Second, Transport: &http.Transport{Protocols: p}}
}

// queryStats is the method ReadTraffic calls (app/stats/command/command.proto).
const queryStats = "/xray.app.stats.command.StatsService/QueryStats"

// ReadTraffic reads the counts of the core whose API is at 127.0.0.1:port, through client (StatsClient).
func ReadTraffic(ctx context.Context, client *http.Client, port int) (Traffic, error) {
	// QueryStatsRequest{pattern: "outbound>>>"}: field 1, a string. Counters are named
	// outbound>>>TAG>>>traffic>>>uplink and downlink.
	const pattern = "outbound>>>"
	msg := append([]byte{0x0a, byte(len(pattern))}, pattern...)
	// A gRPC message: not compressed, its length, then it.
	body := binary.BigEndian.AppendUint32([]byte{0}, uint32(len(msg)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", port, queryStats),
		bytes.NewReader(append(body, msg...)))
	if err != nil {
		return Traffic{}, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	resp, err := client.Do(req)
	if err != nil {
		return Traffic{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Traffic{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Traffic{}, err
	}
	// The status comes in the trailers, or in the headers of a response without a message.
	status := resp.Trailer.Get("Grpc-Status")
	if status == "" {
		status = resp.Header.Get("Grpc-Status")
	}
	if status != "0" {
		return Traffic{}, fmt.Errorf("gRPC 状态 %s：%s", status, resp.Trailer.Get("Grpc-Message"))
	}
	if len(data) < 5 || data[0] != 0 || int(binary.BigEndian.Uint32(data[1:5])) != len(data)-5 {
		return Traffic{}, errProto
	}
	return parseStats(data[5:])
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
