// Package system wraps the OS integration RayDock needs: elevation, start at
// logon and the DNS cache.
package system

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrUACCancelled reports that the user declined the UAC prompt.
var ErrUACCancelled = errors.New("已取消管理员授权")

// IsElevated reports whether the process runs with administrator rights.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// RelaunchElevated starts this executable again through UAC without waiting.
// The caller is expected to quit.
func RelaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	_, err = shellExecute(exe, args, windows.SW_SHOWNORMAL, false)
	return err
}

// runElevated runs a program through UAC, hidden, and waits for its exit code.
func runElevated(exe string, args ...string) (uint32, error) {
	return shellExecute(exe, args, windows.SW_HIDE, true)
}

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	keyClass   uintptr
	hotKey     uint32
	icon       uintptr
	process    windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
)

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

func shellExecute(exe string, args []string, show int32, wait bool) (uint32, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	info := shellExecuteInfo{mask: seeMaskNoAsync, show: show}
	info.size = uint32(unsafe.Sizeof(info))
	info.verb, _ = windows.UTF16PtrFromString("runas")
	info.file, _ = windows.UTF16PtrFromString(exe)
	info.parameters, _ = windows.UTF16PtrFromString(strings.Join(quoted, " "))
	info.directory, _ = windows.UTF16PtrFromString(filepath.Dir(exe))
	if wait {
		info.mask |= seeMaskNoCloseProcess
	}
	if r, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, ErrUACCancelled
		}
		return 0, err
	}
	if !wait || info.process == 0 {
		return 0, nil
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	err := windows.GetExitCodeProcess(info.process, &code)
	return code, err
}

// WaitProcessExit waits up to timeout for process pid to exit.
func WaitProcessExit(pid int, timeout time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
}

var procDnsFlushResolverCache = windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")

// FlushDNS clears the system DNS cache, which may still hold addresses
// resolved through the TUN device (FakeDNS addresses in particular).
func FlushDNS() {
	if procDnsFlushResolverCache.Find() == nil {
		_, _, _ = procDnsFlushResolverCache.Call()
	}
}
