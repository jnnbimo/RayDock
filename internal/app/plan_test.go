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

// Targets set while their operation waits replace each other: only the newest is carried out, with what
// those replaced asked for merged in. One set after a barrier is carried out after it.
func TestLatestCarriesNewestInOrder(t *testing.T) {
	ops := newOperationQueue()
	defer close(ops)
	queue := func(op func()) <-chan struct{} { return queueOperation(ops, op) }
	gate, entered := make(chan struct{}), make(chan struct{})
	queueOperation(ops, func() { close(entered); <-gate })
	<-entered
	var l latest[[]string]
	var ran []string
	run := func(t *[]string) { ran = append(ran, strings.Join(*t, "+")) }
	merge := func(old, t *[]string) { *t = append(*old, *t...) }
	l.set(&[]string{"a"}, merge, queue, run)
	l.set(&[]string{"b"}, merge, queue, run)
	l.barrier()
	queueOperation(ops, func() { ran = append(ran, "barrier") })
	done := l.set(&[]string{"c"}, merge, queue, run)
	close(gate)
	waitOperation(t, done)
	if got := strings.Join(ran, ","); got != "barrier,a+b+c" {
		t.Fatalf("ran %s", got)
	}
}

// Another node with the same connection settings needs no other core, but is the node connected now.
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
	run := a.run.core
	a.Connect(second, nil)
	if a.ActiveID != second || a.State.LastID != second || a.run.core != run || a.ConnErr != "" {
		t.Fatalf("active %q last %q, same backend %v, err %q", a.ActiveID, a.State.LastID, a.run.core == run, a.ConnErr)
	}
	if a.planned == nil || a.planned.id != second {
		t.Fatal("the cores' target keeps the node switched from")
	}
}

// A dedicated port removed before the cores got to starting it starts nothing.
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
	if a.planned == nil || a.planned.run.b.Connected() {
		t.Fatal("the cores should start for the port alone")
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
