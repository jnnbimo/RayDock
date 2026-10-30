//go:build systemtest

// These tests change the current user's startup entries and restore them afterwards:
//
//	go test -tags systemtest ./internal/system -v
package system

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestSystemRunEntry(t *testing.T) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	prev, _, prevErr := k.GetStringValue(runValue)
	k.Close()
	t.Cleanup(func() {
		if prevErr == nil {
			k, _, _ := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
			_ = k.SetStringValue(runValue, prev)
			k.Close()
		} else {
			_ = deleteRunValue()
		}
	})

	exe := `C:\Program Files\RayDock Test\RayDock.exe`
	if err := SetAutostart(StartUser, exe); err != nil {
		t.Fatal(err)
	}
	if m := Autostart(exe); m != StartUser {
		t.Fatalf("mode %v", m)
	}
	if m := Autostart(`C:\Elsewhere\RayDock.exe`); m != StartOff {
		t.Errorf("an entry for another executable should not count: %v", m)
	}
	if err := SetAutostart(StartOff, exe); err != nil {
		t.Fatal(err)
	}
	if m := Autostart(exe); m != StartOff {
		t.Fatalf("mode after off %v", m)
	}
}

// Registering with highest privileges needs elevation, so the same XML is registered at the
// lowest level, through the Task Scheduler's COM interface, to check that it accepts the task, that
// the command reads back, and that deleting removes it.
func TestSystemTaskXML(t *testing.T) {
	sid, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "Ray Dock & 测试", "RayDock.exe")
	src := strings.Replace(taskXML(exe, sid), "HighestAvailable", "LeastPrivilege", 1)
	const name = "RayDock Autostart Test"
	if err := registerTask(name, src); err != nil {
		t.Fatalf("the task was rejected: %v", err)
	}
	t.Cleanup(func() { _ = deleteTask(name) })
	cmd, _, ok := taskDetails(name)
	if !ok || !samePath(cmd, exe) {
		t.Fatalf("command %q ok %v, want %q", cmd, ok, exe)
	}
	if err := registerTask(name, src); err != nil {
		t.Fatalf("replacing the task: %v", err)
	}
	if err := deleteTask(name); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := taskDetails(name); ok {
		t.Fatal("the task is still there")
	}
}
