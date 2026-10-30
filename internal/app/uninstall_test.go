package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"raydock/internal/store"
	"raydock/internal/sysproxy"
)

type cleanupProxy struct {
	current           sysproxy.Settings
	readErr, applyErr error
	writes            int
}

func (p *cleanupProxy) Current() (sysproxy.Settings, error) { return p.current, p.readErr }
func (p *cleanupProxy) Apply(s sysproxy.Settings) error {
	p.writes++
	if p.applyErr != nil {
		return p.applyErr
	}
	p.current = s
	return nil
}

func TestUninstallRestoresOnlyOwnedProxy(t *testing.T) {
	for _, owned := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "state.json")
		previous := sysproxy.Settings{Enabled: true, Server: "127.0.0.1:9000", Override: "original"}
		st := store.State{Settings: store.DefaultSettings(), ProxyBackup: &store.ProxyBackup{Previous: previous, Applied: "127.0.0.1:10818"}}
		if err := store.Save(path, st); err != nil {
			t.Fatal(err)
		}
		p := &cleanupProxy{current: sysproxy.Settings{Enabled: true, Server: "127.0.0.1:7777"}}
		if owned {
			p.current.Server = st.ProxyBackup.Applied
		}
		original := p.current
		if err := RestoreProxyBackup(dir, p); err != nil {
			t.Fatal(err)
		}
		saved, err := store.Load(path)
		if err != nil || saved.ProxyBackup != nil {
			t.Fatal("recovery record not cleared", err)
		}
		if owned && (p.current != previous || p.writes != 1) || !owned && (p.current != original || p.writes != 0) {
			t.Fatal("wrong proxy restored")
		}
	}
}

func TestUninstallPreservesFailureAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st := store.State{Settings: store.DefaultSettings(), ProxyBackup: &store.ProxyBackup{Applied: "127.0.0.1:10818"}}
	if err := store.Save(path, st); err != nil {
		t.Fatal(err)
	}
	p := &cleanupProxy{current: sysproxy.Settings{Enabled: true, Server: st.ProxyBackup.Applied}, applyErr: errors.New("access denied")}
	if err := RestoreProxyBackup(dir, p); err == nil {
		t.Fatal("failed recovery reported success")
	}
	saved, err := store.Load(path)
	if err != nil || saved.ProxyBackup == nil {
		t.Fatal("backup discarded after failure", err)
	}
	if err := os.WriteFile(path, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreProxyBackup(dir, p); err == nil {
		t.Fatal("corrupt state silently ignored")
	}
	if b, _ := os.ReadFile(path); string(b) != "broken JSON" {
		t.Fatal("uninstaller overwrote corrupt state")
	}
}
