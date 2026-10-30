package system

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The logon task starts RayDock with administrator rights: whoever may replace its executable, or put
// files beside it, gets them at the user's next logon. Only the user, SYSTEM, the administrators and
// TrustedInstaller may modify the executable and its folder. Ancestors must not allow an untrusted
// principal to replace a subtree either.

// Access rights that let a principal replace a file or what a folder holds.
const (
	fileWriteData   = 0x2 // a folder: add a file
	fileAppendData  = 0x4 // a folder: add a subfolder
	fileDeleteChild = 0x40
	rightDelete     = 0x10000
	rightWriteDAC   = 0x40000
	rightWriteOwner = 0x80000
	genericAll      = 0x10000000
	genericWrite    = 0x40000000

	modifyRights = fileWriteData | fileAppendData | fileDeleteChild | rightDelete | rightWriteDAC | rightWriteOwner | genericAll | genericWrite
	moveRights   = fileDeleteChild | rightDelete | rightWriteDAC | rightWriteOwner | genericAll
)

// trustedInstaller is the service that owns Windows' own files.
const trustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// AdminAutostartRisk validates the executable, its directory and both the lexical and resolved ancestor
// chains. A writable ancestor can replace a protected subtree; a reparse point must not hide that path.
func AdminAutostartRisk(exe string) error {
	trusted, err := trustedSIDs()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("无法解析自启程序路径：%w", err)
	}
	paths := []string{exe}
	if !strings.EqualFold(exe, resolved) {
		paths = append(paths, resolved)
	}
	return checkAutostartPaths(paths, func(path string, rights uint32) (bool, error) { return othersMay(path, rights, trusted) })
}

func checkAutostartPaths(exes []string, check func(string, uint32) (bool, error)) error {
	for _, exe := range exes {
		for path, i := exe, 0; ; i++ {
			rights := uint32(modifyRights)
			if i >= 2 {
				rights = moveRights
			}
			open, err := check(path, rights)
			if err != nil {
				return fmt.Errorf("无法读取 %s 的权限：%w", path, err)
			}
			if open {
				return fmt.Errorf("其他用户可以修改或替换 %s，不能安全地以管理员身份自启；请修复路径权限或改用普通自启", path)
			}
			parent := filepath.Dir(path)
			if parent == path {
				break
			}
			path = parent
		}
	}
	return nil
}

// othersMay reports whether a principal not in trusted may use some of rights on path, or owns it.
func othersMay(path string, rights uint32, trusted []*windows.SID) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	isTrusted := func(sid *windows.SID) bool {
		for _, t := range trusted {
			if sid.Equals(t) {
				return true
			}
		}
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	if owner != nil && !isTrusted(owner) {
		return true, nil // an owner may always change the permissions
	}
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || err == nil && dacl == nil {
		return true, nil // no DACL: everyone has full control
	}
	if err != nil {
		return false, err
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if uint32(ace.Mask)&rights != 0 && !isTrusted(sid) {
			return true, nil
		}
	}
	return false, nil
}

// trustedSIDs are those who may modify RayDock: the current user, SYSTEM, the administrators and
// TrustedInstaller.
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
