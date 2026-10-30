//go:build windows

package system

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ACL edits are confined to this test's temporary tree. No task or registry value is modified.
func TestAutostartRejectsWritableAncestorACL(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "replaceable")
	leaf := filepath.Join(parent, "protected")
	if err := os.MkdirAll(leaf, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(leaf, "RayDock.exe")
	if err := os.WriteFile(exe, []byte("fixture, not executed"), 0600); err != nil {
		t.Fatal(err)
	}
	sid, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	acl := func(path string, grants ...string) {
		t.Helper()
		args := []string{path, "/inheritance:r", "/grant:r"}
		args = append(args, grants...)
		out, err := exec.Command("icacls", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ACL fixture: %v %s", err, out)
		}
	}
	trusted := []string{"*" + sid + ":(OI)(CI)(F)", "*S-1-5-18:(OI)(CI)(F)", "*S-1-5-32-544:(OI)(CI)(F)"}
	acl(base, append(append([]string{}, trusted...), "*S-1-5-32-545:(OI)(CI)(F)")...)
	acl(parent, append(append([]string{}, trusted...), "*S-1-5-32-545:(OI)(CI)(F)")...)
	acl(leaf, trusted...)
	acl(exe, "*"+sid+":(F)", "*S-1-5-18:(F)", "*S-1-5-32-544:(F)")
	ts, err := trustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	open, err := othersMay(parent, modifyRights, ts)
	if err != nil || !open {
		t.Fatal("fixture ancestor is not writable", err)
	}
	if err := AdminAutostartRisk(exe); err == nil || !strings.Contains(err.Error(), parent) {
		t.Fatal("writable ancestor accepted or wrong trust boundary", err)
	}
	t.Log("protected executable and leaf cannot bypass a writable ancestor")
}
