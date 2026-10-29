//go:build integration

package app

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"raydock/internal/probe"
	"raydock/internal/xray"
)

// The backend cores count what goes through the proxy once, across a backend core replaced by another
// node. Local processes only: direct traffic cannot be, as the backend core's direct outbound refuses
// private addresses (Xray's freedom does so behind a VLESS inbound). RAYDOCK_DIRECT_URL, a site the
// rules send direct, checks it too.
func TestIntegrationTraffic(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.ReadCoreVersion = xray.Version
	// The nodes: SOCKS inbounds of a local server core.
	var servers [2]int
	for i := range servers {
		servers[i], _ = freePort()
	}
	server := xray.Core{}
	if err := server.Start(filepath.Join(a.CoreDir(), xray.ExeName()), filepath.Join(t.TempDir(), "server.json"), fmt.Appendf(nil, `{"log":{"loglevel":"warning"},
		"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks"},{"listen":"127.0.0.1","port":%d,"protocol":"socks"}],
		"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}]}`, servers[0], servers[1])); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	const proxied = 3 << 20
	serve := func(ip string, size int) string {
		l, err := net.Listen("tcp", ip+":0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, size)) })}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Close() })
		return "http://" + l.Addr().String() + "/"
	}
	proxyURL, directURL := serve("127.0.0.1", proxied), os.Getenv("RAYDOCK_DIRECT_URL")

	port, _ := freePort()
	a.State.Settings.Port = port
	a.State.Settings.SystemProxy = false
	a.State.Settings.ProxyRules = "127.0.0.1/32"
	a.ImportLinks(fmt.Sprintf("socks://127.0.0.1:%d#a\nsocks://127.0.0.1:%d#b", servers[0], servers[1]), "")
	a.StartTraffic()
	fetch := func(target string) int64 {
		t.Helper()
		client := probe.ProxyClient(port)
		client.Timeout = 20 * time.Second
		resp, err := client.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if n == 0 {
			t.Fatalf("%s: nothing", target)
		}
		return n
	}
	// Headers and the proxy's own requests add a little; a chain counted twice would double it.
	near := func(got, want int64) bool { return got >= want && got < want+want/10+4096 }
	counted := func(wantProxy, wantDirect int64) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			got := a.TrafficOn(time.Now())
			if near(got.ProxyDown, wantProxy) && (wantDirect == 0 || near(got.DirectDown, wantDirect)) {
				t.Logf("counted %+v", got)
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("counted %+v, want proxy %d and direct %d", got, wantProxy, wantDirect)
			}
			select {
			case f := <-posts:
				f()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	a.Connect(a.State.Nodes[0].ID, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	if a.Conn != StateOn {
		t.Fatal(a.ConnErr)
	}
	fetch(proxyURL)
	var direct int64
	if directURL != "" {
		direct = fetch(directURL)
	}
	counted(proxied, direct)

	// Another node runs on a new backend core, whose counts add to those before.
	old := a.run.core
	a.Connect(a.State.Nodes[1].ID, nil)
	eventually(t, posts, func() bool { return a.run.core != old && a.run.core.Running() && !old.Running() })
	fetch(proxyURL)
	counted(2*proxied, direct)
	if n := len(a.Rates); n == 0 {
		t.Fatal("no rates")
	}
}
