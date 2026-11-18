package xray

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverInterruptedFileSet(t *testing.T) {
	for step := 0; step <= 4; step++ {
		t.Run(string(rune('0'+step)), func(t *testing.T) {
			dir := t.TempDir()
			backup := filepath.Join(dir, "rollback-interrupted")
			if err := os.Mkdir(backup, 0700); err != nil {
				t.Fatal(err)
			}
			files := []replacement{{"old", true}, {"also-old", true}, {"new", false}}
			put := func(path, contents string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			put(filepath.Join(dir, "old"), "first")
			put(filepath.Join(dir, "also-old"), "second")
			if err := writeJournal(backup, files); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < min(step, 2); i++ {
				n := files[i].Name
				if err := os.Rename(filepath.Join(dir, n), filepath.Join(backup, n)); err != nil {
					t.Fatal(err)
				}
			}
			if step >= 3 {
				put(filepath.Join(dir, "old"), "replaced")
			}
			if step >= 4 {
				put(filepath.Join(dir, "new"), "newly created")
			}
			if !RecoveryPending(dir) {
				t.Fatal("journal not detected")
			}
			if err := RecoverFiles(dir); err != nil {
				t.Fatal(err)
			}
			for n, want := range map[string]string{"old": "first", "also-old": "second"} {
				if got, err := os.ReadFile(filepath.Join(dir, n)); err != nil || string(got) != want {
					t.Fatalf("%s=%q %v", n, got, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
				t.Fatalf("uncommitted file present: %v", err)
			}
			if RecoveryPending(dir) {
				t.Fatal("completed recovery still pending")
			}
		})
	}
}
