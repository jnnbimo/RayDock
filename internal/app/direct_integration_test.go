//go:build integration

package app

import (
	"context"
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

// Private addresses, which the routing sends direct, are reached through the local port. Open to the LAN,
// loopback is refused.
func TestIntegrationPrivateDirect(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.ReadCoreVersion = xray.Version
	server, _ := freePort()
	core := xray.Core{}
	if err := core.Start(context.Background(), filepath.Join(a.CoreDir(), xray.ExeName()), filepath.Join(t.TempDir(), "server.json"), fmt.Appendf(nil,
		`{"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks"}],"outbounds":[{"protocol":"freedom"}]}`, server), nil); err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	l, err := net.Listen("tcp", "127.0.0.2:0") // private, not the proxy rule's 127.0.0.1
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("private")) })}
	go srv.Serve(l)
	defer srv.Close()
	target := "http://" + l.Addr().String() + "/"

	port, _ := freePort()
	a.State.Settings.Port, a.State.Settings.SystemProxy = port, false
	a.ImportLinks(fmt.Sprintf("socks://127.0.0.1:%d#a", server), "")
	get := func() (string, error) {
		c := probe.ProxyClient(a.ProxyPort())
		c.Timeout = 5 * time.Second
		resp, err := c.Get(target)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	a.Connect(a.State.Nodes[0].ID, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	if a.Conn != StateOn {
		t.Fatal(a.ConnErr)
	}
	if body, err := get(); err != nil || body != "private" {
		t.Fatalf("private address: %q %v", body, err)
	}

	a.State.Settings.AllowLAN = true
	a.SettingChanged()
	a.Reconnect()
	waitOperation(t, a.coreDo(func() {})) // the core restarted
	drainRegression(posts)
	if !a.running.opts.AllowLAN || a.SettingsDirty {
		t.Fatal("the LAN setting was not applied")
	}
	if body, err := get(); err == nil {
		t.Fatalf("loopback reached with the local port open to the LAN: %q", body)
	}
}
