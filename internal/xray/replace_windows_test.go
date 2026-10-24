package xray

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyCoreRollbackLockedDestination(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	for _, name := range CoreFiles() {
		if err := os.WriteFile(filepath.Join(src, name), []byte("new "+name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), []byte("old "+name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	p, err := windows.UTF16PtrFromString(filepath.Join(dst, "geoip.dat"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := CopyCore(src, dst, nil); err == nil {
		t.Fatal("locked destination should fail")
	}
	for _, name := range CoreFiles() {
		b, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "old "+name {
			t.Fatalf("failed update changed %s: %q", name, b)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dst, "rollback-*")); len(left) > 0 {
		t.Fatalf("successful rollback leaked backups: %v", left)
	}
}
