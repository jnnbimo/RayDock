package probe

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Through times a second request over the connection the first one set up.
func TestThrough(t *testing.T) {
	var conns, requests atomic.Int32
	// A forward proxy answering every request itself; the first, like a handshake, is slow.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			time.Sleep(300 * time.Millisecond)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	d, err := Through(context.Background(), port, "http://target.example/generate_204", 5*time.Second)
	if err != nil || d >= 300*time.Millisecond { // a loopback trip may round to 0 on Windows' clock
		t.Fatalf("%v %v", d, err)
	}
	if requests.Load() != 2 || conns.Load() != 1 {
		t.Fatalf("%d requests over %d connections", requests.Load(), conns.Load())
	}
	if _, err := Through(context.Background(), freeClosedPort(t), "http://target.example/", time.Second); err == nil {
		t.Error("no proxy should fail")
	}
}

func freeClosedPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestDownloadUpload(t *testing.T) {
	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/down":
			chunk := make([]byte, 32*1024)
			for {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		case "/up":
			n, _ := io.Copy(io.Discard, r.Body)
			received.Store(n)
		case "/short":
			w.Write(make([]byte, 8<<20))
		case "/stall": // a byte, then nothing for long
			w.Write([]byte{1})
			w.(http.Flusher).Flush()
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	start := time.Now()
	down, err := Download(ctx, srv.Client(), srv.URL+"/down", 300*time.Millisecond)
	if err != nil || down <= 0 {
		t.Fatalf("download %v %v", down, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("download should stop after the window, took %v", d)
	}
	// A body ending within the window ends the measurement early.
	start = time.Now()
	if r, err := Download(ctx, srv.Client(), srv.URL+"/short", time.Minute); err != nil || r <= 0 || time.Since(start) > 10*time.Second {
		t.Errorf("short download %v %v after %v", r, err, time.Since(start))
	}
	// A read blocked when the window ends is cut short: the window bounds the measurement.
	start = time.Now()
	if r, err := Download(ctx, srv.Client(), srv.URL+"/stall", 100*time.Millisecond); err != nil || r <= 0 || time.Since(start) > time.Second {
		t.Errorf("stalled download %v %v after %v", r, err, time.Since(start))
	}
	if _, err := Download(ctx, srv.Client(), srv.URL+"/missing", time.Second); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing %v", err)
	}

	up, err := Upload(ctx, srv.Client(), srv.URL+"/up", 300*time.Millisecond)
	if err != nil || up <= 0 || received.Load() == 0 {
		t.Fatalf("upload %v %v, server got %d bytes", up, err, received.Load())
	}

	// Cancelling stops a transfer at once.
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := Download(cctx, srv.Client(), srv.URL+"/down", time.Minute); err == nil {
		t.Error("cancelled download should fail")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("cancel took %v", d)
	}
}

// ExitIP asks the IP services in order through the proxy, going on to the next when one fails, and
// rejects an answer that is not an IP.
func TestExitIP(t *testing.T) {
	answers := map[string]func(w http.ResponseWriter){
		"first.test":  func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) },
		"second.test": func(w http.ResponseWriter) { io.WriteString(w, "203.0.113.7\n") },
		"junk.test":   func(w http.ResponseWriter) { io.WriteString(w, "<html>blocked</html>") },
	}
	// A forward proxy answering for the hosts above.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answers[r.URL.Host](w)
	}))
	defer proxy.Close()
	port := proxy.Listener.Addr().(*net.TCPAddr).Port
	defer func(saved []string) { IPURLs = saved }(IPURLs)

	IPURLs = []string{"http://first.test/", "http://second.test/"}
	ip, err := ExitIP(context.Background(), port, 5*time.Second)
	if err != nil || ip.String() != "203.0.113.7" {
		t.Fatalf("ip %v err %v", ip, err)
	}
	IPURLs = []string{"http://junk.test/"}
	if ip, err := ExitIP(context.Background(), port, 5*time.Second); err == nil {
		t.Fatalf("junk answer taken as %v", ip)
	}
}
