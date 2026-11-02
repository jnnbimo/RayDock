package system

import (
	"golang.org/x/sys/windows"
)

// PrivateDir limits dir, and everything it will hold, to the current user, SYSTEM and the
// administrators. RayDock's data holds node credentials; beside the executable it would otherwise
// inherit whatever that folder grants other users, such as Authenticated Users on a data drive's root.
func PrivateDir(dir string) error {
	sid, err := userSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// ShowError reports an error that stops RayDock before it has a window to show it in.
func ShowError(title, text string) {
	t, err1 := windows.UTF16PtrFromString(text)
	c, err2 := windows.UTF16PtrFromString(title)
	if err1 != nil || err2 != nil {
		return
	}
	_, _ = windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONERROR|windows.MB_SETFOREGROUND)
}
