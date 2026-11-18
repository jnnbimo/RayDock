package system

import (
	"fmt"
	"io/fs"
	"path/filepath"

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
	trusted, err := trustedSIDs()
	if err != nil {
		return err
	}
	var paths []string
	if err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := rejectReparsePoint(path); err != nil {
			return err
		}
		foreign, err := otherOwner(path, trusted)
		if err != nil {
			return err
		}
		if foreign {
			return fmt.Errorf("无法保护其他账户拥有的文件：%s", path)
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return err
	}
	for _, path := range paths {
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
			return fmt.Errorf("限制 %s 的访问权限：%w", path, err)
		}
	}
	return nil
}

// trustedInstaller is the service that owns Windows' own files.
const trustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// trustedSIDs are those whose files PrivateDir may take over: the current user, SYSTEM, the
// administrators and TrustedInstaller.
func trustedSIDs() ([]*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	out := []*windows.SID{u.User.Sid}
	for _, k := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(k)
		if err != nil {
			return nil, err
		}
		out = append(out, sid)
	}
	ti, err := windows.StringToSid(trustedInstaller)
	if err != nil {
		return nil, err
	}
	return append(out, ti), nil
}

// otherOwner reports whether path is owned by none of trusted.
func otherOwner(path string, trusted []*windows.SID) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	for _, t := range trusted {
		if owner != nil && owner.Equals(t) {
			return false, nil
		}
	}
	return true, nil
}

// rejectReparsePoint refuses a path that is a link or junction, which would lead the change elsewhere.
func rejectReparsePoint(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("数据目录中不能有符号链接或联接点：%s", path)
	}
	return nil
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
