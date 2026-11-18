package app

import (
	"errors"
	"os"
	"strings"
	"testing"

	"raydock/internal/store"
	"raydock/internal/sysproxy"
)

// The Windows writer updates Enabled, Server, then Override. Model every prefix left by a failed restore.
type prefixProxy struct {
	current sysproxy.Settings
	steps   int
}

func (p *prefixProxy) Current() (sysproxy.Settings, error) { return p.current, nil }
func (p *prefixProxy) Apply(to sysproxy.Settings) error {
	if p.steps > 0 {
		p.current.Enabled = to.Enabled
	}
	if p.steps > 1 {
		p.current.Server = to.Server
	}
	if p.steps > 2 {
		p.current.Override = to.Override
	}
	if p.steps < 4 {
		return errors.New("interrupted during proxy write/notification")
	}
	return nil
}

func TestRecoverEveryPartialProxyRestore(t *testing.T) {
	for i := 0; i <= 3; i++ {
		a, _, _ := newTestApp(t)
		old := sysproxy.Settings{Enabled: false, Server: "previous.invalid:8080", Override: "prior"}
		address := sysproxy.Settings{Enabled: true, Server: a.ProxyAddr(), Override: sysproxy.Bypass}
		proxy := &prefixProxy{current: address, steps: i}
		a.Proxy = proxy
		a.State.ProxyBackup = &store.ProxyBackup{Previous: old, Applied: address.Server, Owned: &address}
		a.restoreProxy()
		if a.ProxyErr == "" || a.State.ProxyBackup == nil || a.State.ProxyBackup.Transition == nil {
			t.Fatalf("step %d: missing error or persisted intent", i)
		}
		proxy.steps = 4
		// The uninstaller must be able to pick up the same journal after a crash.
		if err := RestoreProxyBackup(a.DataDir, proxy); err != nil {
			t.Fatal(err)
		}
		saved, err := store.Load(a.statePath())
		if err != nil || saved.ProxyBackup != nil || proxy.current != old {
			t.Fatalf("step %d: saved=%v current=%+v err=%v", i, saved.ProxyBackup, proxy.current, err)
		}
		a.State.ProxyBackup = nil
	}
}

func TestProxyPortRetryRetainsIntermediateServer(t *testing.T) {
	a, _, _ := newTestApp(t)
	previous := sysproxy.Settings{Enabled: true, Server: "before.invalid:8080", Override: "prior"}
	proxy := &prefixProxy{current: previous, steps: 4}
	a.Proxy = proxy
	a.Conn, a.runningPort = StateOn, 21081
	a.SyncProxy()
	a.runningPort, proxy.steps = 21082, 2
	a.SyncProxy() // server changed, response failed
	if proxy.current.Server != "127.0.0.1:21082" {
		t.Fatal("fixture did not change the server")
	}
	a.runningPort, proxy.steps = 21083, 0
	a.SyncProxy() // another failure before any write
	proxy.steps = 4
	a.restoreProxy()
	if proxy.current != previous || a.State.ProxyBackup != nil {
		t.Fatalf("lost ownership across retries: %+v", proxy.current)
	}
}

func TestResumeRejectsInvalidEditedChain(t *testing.T) {
	a, _, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://127.0.0.1:9#Target", "")
	target := a.State.Nodes[0]
	group, err := a.AddGroup(GroupInput{Name: "front"})
	if err != nil {
		t.Fatal(err)
	}
	if result := a.ImportLinks("vless://11111111-2222-3333-4444-555555555555@public.example.com:443#Invalid", group); result.Added != 1 {
		t.Fatal(result)
	}
	a.coreHold = true
	a.Connect(target.ID, nil)
	if !a.WaitCore {
		t.Fatal("connection did not wait")
	}
	if err := a.EditGroup(target.GroupID, GroupInput{Name: "target", Front: a.State.Nodes[1].ID}); err != nil {
		t.Fatal(err)
	}
	a.coreHold = false
	a.resumeConnect(change{})
	if a.WaitCore || a.Conn != StateOff || !strings.Contains(a.ConnErr, "Invalid") {
		t.Fatalf("wait=%v state=%v err=%q", a.WaitCore, a.Conn, a.ConnErr)
	}
}

func TestFailedStateSaveKeepsPendingChanges(t *testing.T) {
	a, posts, _ := newTestApp(t)
	path := a.statePath() + ".tmp"
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	a.State.SelectedID = "new"
	a.Save()
	if a.SaveErr == "" || !a.saveDue {
		t.Fatal("failed save hidden or marked complete")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, posts, func() bool { return !a.saveDue })
	s, err := store.Load(a.statePath())
	if err != nil || a.SaveErr != "" || s.SelectedID != "new" {
		t.Fatalf("retry did not save new state: %+v %v", s, err)
	}
}
