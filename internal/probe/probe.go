// Package probe measures node latency and throughput.
package probe

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// TestURL returns 204 with no content.
	TestURL = "https://www.gstatic.com/generate_204"
	// DownloadURL sends the requested number of bytes; UploadURL accepts and discards a body.
	DownloadURL = "https://speed.cloudflare.com/__down?bytes=50000000" // the service refuses 100 MB and more
	UploadURL   = "https://speed.cloudflare.com/__up"
)

// TCP measures the time to open a TCP connection to addr with d. It only shows the port is reachable, not
// that the proxy works.
func TCP(ctx context.Context, d *net.Dialer, addr netip.AddrPort, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	conn.Close()
	return elapsed, nil
}

// ViaProxy requests target through the local HTTP proxy and returns the time to the response headers.
func ViaProxy(ctx context.Context, proxyPort int, target string, timeout time.Duration) (time.Duration, error) {
	client := ProxyClient(proxyPort)
	client.Timeout = timeout
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return elapsed, nil
}

// Through measures the round trip of a request through the local HTTP proxy at port over a connection
// already set up. A first request sets up the proxy's session to its server (for Hysteria2 or WireGuard
// the handshake) and the connection to target; a second goes over that connection, and its time to the
// response headers is returned: one trip to the server and on to target. It measures nodes reached over
// UDP, which a TCP handshake cannot.
func Through(ctx context.Context, port int, target string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	proxy := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), MaxIdleConnsPerHost: 1}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	get := func() (time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return 0, err
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		elapsed := time.Since(start)
		// Read to the end, so the connection is reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return elapsed, nil
	}
	if _, err := get(); err != nil {
		return 0, err
	}
	return get()
}

// IPURLs answer with the caller's public IP as plain text. ExitIP asks them in order; they are run by
// different providers, so one being unreachable through a node does not leave it without an answer.
var IPURLs = []string{"https://api.ipify.org", "https://ipinfo.io/ip"}

// ExitIP returns the public IP sites see for requests through the local HTTP proxy at port: the address
// traffic leaves the proxy's last node from. timeout bounds the whole lookup, all services together.
func ExitIP(ctx context.Context, port int, timeout time.Duration) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := ProxyClient(port)
	var errs []error
	for _, target := range IPURLs {
		ip, err := askIP(ctx, client, target)
		if err == nil {
			return ip, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return netip.Addr{}, errors.Join(errs...)
}

func askIP(ctx context.Context, client *http.Client, target string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return netip.Addr{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("%s: HTTP %d", target, resp.StatusCode)
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s: not an IP", target)
	}
	return ip.Unmap(), nil
}

// ProxyClient returns a client using the local HTTP proxy at port, without connection reuse.
func ProxyClient(port int) *http.Client {
	proxy := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
}

// connectTimeout bounds getting a transfer going: proxy handshakes and the response headers.
const connectTimeout = 15 * time.Second

// errWindowOver ends a download's measurement.
var errWindowOver = errors.New("measurement window over")

// Download reads target for up to window after the response headers arrive and returns bytes per second.
// A read blocked when window ends is cut short with the request.
func Download(ctx context.Context, client *http.Client, target string, window time.Duration) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout+window)
	defer cancel()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	start := time.Now()
	timer := time.AfterFunc(window, func() { stop(errWindowOver) })
	defer timer.Stop()
	buf := make([]byte, 64*1024)
	var total int64
	for time.Since(start) < window {
		n, err := resp.Body.Read(buf)
		total += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			if context.Cause(ctx) == errWindowOver {
				break // the measurement is over, not the transfer failed
			}
			return 0, err
		}
	}
	return rate(total, time.Since(start))
}

// Upload sends a body to target for up to window and returns bytes per second, timed from the first
// byte sent to the response.
func Upload(ctx context.Context, client *http.Client, target string, window time.Duration) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout+window)
	defer cancel()
	body := &timedBody{window: window}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	end := time.Now()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	start := body.start.Load()
	if start == 0 {
		return 0, errors.New("没有发送数据")
	}
	return rate(body.sent.Load(), end.Sub(time.Unix(0, start)))
}

func rate(bytes int64, d time.Duration) (float64, error) {
	if bytes == 0 || d <= 0 {
		return 0, errors.New("没有传输数据")
	}
	return float64(bytes) / d.Seconds(), nil
}

// uploadChunk is random so no layer can compress it.
var uploadChunk = func() []byte {
	b := make([]byte, 64*1024)
	_, _ = rand.Read(b)
	return b
}()

// timedBody yields random data until window has passed since the first read. The transport may still
// read it after the response arrived, so its counters are atomic.
type timedBody struct {
	window time.Duration
	start  atomic.Int64 // UnixNano of the first read
	sent   atomic.Int64
}

func (b *timedBody) Read(p []byte) (int, error) {
	start := b.start.Load()
	if start == 0 {
		b.start.Store(time.Now().UnixNano())
	} else if time.Since(time.Unix(0, start)) >= b.window {
		return 0, io.EOF
	}
	n := copy(p, uploadChunk)
	b.sent.Add(int64(n))
	return n, nil
}
