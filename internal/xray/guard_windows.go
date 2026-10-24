package xray

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hideWindow keeps the console program xray.exe from opening a window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// processGuard is a KILL_ON_JOB_CLOSE job: however this process ends, even killed, the kernel closes
// the job's handle and ends the xray processes in it, so no stray process keeps the ports. One job holds
// every core this process starts: its handle lives as long as the process, and a core that exits leaves
// it by itself.
type processGuard struct{ job windows.Handle }

// newProcessGuard returns the process's job, created on first use; nil when Windows refuses one.
var newProcessGuard = sync.OnceValue(createProcessGuard)

func createProcessGuard() *processGuard {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil
	}
	return &processGuard{job: job}
}

func (g *processGuard) add(pid int) {
	if g == nil {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(g.job, h)
}
