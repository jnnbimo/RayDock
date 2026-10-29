//go:build integration

package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"raydock/internal/probe"
	"raydock/internal/store"
	"raydock/internal/xray"
)

func TestIntegrationFinalTrafficOnDisconnectAndShutdown(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint("disconnect=", disconnect), func(t *testing.T) {
			a, posts, _ := newRegressionApp(t)
			if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
				t.Fatal(err)
			}
			a.State.Settings.Port, _ = freePort()
			a.State.Settings.SystemProxy = false
			a.State.Settings.Mode = xray.ModeDirect
			a.ImportLinks("socks://127.0.0.1:9", "")
			a.Connect(a.State.Nodes[0].ID, nil)
			waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
			if a.Conn != StateOn {
				t.Fatal(a.ConnErr)
			}
			const size = 64 << 10
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(make([]byte, size))
			}))
			defer srv.Close()
			a.StartTraffic()
			client := probe.ProxyClient(a.ProxyPort())
			client.Timeout = 3 * time.Second
			resp, err := client.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			n, err := io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err != nil || n != size {
				t.Fatalf("transfer incomplete: %d %v", n, err)
			}
			// Do not pump the UI or wait for the one-second polling tick.
			if disconnect {
				a.Disconnect()
				waitOperation(t, a.coreDo(func() {}))
				a.flushTraffic()
				if a.TrafficOn(time.Now()).DirectDown < size {
					t.Fatal("disconnect lost its final sample")
				}
			}
			a.Shutdown()
			saved, err := store.LoadTraffic(a.trafficPath())
			got := saved.Days[time.Now().Format(time.DateOnly)].DirectDown
			if err != nil || got < size || got >= 2*size {
				t.Fatalf("final sample missing or counted twice: %d %v", got, err)
			}
		})
	}
}
