//go:build integration

package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"raydock/internal/xray"
)

// A handover cancelled after the new core took over leaves it running without its public listeners. The
// next plan may keep that same core: it must open them then, not treat the running core as complete.
func TestIntegrationKeptCoreOpensListeners(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.ReadCoreVersion = xray.Version
	port, _ := freePort()
	a.State.Settings.Port, a.State.Settings.SystemProxy = port, false
	a.ImportLinks("socks://127.0.0.1:9#node", "")
	a.Connect(a.State.Nodes[0].ID, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	if a.Conn != StateOn {
		t.Fatal(a.ConnErr)
	}
	listening := func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	old := a.running.core()
	next, err := planCore(nil, old.coreSpec, true)
	if err != nil {
		t.Fatal(err)
	}
	next.core = a.newCore()
	var startErr error
	waitOperation(t, a.coreDo(func() {
		if startErr = a.startProcess(context.Background(), next, true); startErr == nil {
			a.live.Store(next)
			a.stopProcess(old)
		}
	}))
	if startErr != nil {
		t.Fatal(startErr)
	}
	if listening() {
		t.Fatal("the candidate opened its listeners before activation")
	}
	var keepErr error
	waitOperation(t, a.coreDo(func() { keepErr = a.replaceProcess(context.Background(), next) }))
	if keepErr != nil || !listening() || next.inactive {
		t.Fatalf("kept core did not open its listeners: %v", keepErr)
	}
}
