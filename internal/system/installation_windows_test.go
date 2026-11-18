package system

import (
	"crypto/rand"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNamedLeaseReleasedWhenOwnerCloses(t *testing.T) {
	name := `Global\RayDock-Test-` + rand.Text()
	release, err := tryNamedLease(name)
	if err != nil || release == nil {
		t.Fatalf("first lease: %v", err)
	}
	if second, err := tryNamedLease(name); err != nil || second != nil {
		release()
		t.Fatalf("second acquisition while held: %v", err)
	}
	release()
	last, err := tryNamedLease(name)
	if err != nil || last == nil {
		t.Fatalf("lease after release: %v", err)
	}
	last()
}

func TestInstallLeaseAndReservationAreScoped(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	release, err := TryInstallationSession(dir)
	if release == nil || err != nil {
		t.Fatalf("first lease: %v", err)
	}
	defer release()
	if second, err := TryInstallationSession(dir); second != nil || err != nil {
		t.Fatalf("second acquired installation, err=%v", err)
	}
	second, err := TryInstallationSession(other)
	if second == nil || err != nil {
		t.Fatalf("unrelated installation was blocked: %v", err)
	}
	defer second()
	h, err := openInstallFile(dir, uninstallReservation, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, windows.OPEN_ALWAYS)
	if err != nil {
		t.Fatal(err)
	}
	if reserved, err := UninstallReserved(dir); !reserved || err != nil {
		windows.CloseHandle(h)
		t.Fatalf("reservation not visible: %v %v", reserved, err)
	}
	if reserved, err := UninstallReserved(other); reserved || err != nil {
		windows.CloseHandle(h)
		t.Fatalf("unrelated path affected: %v %v", reserved, err)
	}
	windows.CloseHandle(h)
	if reserved, err := UninstallReserved(dir); reserved || err != nil {
		t.Fatalf("closing the owner did not clear reservation: %v %v", reserved, err)
	}
}
