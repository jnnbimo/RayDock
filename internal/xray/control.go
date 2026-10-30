package xray

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// apiUser and tagAPIAuth name the authenticated API tunnel (coreAPI, apiDial).
const (
	apiUser    = "raydock"
	tagAPIAuth = "api-auth"
)

// finishRun adds the API to a run's config and, deferred, leaves out the public listeners, which
// ActivateListeners opens later.
func finishRun(cfg obj, o Options, deferred bool) {
	if deferred {
		ins := cfg["inbounds"].([]any)
		kept := make([]any, 0, len(ins))
		for _, in := range ins {
			if in.(obj)["protocol"] != "socks" {
				kept = append(kept, in)
			}
		}
		cfg["inbounds"] = kept
	}
	coreAPI(cfg, o)
}

// apiDial authenticates a loopback HTTP CONNECT tunnel to Xray's API. No unauthenticated HandlerService
// listener is exposed: the HTTP inbound requires a random per-process token and routes only to the API.
func apiDial(token string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { c.Close() })
		ok := false
		defer func() {
			stop()
			if !ok {
				c.Close()
			}
		}()
		deadline := time.Now().Add(5 * time.Second)
		if d, yes := ctx.Deadline(); yes && d.Before(deadline) {
			deadline = d
		}
		c.SetDeadline(deadline)
		auth := base64.StdEncoding.EncodeToString([]byte(apiUser + ":" + token))
		if _, err = fmt.Fprintf(c, "CONNECT 127.0.0.1:1 HTTP/1.1\r\nHost: 127.0.0.1:1\r\nProxy-Authorization: Basic %s\r\n\r\n", auth); err != nil {
			return nil, err
		}
		reader := bufio.NewReader(c)
		resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("核心接口认证失败：HTTP %d", resp.StatusCode)
		}
		if reader.Buffered() != 0 {
			return nil, errors.New("核心接口握手包含意外数据")
		}
		if err = c.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
		ok = true
		return c, nil
	}
}

// rpc calls the small subset of Xray's public gRPC API used for traffic and listener activation.
func rpc(ctx context.Context, client *http.Client, port int, method string, msg []byte) ([]byte, error) {
	body := binary.BigEndian.AppendUint32([]byte{0}, uint32(len(msg)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", port, method), bytes.NewReader(append(body, msg...)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	status := resp.Trailer.Get("Grpc-Status")
	if status == "" {
		status = resp.Header.Get("Grpc-Status")
	}
	message := resp.Trailer.Get("Grpc-Message")
	if message == "" {
		message = resp.Header.Get("Grpc-Message")
	}
	if status != "0" {
		return nil, fmt.Errorf("gRPC 状态 %s：%s", status, message)
	}
	if len(data) < 5 || data[0] != 0 || int(binary.BigEndian.Uint32(data[1:5])) != len(data)-5 {
		return nil, errProto
	}
	return data[5:], nil
}

// wireBytes/wireInt encode the published protobuf schema, without importing the entire Xray core. The
// fields below are from core/config.proto, app/proxyman/config.proto, proxy/socks/config.proto and
// transport/internet/config.proto. Integration tests validate them against the bundled Xray.
func wireBytes(n uint64, b []byte) []byte {
	v := binary.AppendUvarint(nil, n<<3|2)
	v = binary.AppendUvarint(v, uint64(len(b)))
	return append(v, b...)
}
func wireInt(n, v uint64) []byte { return binary.AppendUvarint(binary.AppendUvarint(nil, n<<3), v) }
func typed(name string, b []byte) []byte {
	return append(wireBytes(1, []byte(name)), wireBytes(2, b)...)
}

// ActivateListeners creates the public, exclusively-owned mixed listeners after the old core released
// them. It only adds RayDock's fixed SOCKS/HTTP inbounds; it never replaces outbounds or routing.
func ActivateListeners(ctx context.Context, client *http.Client, port int, config []byte) error {
	var cfg struct {
		Inbounds []struct {
			Tag, Protocol, Listen string
			Port                  int
			Sniffing              json.RawMessage
		}
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return err
	}
	for _, in := range cfg.Inbounds {
		if in.Protocol != "socks" {
			continue
		}
		address := net.ParseIP(in.Listen).To4()
		if address == nil || in.Port < 1 || in.Port > 65535 {
			return errors.New("无效的代理监听配置")
		}
		pr := append(wireInt(1, uint64(in.Port)), wireInt(2, uint64(in.Port))...)
		receiver := append(wireBytes(1, wireBytes(1, pr)), wireBytes(2, wireBytes(1, address))...)
		stream := wireBytes(5, []byte("tcp"))
		if Overlap {
			var custom []byte
			for i, s := range []string{"windows", "", "65535", "-5", "1", "int"} {
				if s != "" {
					custom = append(custom, wireBytes(uint64(i+1), []byte(s))...)
				}
			}
			stream = append(stream, wireBytes(6, wireBytes(20, custom))...)
		}
		receiver = append(receiver, wireBytes(3, stream)...)
		if len(in.Sniffing) > 0 {
			var sn struct {
				Enabled, MetadataOnly, RouteOnly bool
				DestOverride                     []string
			}
			if err := json.Unmarshal(in.Sniffing, &sn); err != nil {
				return err
			}
			var sniff []byte
			if sn.Enabled {
				sniff = append(sniff, wireInt(1, 1)...)
			}
			for _, s := range sn.DestOverride {
				sniff = append(sniff, wireBytes(2, []byte(s))...)
			}
			if sn.MetadataOnly {
				sniff = append(sniff, wireInt(4, 1)...)
			}
			if sn.RouteOnly {
				sniff = append(sniff, wireInt(5, 1)...)
			}
			receiver = append(receiver, wireBytes(6, sniff)...)
		}
		inbound := wireBytes(1, []byte(in.Tag))
		inbound = append(inbound, wireBytes(2, typed("xray.app.proxyman.ReceiverConfig", receiver))...)
		inbound = append(inbound, wireBytes(3, typed("xray.proxy.socks.ServerConfig", wireInt(4, 1)))...)
		if _, err := rpc(ctx, client, port, "/xray.app.proxyman.command.HandlerService/AddInbound", wireBytes(1, inbound)); err != nil {
			return fmt.Errorf("启用端口 %s 失败：%w", strconv.Itoa(in.Port), err)
		}
	}
	return nil
}
