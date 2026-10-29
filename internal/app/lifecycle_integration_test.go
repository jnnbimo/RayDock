//go:build integration

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"raydock/internal/xray"
)

func TestIntegrationExitDuringFailedSwitch(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.State.Settings.Port, _ = freePort()
	a.State.Settings.SystemProxy = false
	a.ImportLinks("socks://127.0.0.1:9#working", "")
	a.ImportLinks(`{"protocol":"socks","settings":{"address":"127.0.0.1","port":10},"streamSettings":{"network":"no-such-transport"}}`, "")
	a.Connect(a.State.Nodes[0].ID, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOn || a.ConnErr != "" })
	if a.Conn != StateOn {
		t.Fatal(a.ConnErr)
	}
	old := a.run.core
	gate, entered := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	a.coreDo(func() { close(entered); <-gate })
	<-entered
	a.Connect(a.State.Nodes[1].ID, nil)
	proc, err := os.FindProcess(old.PID())
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil { // only the core created by this test
		t.Fatal(err)
	}
	// Let the waiter post the exit event, but keep it queued on the UI. This
	// exercises rollback as well as the exit callback's committed ownership.
	for deadline := time.Now().Add(3 * time.Second); old.Running(); {
		if time.Now().After(deadline) {
			t.Fatal("old backend did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	waitOperation(t, a.coreDo(func() {}))
	waitRegression(t, posts, func() bool { return a.Conn == StateOff })
	waitOperation(t, a.coreDo(func() {}))
	drainRegression(posts)
	if a.ConnErr == "" || coresRunning(a) {
		t.Fatalf("dead backend was restored or left running: state=%v error=%q", a.Conn, a.ConnErr)
	}
}

func TestIntegrationGeoDownloadBesideQueuedStart(t *testing.T) {
	root := os.Getenv("RAYDOCK_CORE_DIR")
	if root == "" {
		t.Skip("set RAYDOCK_CORE_DIR to an existing core")
	}
	a, posts, _ := newRegressionApp(t)
	if err := xray.CopyCore(root, a.CoreDir(), nil); err != nil {
		t.Fatal(err)
	}
	a.State.Settings.Port, _ = freePort()
	a.State.Settings.SystemProxy = false
	a.ImportLinks("socks://127.0.0.1:9#node", "")
	gap, finish := make(chan struct{}), make(chan struct{})
	finishDownload := sync.OnceFunc(func() { close(finish) })
	defer finishDownload()
	a.CoreSource = lifecycleGeoSource{install: func(ctx context.Context, dir string) error {
		if dir == a.CoreDir() {
			return fmt.Errorf("download unexpectedly targeted the live directory")
		}
		if err := xray.CopyGeo(root, dir); err != nil {
			return err
		}
		// Model the installer's multi-file gap in the download directory.
		for _, name := range xray.GeoFiles {
			if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".old")); err != nil {
				return err
			}
		}
		close(gap)
		select {
		case <-finish:
		case <-ctx.Done():
			return ctx.Err()
		}
		for _, name := range xray.GeoFiles {
			if err := os.Rename(filepath.Join(dir, name+".old"), filepath.Join(dir, name)); err != nil {
				return err
			}
		}
		return nil
	}}
	a.UpdateGeo()
	select {
	case <-gap:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach its staging gap")
	}
	gate, entered := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	a.coreDo(func() { close(entered); <-gate })
	<-entered
	a.Connect(a.State.Nodes[0].ID, nil)
	if a.WaitCore || !a.CoreReady() {
		t.Fatal("a staged download made the installed core unusable")
	}
	finishDownload()
	waitRegression(t, posts, func() bool { return a.coreHold })
	release()
	waitRegression(t, posts, func() bool { return !a.GeoBusy && a.Conn == StateOn || a.ConnErr != "" })
	waitOperation(t, a.coreDo(func() {}))
	drainRegression(posts)
	if a.Conn != StateOn || a.ConnErr != "" || a.GeoErr != "" || a.coreHold || !a.run.core.Running() {
		t.Fatalf("start/commit failed: state=%v connection=%q geo=%q hold=%v", a.Conn, a.ConnErr, a.GeoErr, a.coreHold)
	}
}
