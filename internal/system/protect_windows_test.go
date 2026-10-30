package system

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An executable in a folder of the user's own is safe to start with administrator rights; one that the
// Users group may modify, or whose folder it may, is not.
func TestAdminAutostartRisk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The folder of the user's own, whatever the temporary folder lets others do.
	sid, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", dir, "/inheritance:r", "/grant:r", "*"+sid+":(OI)(CI)(F)", "*S-1-5-18:(OI)(CI)(F)",
		"*S-1-5-32-544:(OI)(CI)(F)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v %s", err, out)
	}
	exe := filepath.Join(dir, "RayDock.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Isolate this ACL test from the developer machine's drive-root permissions. The complete ancestor
	// walk is tested separately below, including an unsafe drive root and a writable intermediate folder.
	trusted, err := trustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	check := func() error {
		return checkAutostartPaths([]string{exe}, func(path string, rights uint32) (bool, error) {
			if path == exe || path == dir {
				return othersMay(path, rights, trusted)
			}
			return false, nil
		})
	}
	if err := check(); err != nil {
		t.Fatalf("the user's own folder: %v", err)
	}
	grant := func(path, rights string) {
		t.Helper()
		if out, err := exec.Command("icacls", path, "/grant", "*S-1-5-32-545:"+rights).CombinedOutput(); err != nil {
			t.Fatalf("icacls: %v %s", err, out)
		}
	}
	grant(dir, "(RX)") // reading is fine
	if err := check(); err != nil {
		t.Fatalf("readable by Users: %v", err)
	}
	grant(dir, "(M)") // the folder alone
	if err := check(); err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("a folder modifiable by Users: %v", err)
	}
	grant(exe, "(M)")
	if err := check(); err == nil || !strings.Contains(err.Error(), exe) {
		t.Fatalf("modifiable by Users: %v", err)
	}
}
