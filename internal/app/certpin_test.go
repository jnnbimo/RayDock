package app

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"raydock/internal/node"
	"raydock/internal/xray"
)

func TestPinEditedWhileInFlight(t *testing.T) {
	a, posts, _ := newTestApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	a.CertPin = func(ctx context.Context, n *node.Node) (string, error) {
		if n.Host == "old.localhost" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "old-pin", nil
		}
		return "new-pin", nil
	}
	a.ImportLinks("trojan://p@old.localhost:443?allowInsecure=1", "")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	id := a.State.Nodes[0].ID
	edited := *a.NodeByID(id)
	edited.Host = "new.localhost"
	if err := a.EditNode(edited); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitFor(t, posts, func() bool { return a.NodeByID(id).AutoPin == "new-pin" })
	if a.pinning[id] {
		t.Fatal("completed pin still busy")
	}
}

func TestCoreReplacementCancelsAndResumesPinWorkers(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	started, cancelled := make(chan struct{}), make(chan struct{})
	var attempt atomic.Int32
	a.CertPin = func(ctx context.Context, _ *node.Node) (string, error) {
		if attempt.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			if b, _ := os.ReadFile(a.coreExe()); string(b) == "new" {
				t.Error("files replaced before the old pin worker released them")
			}
			close(cancelled)
			return "", ctx.Err()
		}
		if b, _ := os.ReadFile(a.coreExe()); string(b) != "new" {
			t.Error("pin retry ran before replacement")
		}
		return "pin", nil
	}
	a.ReadCoreVersion = func(string) (string, error) { return xray.MinTUNVersion, nil }
	a.ImportLinks("trojan://p@localhost:443?allowInsecure=1", "")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pin not started")
	}
	src := coreFilesIn(t, "new")
	a.coreJob("install", "安装", func(*http.Client) (string, error) { return src, nil })
	waitFor(t, posts, func() bool { return !a.CoreBusy && a.State.Nodes[0].AutoPin == "pin" })
	select {
	case <-cancelled:
	default:
		t.Fatal("the previous pin batch was not cancelled")
	}
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "new" {
		t.Fatal("replacement never completed")
	}
	if attempt.Load() != 2 {
		t.Fatalf("pin attempts %d, want one cancelled and one retried", attempt.Load())
	}
}
