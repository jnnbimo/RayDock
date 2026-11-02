package xray

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommitPreservesRecoveryBackups(t *testing.T) {
	dir, src := t.TempDir(), t.TempDir()
	// Unmarked and legacy rollback directories must be conservative recovery data.
	for _, name := range []string{"rollback-recovery-needed", "rollback-legacy"} {
		backup := filepath.Join(dir, name)
		if err := os.Mkdir(backup, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backup, ExeName()), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	retired := filepath.Join(dir, "retired-previous-success")
	if err := os.Mkdir(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "old"), []byte("disposable"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range CoreFiles() {
		if err := os.WriteFile(filepath.Join(src, name), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := CopyCore(src, dir, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"rollback-recovery-needed", "rollback-legacy"} {
		if data, err := os.ReadFile(filepath.Join(dir, name, ExeName())); err != nil || string(data) != "original" {
			t.Fatalf("recovery data was swept: %s %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("a confirmed retired backup was not collected: %v", err)
	}
}

func TestCommitFilesRollsBackInstallFailure(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "one.new"), filepath.Join(dir, "two.new")}
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths[0], []byte("new one"), 0600); err != nil {
		t.Fatal(err)
	}
	// The second staged file is missing: failure occurs after the first install, not during preparation.
	if err := commitFiles(paths); err == nil {
		t.Fatal("missing staged file should fail")
	}
	for _, name := range []string{"one", "two"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != "old "+name {
			t.Fatalf("rollback %s=%q: %v", name, b, err)
		}
	}
}

func TestCommitFilesRollbackRemovesNewDestinations(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.new")
	if err := os.WriteFile(first, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := commitFiles([]string{first, filepath.Join(dir, "two.new")}); err == nil {
		t.Fatal("missing staged file should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "one")); !os.IsNotExist(err) {
		t.Fatalf("failed new install left destination: %v", err)
	}
}
