//go:build integration

package xray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows allows a running executable to move but not be deleted. Force a real
// rollback failure, then ensure a later successful update preserves its recovery copy.
func TestIntegrationRollbackRecoverySurvivesNextCommit(t *testing.T) {
	exe := coreForTest(t)
	dir := t.TempDir()
	staged := filepath.Join(dir, ExeName()+".new")
	in, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	err = writeFile(staged, in)
	in.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ExeName()), []byte("original recovery data"), 0o600); err != nil {
		t.Fatal(err)
	}
	core := startCore(t, staged, dir, "recovery", []byte(`{"log":{"loglevel":"warning"},"outbounds":[{"protocol":"freedom"}]}`), nil)
	err = commitFiles([]string{staged, filepath.Join(dir, "missing.new")})
	if err == nil || !strings.Contains(err.Error(), "备份保留") {
		t.Fatalf("did not force a rollback recovery failure: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "rollback-*", ExeName()))
	if err != nil || len(backups) != 1 {
		t.Fatalf("recovery backup not retained: %v %v", backups, err)
	}
	core.Stop()
	if err := CopyCore(filepath.Dir(exe), dir, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(backups[0]); err != nil || string(data) != "original recovery data" {
		t.Fatalf("the next commit deleted required recovery data: %q %v", data, err)
	}
}
