package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The data directory and what is created in it later grant only the user, SYSTEM and the
// administrators, whatever the parent grants.
func TestPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "state.json")
	if err := os.WriteFile(child, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	sid, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, child} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		sddl := sd.String()
		for _, ace := range strings.Split(sddl[strings.Index(sddl, "(")+1:len(sddl)-1], ")(") {
			trustee := ace[strings.LastIndex(ace, ";")+1:]
			if trustee != sid && trustee != "SY" && trustee != "BA" {
				t.Errorf("%s grants %s: %s", path, trustee, sddl)
			}
		}
		if path == dir && !strings.HasPrefix(sddl, "D:P") {
			t.Errorf("%s inherits its parent's access: %s", path, sddl)
		}
	}
}
