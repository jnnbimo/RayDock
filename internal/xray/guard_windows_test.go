package xray

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Every core shares the process's one job: starting cores, short-lived ones included, opens no handle
// that stays.
func TestProcessGuardShared(t *testing.T) {
	count := func() uint32 {
		var n uint32
		proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
		if ok, _, err := proc.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n))); ok == 0 {
			t.Fatal(err)
		}
		return n
	}
	g := newProcessGuard()
	if g == nil {
		t.Skip("no job object")
	}
	before := count()
	for range 50 {
		if newProcessGuard() != g {
			t.Fatal("another job")
		}
	}
	if after := count(); after > before+5 {
		t.Fatalf("handles %d → %d", before, after)
	}
}
