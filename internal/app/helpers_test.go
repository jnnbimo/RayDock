package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/xray"
)

type fakeProxy struct {
	cur     sysproxy.Settings
	applied int
	fail    error // Apply fails with it, changing nothing
}

func (p *fakeProxy) Current() (sysproxy.Settings, error) { return p.cur, nil }
func (p *fakeProxy) Apply(s sysproxy.Settings) error {
	if p.fail != nil {
		return p.fail
	}
	p.cur = s
	p.applied++
	return nil
}
func newTestApp(t *testing.T) (*App, chan func(), *fakeProxy) {
	t.Helper()
	posts := make(chan func(), 1024)
	p := &fakeProxy{}
	a := New(Deps{DataDir: t.TempDir(), Proxy: p, Post: func(f func()) { posts <- f }}, store.State{Settings: store.DefaultSettings()})
	t.Cleanup(a.Shutdown)
	return a, posts, p
}
func waitFor(t *testing.T, posts chan func(), cond func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for !cond() {
		select {
		case f := <-posts:
			f()
		case <-timer.C:
			t.Fatal("background result timed out")
		}
	}
}
func drainPosts(posts chan func()) {
	for {
		select {
		case f := <-posts:
			f()
		default:
			return
		}
	}
}

// coresRunning reports whether a core of the connection runs.
func coresRunning(a *App) bool {
	a.procs.mu.Lock()
	defer a.procs.mu.Unlock()
	return slices.ContainsFunc(a.procs.cores, (*xray.Core).Running)
}

// coreConfigWritten reports a config written for the core, as starting one does.
func coreConfigWritten(a *App) error {
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		return fmt.Errorf("%s: %v", filepath.Base(a.configPath()), err)
	}
	return nil
}

func placeholderCore(t *testing.T, a *App) {
	t.Helper()
	if err := os.MkdirAll(a.CoreDir(), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(a.CoreDir(), name), []byte("inert test placeholder"), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

// coreFilesIn returns a directory holding core files that read content, as a download or bundle would.
func coreFilesIn(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func waitOperation(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("core operation timed out")
	}
}

// runningFake makes a look connected to node id through its core, as a start leaves it, without a
// process but with core files, which a restart needs; core operations wait until the returned func is
// called.
func runningFake(t *testing.T, a *App, id string) (release func()) {
	t.Helper()
	if !a.CoreReady() {
		placeholderCore(t, a)
	}
	chain, err := a.Chain(a.NodeByID(id))
	if err != nil {
		t.Fatal(err)
	}
	opts := a.xrayOptions()
	local, err := a.tunLocal(opts)
	if err != nil {
		t.Skip("no physical network for TUN: ", err)
	}
	plan, err := a.planTarget(coreSpec{opts: opts, run: xray.Run{Chain: copyChain(chain), Local: local}}, change{})
	if err != nil {
		t.Fatal(err)
	}
	plan.proc.core = a.newCore()
	plan.id, plan.opts, plan.seq, plan.connSeq, plan.ctx, plan.fresh, plan.switched = id, opts, a.coreSeq, a.connSeq, a.coreCtx, true, true
	a.Conn, a.ActiveID, a.runningPort, a.Since = StateOn, id, opts.Port, time.Now()
	a.connCtx, a.connCancel = context.WithCancel(a.workCtx)
	a.planned, a.running = plan, plan
	gate := make(chan struct{})
	a.coreDo(func() { <-gate })
	return sync.OnceFunc(func() { close(gate) })
}

// Test fixtures must not wait on or reuse an adapter of a real RayDock running on the developer's PC.
// These tests use placeholder executables; the opt-in elevated adapter tests live in package xray.
func TestMain(m *testing.M) {
	xray.TUNNames = [2]string{fmt.Sprintf("RayDockTest-%d-A", os.Getpid()), fmt.Sprintf("RayDockTest-%d-B", os.Getpid())}
	os.Exit(m.Run())
}

// connectedFake connects the first node as runningFake does, with the core committed: checks may run
// through its local port, where nothing listens, so they fail fast without external traffic.
func connectedFake(t *testing.T, a *App) {
	t.Helper()
	a.ImportLinks("trojan://p@127.0.0.1:1#n", "")
	a.State.Settings.Port, _ = freePort()
	t.Cleanup(runningFake(t, a, a.State.Nodes[0].ID))
	liveFake(a, a.planned)
}

// commitFake makes start t of the connection's core done, as its process started, without it.
func commitFake(a *App, t *coreTarget) {
	liveFake(a, t)
	a.running = t
	a.started(t, nil)
}

// liveFake publishes the core of t as running.
func liveFake(a *App, t *coreTarget) { a.live.Store(t.proc) }
