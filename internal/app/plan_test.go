package app

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Issuing a core operation never waits, however many are queued before it.
func TestOperationQueueNeverBlocks(t *testing.T) {
	ops := newOperationQueue()
	defer close(ops)
	gate, entered := make(chan struct{}), make(chan struct{})
	queueOperation(ops, func() { close(entered); <-gate })
	<-entered
	issued := make(chan struct{})
	go func() {
		for range 200 {
			queueOperation(ops, func() {})
		}
		close(issued)
	}()
	select {
	case <-issued:
	case <-time.After(time.Second):
		t.Fatal("issuing waited for the operations before")
	}
	close(gate)
	waitOperation(t, queueOperation(ops, func() {}))
}

// Another node with the same connection settings is another node: the connection moves to it, its core
// restarted as for any other.
func TestEquivalentNodeIsConnected(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	a.ImportLinks("socks://127.0.0.1:9#first", "")
	other, err := a.AddGroup(GroupInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	a.ImportLinks("socks://127.0.0.1:9#second", other)
	first, second := a.State.Nodes[0].ID, a.State.Nodes[1].ID
	t.Cleanup(runningFake(t, a, first))
	a.Connect(second, nil)
	if a.ActiveID != second || a.State.LastID != second || a.ConnErr != "" {
		t.Fatalf("active %q last %q, err %q", a.ActiveID, a.State.LastID, a.ConnErr)
	}
	if a.planned == nil || a.planned.id != second || !a.planned.switched {
		t.Fatal("the core's target keeps the node switched from")
	}
}

// A dedicated port removed before the core got to starting it starts nothing.
func TestPortWithdrawnBeforeItsTurn(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
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
	drainRegression(posts)
	if err := coreConfigWritten(a); err != nil || coresRunning(a) {
		t.Fatalf("the port withdrawn was started: %v", err)
	}
}

// Typing in a settings field saves once the typing pauses, not on every key.
func TestSettingChangedSavesOnce(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	for i := range 20 {
		a.State.Settings.ProxyRules = strings.Repeat("x", i) + ".com"
		a.SettingChanged()
	}
	if _, err := os.Stat(a.statePath()); err == nil {
		t.Fatal("saved on a key")
	}
	waitRegression(t, posts, func() bool { return !a.saveDue })
	if raw, err := os.ReadFile(a.statePath()); err != nil || !strings.Contains(string(raw), strings.Repeat("x", 19)+".com") {
		t.Fatalf("the last edit was not saved: %v", err)
	}
}

// Importing says why nothing was imported, so the input can be corrected.
func TestImportResult(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	if r := a.ImportLinks("not a link", ""); r.Added != 0 || len(r.Invalid) != 1 || r.Problem() == "" {
		t.Fatalf("invalid input: %+v %q", r, r.Problem())
	}
	link := "socks://127.0.0.1:9#a"
	if r := a.ImportLinks(link+"\n"+link, ""); r.Added != 1 || r.Duplicate != 1 || r.Problem() != "" {
		t.Fatalf("a link twice: %+v", r)
	}
	if r := a.ImportLinks(link, ""); r.Added != 0 || r.Duplicate != 1 || !strings.Contains(r.Problem(), "已存在") {
		t.Fatalf("a link again: %+v %q", r, r.Problem())
	}
}
