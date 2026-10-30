package system

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// StartMode is how RayDock starts when the user logs on.
type StartMode int

const (
	StartOff   StartMode = iota
	StartUser            // registry Run entry, standard rights
	StartAdmin           // scheduled task with highest privileges, no UAC prompt
)

// AutostartArg marks a launch at logon: the app starts in the tray.
const AutostartArg = "--autostart"

const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
	runValue    = "RayDock"
	taskName    = "RayDock Autostart"
)

// AutostartSupported reports whether start at logon can be configured.
func AutostartSupported() bool { return true }

// Autostart reports the mode that currently starts exe at logon. An entry that
// points to another executable or was disabled in Task Manager does not count.
func Autostart(exe string) StartMode {
	if cmd, enabled, ok := taskDetails(taskName); ok && enabled && samePath(cmd, exe) {
		return StartAdmin
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return StartOff
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	if err != nil || !samePath(commandExe(v), exe) || disabledInTaskManager() {
		return StartOff
	}
	return StartUser
}

// SetAutostart makes exe start at logon in mode and removes the other mode.
// Creating or deleting the scheduled task needs administrator rights; without
// them it asks once through UAC. The task is not made for an executable other
// users could replace (AdminAutostartRisk).
func SetAutostart(mode StartMode, exe string) error {
	if mode == StartAdmin {
		if err := AdminAutostartRisk(exe); err != nil {
			return err
		}
		if err := changeTask(true, exe); err != nil {
			return err
		}
	} else {
		raw, err := readTaskDefinition(taskName)
		if err != nil && !taskMissing(err) {
			return err
		}
		if err == nil {
			cmd, _ := parseTask(raw)
			if cmd == "" {
				return errors.New("无法核实 RayDock 自启任务的执行程序，请先检查并移除该任务")
			}
			if samePath(cmd, exe) {
				if err := changeTask(false, exe); err != nil {
					return err
				}
			}
		}
	}
	if mode == StartUser {
		return setRunValue(exe)
	}
	return deleteRunValue()
}

func setRunValue(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue(runValue, fmt.Sprintf("%q %s", exe, AutostartArg)); err != nil {
		return err
	}
	return clearStartupApproval()
}

// Clear a disabled mark left by Task Manager. Access failures must not masquerade as re-enablement.
func clearStartupApproval() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err = k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func deleteRunValue() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// UninstallArg asks RayDock, run by its uninstaller, to remove its start at logon (TaskHelper).
const UninstallArg = "--uninstall-autostart"

// CleanupAutostart removes the logon entries that start exe, as RayDock is uninstalled: the Run value
// and the logon task, which needs administrator rights, asked once through UAC. Entries of another
// installation are left alone.
func CleanupAutostart(exe string) error {
	raw, err := readTaskDefinition(taskName)
	if err != nil && !taskMissing(err) {
		return err
	}
	if err == nil {
		cmd, _ := parseTask(raw)
		if cmd == "" {
			return errors.New("无法核实 RayDock 自启任务的执行程序，请先检查并移除该任务")
		}
		if samePath(cmd, exe) {
			if err := changeTask(false, exe); err != nil {
				return err
			}
		}
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	v, _, err := k.GetStringValue(runValue)
	k.Close()
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !samePath(commandExe(v), exe) {
		return nil
	}
	if err := clearStartupApproval(); err != nil {
		return err
	}
	return deleteRunValue()
}

// disabledInTaskManager reads the StartupApproved flag: an odd first byte
// means the user disabled the entry.
func disabledInTaskManager() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(runValue)
	return err == nil && len(b) > 0 && b[0]&1 == 1
}

// taskHelperArg asks RayDock, relaunched through UAC, to create the logon task for the user SID after
// it, or with nothing after it to delete the task (TaskHelper).
const taskHelperArg = "--autostart-task="

// changeTask creates (create) or deletes the logon task starting exe. Both need administrator rights:
// unelevated, this executable is relaunched through UAC to do it (TaskHelper), for this user.
func changeTask(create bool, exe string) error {
	sid := ""
	if create {
		var err error
		if sid, err = userSID(); err != nil {
			return err
		}
	}
	if IsElevated() {
		return applyTask(create, exe, sid)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	code, err := runElevated(self, taskHelperArg+sid)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("计划任务操作失败：%w", hresultError(code))
	}
	return nil
}

func applyTask(create bool, exe, sid string) error {
	if create {
		if err := AdminAutostartRisk(exe); err != nil {
			return err
		}
		return registerTask(taskName, taskXML(exe, sid))
	}
	return deleteTask(taskName)
}

// TaskHelper does the change of start at logon RayDock is run for without its window, when args ask
// for one: the change of the logon task an unelevated RayDock asked for through UAC (changeTask), or the
// removal of its entries by the uninstaller (CleanupAutostart). It reports whether they did, and the exit
// code, 0 or the failure's HRESULT.
func TaskHelper(args []string) (code int, ok bool) {
	for _, a := range args {
		if a == UninstallArg {
			exe, err := os.Executable()
			if err == nil {
				err = CleanupAutostart(exe)
			}
			if err != nil {
				return 1, true
			}
			return 0, true
		}
		sid, found := strings.CutPrefix(a, taskHelperArg)
		if !found {
			continue
		}
		exe, err := os.Executable()
		if err == nil {
			err = applyTask(sid != "", exe, sid)
		}
		var hr hresult
		switch {
		case err == nil:
			return 0, true
		case errors.As(err, &hr):
			return int(hr), true
		default:
			return int(uint32(0x80004005)), true // E_FAIL
		}
	}
	return 0, false
}

// taskXML runs exe at logon of the user with highest privileges, at normal
// priority, without the default time limit and battery restrictions.
func taskXML(exe, sid string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Start RayDock at logon with administrator rights</Description></RegistrationInfo>
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled><UserId>` + sid + `</UserId><Delay>PT3S</Delay></LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author"><UserId>` + sid + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>4</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec><Command>` + esc(exe) + `</Command><Arguments>` + AutostartArg + `</Arguments><WorkingDirectory>` + esc(filepath.Dir(exe)) + `</WorkingDirectory></Exec>
  </Actions>
</Task>
`
}

func taskMissing(err error) bool {
	var hr hresult
	return errors.As(err, &hr) && uint32(hr) == hrFileNotFound
}

func taskDetails(name string) (string, bool, bool) {
	raw, ok := taskDefinition(name)
	if !ok {
		return "", false, false
	}
	sid, err := userSID()
	if err != nil {
		return "", false, true
	}
	cmd, on := parseTask(raw, sid)
	return cmd, on, true
}

// parseTask treats a disabled task or disabled logon trigger as inactive, so selecting admin mode can
// re-enable it rather than returning early with a stale enabled state.
func parseTask(raw string, user ...string) (string, bool) {
	var task struct {
		Settings struct{ Enabled *bool }
		Triggers struct {
			LogonTrigger []struct {
				Enabled *bool
				UserID  string `xml:"UserId"`
			}
		}
		Principals struct {
			Principal []struct {
				UserID   string `xml:"UserId"`
				RunLevel string
			}
		}
		Actions struct {
			Exec  []struct{ Command string }
			Other []struct{ XMLName xml.Name } `xml:",any"`
		}
	}
	decoder := xml.NewDecoder(strings.NewReader(raw))
	// IRegisteredTask::Xml is a BSTR already decoded to UTF-8 by readTaskDefinition; its declaration
	// still describes the original UTF-16 representation.
	decoder.CharsetReader = func(charset string, r io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "UTF-16") {
			return r, nil
		}
		return nil, fmt.Errorf("不支持的计划任务编码 %s", charset)
	}
	if decoder.Decode(&task) != nil || len(task.Actions.Exec) != 1 || len(task.Actions.Other) != 0 {
		return "", false
	}
	on := task.Settings.Enabled == nil || *task.Settings.Enabled
	trigger := false
	for _, t := range task.Triggers.LogonTrigger {
		enabled := t.Enabled == nil || *t.Enabled
		if len(user) > 0 {
			enabled = enabled && (t.UserID == "" || strings.EqualFold(t.UserID, user[0]))
		}
		trigger = trigger || enabled
	}
	if len(user) > 0 {
		principal := false
		for _, p := range task.Principals.Principal {
			principal = principal || (strings.EqualFold(p.UserID, user[0]) && p.RunLevel == "HighestAvailable")
		}
		on = on && principal
	}
	return strings.Trim(strings.TrimSpace(task.Actions.Exec[0].Command), `"`), on && trigger
}

// commandExe returns the program of a command line such as `"C:\a b\x.exe" --flag`.
func commandExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if rest, ok := strings.CutPrefix(cmd, `"`); ok {
		exe, _, _ := strings.Cut(rest, `"`)
		return exe
	}
	exe, _, _ := strings.Cut(cmd, " ")
	return exe
}

func samePath(a, b string) bool {
	return a != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func userSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
