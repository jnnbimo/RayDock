package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The core serving the dedicated ports alone restarts when it exits, unless it ran only briefly, after
// which the next change starts it again.
func TestPortsCoreExit(t *testing.T) {
	a, _, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://127.0.0.1:9#a", "")
	gate := make(chan struct{})
	defer close(gate)
	a.coreDo(func() { <-gate })
	port, _ := freePort()
	if err := a.SetNodePort(a.State.Nodes[0].ID, port); err != nil {
		t.Fatal(err)
	}
	commit := func() *runningCore {
		liveFake(a, a.planned)
		a.running = a.planned
		return a.planned.core()
	}
	ports := commit()
	ports.since = time.Now()
	a.coreExited(ports, errors.New("crash"))
	if a.planned.core() != ports || !strings.Contains(a.PortErrs[a.State.Nodes[0].ID], "crash") {
		t.Fatalf("a brief core: errors %v", a.PortErrs)
	}
	a.SyncCores() // the next change
	again := a.planned.core()
	if again == ports || again == nil {
		t.Fatal("the next change should start the core again")
	}
	commit()
	again.since = time.Now().Add(-2 * portsRestartAfter)
	a.coreExited(again, errors.New("crash"))
	if p := a.planned.core(); p == again || p == nil || a.Conn != StateOff {
		t.Fatal("a core that ran a while restarts at once")
	}
}

// A dedicated port removed before the core got to starting it starts nothing.
func TestPortWithdrawnBeforeItsTurn(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://127.0.0.1:9#node", "")
	id := a.State.Nodes[0].ID
	gate, entered := make(chan struct{}), make(chan struct{})
	a.coreDo(func() { close(entered); <-gate })
	<-entered
	port, _ := freePort()
	if err := a.SetNodePort(id, port); err != nil {
		t.Fatal(err)
	}
	if r := a.planned.core(); r == nil || r.run.Chain != nil {
		t.Fatal("the core should start for the port alone")
	}
	if err := a.SetNodePort(id, 0); err != nil {
		t.Fatal(err)
	}
	close(gate)
	waitOperation(t, a.coreDo(func() {}))
	drainPosts(posts)
	if err := coreConfigWritten(a); err != nil || coresRunning(a) {
		t.Fatalf("the port withdrawn was started: %v", err)
	}
}
