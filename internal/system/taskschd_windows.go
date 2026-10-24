package system

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Task Scheduler 2.0 COM API (taskschd.h), called directly: registering a task this way needs no
// schtasks.exe process and no task file on disk.

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	oleaut32             = windows.NewLazySystemDLL("oleaut32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procSysAllocString   = oleaut32.NewProc("SysAllocString")
	procSysFreeString    = oleaut32.NewProc("SysFreeString")

	clsidTaskScheduler = windows.GUID{Data1: 0x0f87369f, Data2: 0xa4e5, Data3: 0x4cfc, Data4: [8]byte{0xbd, 0x3e, 0x73, 0xe6, 0x15, 0x45, 0x72, 0xdd}}
	iidTaskService     = windows.GUID{Data1: 0x2faba4c7, Data2: 0x4da9, Data3: 0x4013, Data4: [8]byte{0x96, 0x97, 0x20, 0xcc, 0x3f, 0xd4, 0x0f, 0x85}}
)

// Methods by their index in the interfaces' tables, after IDispatch's seven.
const (
	taskServiceGetFolder = 7  // ITaskService::GetFolder
	taskServiceConnect   = 10 // ITaskService::Connect
	folderGetTask        = 13 // ITaskFolder::GetTask
	folderDeleteTask     = 15 // ITaskFolder::DeleteTask
	folderRegisterTask   = 16 // ITaskFolder::RegisterTask
	registeredTaskXML    = 20 // IRegisteredTask::get_Xml

	comRelease = 2 // IUnknown::Release

	coinitMultithreaded       = 0x0
	clsctxInprocServer        = 0x1
	sFalse                    = 0x1
	taskCreateOrUpdate        = 0x6
	taskLogonInteractiveToken = 3
	hrFileNotFound            = 0x80070002
)

// variant is an empty VARIANT, 24 bytes on 64-bit Windows, where one is passed by reference.
type variant [24]byte

// comObject is an interface pointer of a COM object.
type comObject struct{ p unsafe.Pointer }

func (o comObject) call(method int, args ...uintptr) uint32 {
	vtbl := *(*unsafe.Pointer)(o.p)
	fn := *(*uintptr)(unsafe.Add(vtbl, method*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(o.p)}, args...)...)
	return uint32(r)
}

func (o comObject) release() { o.call(comRelease) }

// hresult is a failed HRESULT, told with the system's message for the Win32 errors it wraps.
type hresult uint32

func (h hresult) Error() string {
	if h&0xffff0000 == 0x80070000 {
		return windows.Errno(h & 0xffff).Error()
	}
	return fmt.Sprintf("HRESULT 0x%08X", uint32(h))
}

func hresultError(hr uint32) error { return hresult(hr) }

// bstr is a BSTR holding s, which free releases.
type bstr struct{ p uintptr }

func newBSTR(s string) (bstr, error) {
	u, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return bstr{}, err
	}
	p, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(u)))
	if p == 0 {
		return bstr{}, errors.New("SysAllocString failed")
	}
	return bstr{p}, nil
}

func (b bstr) free() { procSysFreeString.Call(b.p) }

// withRootFolder connects to the local Task Scheduler and runs fn with its root folder, on a thread of its
// own initialized for COM.
func withRootFolder(fn func(folder comObject) error) error {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return errors.New("当前架构不支持计划任务接口")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded); hr != 0 && hr != sFalse {
		return hresultError(uint32(hr))
	}
	defer procCoUninitialize.Call()

	var svc comObject
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidTaskScheduler)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidTaskService)), uintptr(unsafe.Pointer(&svc.p))); hr != 0 {
		return hresultError(uint32(hr))
	}
	defer svc.release()
	var empty variant // the local machine, as the current user
	if hr := svc.call(taskServiceConnect, uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&empty)),
		uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&empty))); hr != 0 {
		return hresultError(hr)
	}
	root, err := newBSTR(`\`)
	if err != nil {
		return err
	}
	defer root.free()
	var folder comObject
	if hr := svc.call(taskServiceGetFolder, root.p, uintptr(unsafe.Pointer(&folder.p))); hr != 0 {
		return hresultError(hr)
	}
	defer folder.release()
	return fn(folder)
}

// registerTask creates task name from its XML definition, or replaces it.
func registerTask(name, xml string) error {
	return withRootFolder(func(folder comObject) error {
		n, err := newBSTR(name)
		if err != nil {
			return err
		}
		defer n.free()
		x, err := newBSTR(xml)
		if err != nil {
			return err
		}
		defer x.free()
		var empty variant // the user, password and security descriptor the XML gives
		var task comObject
		hr := folder.call(folderRegisterTask, n.p, x.p, taskCreateOrUpdate,
			uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&empty)), taskLogonInteractiveToken,
			uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&task.p)))
		if hr != 0 {
			return hresultError(hr)
		}
		task.release()
		return nil
	})
}

// deleteTask removes task name; one that does not exist is no error.
func deleteTask(name string) error {
	return withRootFolder(func(folder comObject) error {
		n, err := newBSTR(name)
		if err != nil {
			return err
		}
		defer n.free()
		if hr := folder.call(folderDeleteTask, n.p, 0); hr != 0 && hr != hrFileNotFound {
			return hresultError(hr)
		}
		return nil
	})
}

// taskDefinition returns the XML definition of task name; ok is false when there is no such task or it
// cannot be read.
func taskDefinition(name string) (xml string, ok bool) {
	err := withRootFolder(func(folder comObject) error {
		n, err := newBSTR(name)
		if err != nil {
			return err
		}
		defer n.free()
		var task comObject
		if hr := folder.call(folderGetTask, n.p, uintptr(unsafe.Pointer(&task.p))); hr != 0 {
			return hresultError(hr)
		}
		defer task.release()
		var text *uint16
		if hr := task.call(registeredTaskXML, uintptr(unsafe.Pointer(&text))); hr != 0 {
			return hresultError(hr)
		}
		xml = windows.UTF16PtrToString(text)
		procSysFreeString.Call(uintptr(unsafe.Pointer(text)))
		return nil
	})
	return xml, err == nil
}
