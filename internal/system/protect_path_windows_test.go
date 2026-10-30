package system

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAutostartPathTrustChain(t *testing.T) {
	exe := `C:\shared\protected\RayDock.exe`
	for _, unsafe := range []string{exe, filepath.Dir(exe), `C:\shared`, `C:\`} {
		seen := map[string]uint32{}
		err := checkAutostartPaths([]string{exe}, func(p string, m uint32) (bool, error) { seen[p] = m; return p == unsafe, nil })
		if err == nil || !strings.Contains(err.Error(), unsafe) {
			t.Fatalf("unsafe %s accepted: %v", unsafe, err)
		}
		if unsafe == `C:\shared` && seen[unsafe] != moveRights {
			t.Fatal("ancestor replacement rights not checked")
		}
	}
	seen := map[string]bool{}
	if err := checkAutostartPaths([]string{exe, `D:\target\RayDock.exe`}, func(p string, m uint32) (bool, error) { seen[p] = true; return false, nil }); err != nil || !seen[`C:\`] || !seen[`D:\`] {
		t.Fatal("both lexical/resolved roots must be checked", err, seen)
	}
}
