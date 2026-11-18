package system

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// InstallerNSIS opens the reservation with the same sharing contract as this probe.
const uninstallReservation = ".raydock-uninstall.lock"
const installationSession = ".raydock-session.lock"

// UninstallReserved checks the exclusive write handle held by the uninstaller.
// The file itself is not the reservation; a crash releases the handle.
func UninstallReserved(dir string) (bool, error) {
	h, err := openInstallFile(dir, uninstallReservation, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.OPEN_EXISTING)
	switch {
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
		return false, nil
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
		return true, nil
	case err != nil:
		return false, err
	default:
		windows.CloseHandle(h)
		return false, nil
	}
}

// TryInstallationSession acquires the scoped app/cleanup lease without waiting.
// The first app holds it until Shutdown is complete; a second caller gets nil.
func TryInstallationSession(dir string) (release func(), err error) {
	h, err := openInstallFile(dir, installationSession, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, windows.OPEN_ALWAYS)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}

// TryProxySession serializes access to the current user's WinINet recovery across installations.
// The named object's lifetime follows its handle, without thread-affine mutex ownership.
func TryProxySession() (func(), error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	return tryNamedLease(`Global\RayDock-WinINet-` + sid)
}

func tryNamedLease(name string) (func(), error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// CreateMutex returned an open handle even though the object already exists.
		windows.CloseHandle(h)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}

func openInstallFile(dir, name string, share, disposition uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(filepath.Join(dir, name))
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.GENERIC_WRITE, share, nil, disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}
