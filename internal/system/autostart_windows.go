package system

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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

// taskArgs is where the logon task takes the arguments a run of it asks for (RelaunchElevated). At logon it
// gets none, and passes "$(Arg0)" as it is, which RayDock ignores.
const taskArgs = "$(Arg0)"

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
//
// Elevated, it brings a task of an earlier version, which takes no arguments, up to date, so that it can
// relaunch RayDock (RelaunchElevated).
func Autostart(exe string) StartMode {
	if t, ok := taskDetails(taskName); ok && t.on && samePath(t.cmd, exe) {
		if !t.takesArgs() && IsElevated() {
			if sid, err := userSID(); err == nil {
				_ = applyTask(true, exe, sid)
			}
		}
		return StartAdmin
	}
	r, err := readRunEntry()
	if err != nil || !samePath(commandExe(r.command), exe) || r.disabled() {
		return StartOff
	}
	return StartUser
}

// SetAutostart makes exe start at logon in mode and removes the other mode.
// Creating or deleting the scheduled task needs administrator rights; without
// them it asks once through UAC.
func SetAutostart(mode StartMode, exe string) error {
	if mode < StartOff || mode > StartAdmin {
		return errors.New("未知自启模式")
	}
	if mode == StartAdmin {
		if !IsElevated() && !splitAdmin() {
			return errors.New("当前账户不是管理员，无法以管理员身份自启")
		}
	}
	before, err := readRunEntry()
	if err != nil {
		return err
	}
	foreign := before.command != "" && !samePath(commandExe(before.command), exe)
	if foreign && mode == StartUser {
		return errors.New("普通自启已指向另一份 RayDock，请先在该安装中关闭")
	}
	next := before
	if !foreign && (before.command != "" || mode == StartUser) {
		next = runEntry{}
		if mode == StartUser {
			next.command = "\"" + exe + "\" " + AutostartArg
		}
	}
	if !before.equal(next) {
		if err := writeRunEntry(next); err != nil {
			return errors.Join(err, writeRunEntry(before))
		}
	}
	if mode == StartAdmin {
		err = changeTask(true, exe)
	} else {
		err = removeTask(exe)
	}
	if err == nil || before.equal(next) {
		return err
	}
	// A cancelled or rejected UAC request must restore the user's former Run settings.
	// Do not overwrite another change made while the UAC prompt was open.
	cur, readErr := readRunEntry()
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if !cur.equal(next) {
		return errors.Join(err, errors.New("自启设置已被其他程序修改，未覆盖"))
	}
	return errors.Join(err, writeRunEntry(before))
}

type runEntry struct {
	command     string
	approved    []byte
	hasApproval bool
}

func (r runEntry) equal(s runEntry) bool {
	return r.command == s.command && r.hasApproval == s.hasApproval && bytes.Equal(r.approved, s.approved)
}

func readRunEntry() (r runEntry, err error) {
	if k, e := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE); e == nil {
		r.command, _, err = k.GetStringValue(runValue)
		k.Close()
	} else {
		err = e
	}
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return r, err
	}
	if k, e := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE); e == nil {
		r.approved, _, err = k.GetBinaryValue(runValue)
		k.Close()
		r.hasApproval = err == nil
	} else {
		err = e
	}
	if errors.Is(err, registry.ErrNotExist) {
		err = nil
	}
	return r, err
}

func writeRunEntry(r runEntry) error {
	if r.command == "" {
		if err := deleteValue(runKey); err != nil {
			return err
		}
	} else {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue(runValue, r.command)
		k.Close()
		if err != nil {
			return err
		}
	}
	if !r.hasApproval {
		return deleteValue(approvedKey)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetBinaryValue(runValue, r.approved)
}

func ownedTask(exe, sid string) (present, owned bool, err error) {
	raw, err := readTaskDefinition(taskName)
	if taskMissing(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	t := parseTask(raw)
	if t.cmd == "" || t.user == "" {
		return true, false, errors.New("无法核实 RayDock 自启任务的执行程序和账户")
	}
	return true, samePath(t.cmd, exe) && sameUser(t.user, sid), nil
}

func removeTask(exe string) error {
	sid, err := userSID()
	if err != nil {
		return err
	}
	_, owned, err := ownedTask(exe, sid)
	if err != nil || !owned {
		return err
	}
	return changeTask(false, exe)
}

// deleteValue removes RayDock's value from key; a value or key that is not there is no error.
func deleteValue(key string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.SET_VALUE)
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

// CleanupAutostart removes the logon entries that start exe, as RayDock is uninstalled: the Run value
// and the logon task, which needs administrator rights, asked once through UAC. Entries of another
// installation are left alone.
func CleanupAutostart(exe string) error { return SetAutostart(StartOff, exe) }

func (r runEntry) disabled() bool { return len(r.approved) > 0 && r.approved[0]&1 == 1 }

// Helpers receive the original user SID even when UAC uses another account.
const taskHelperArg = "--autostart-task="

func changeTask(create bool, exe string) error {
	sid, err := userSID()
	if err != nil {
		return err
	}
	if IsElevated() {
		return applyTask(create, exe, sid)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	action := "create:"
	if !create {
		action = "remove:"
	}
	code, err := runElevated(self, taskHelperArg+action+sid)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("计划任务操作失败：%w", hresultError(code))
	}
	return nil
}

func applyTask(create bool, exe, sid string) error {
	if _, err := windows.StringToSid(sid); err != nil {
		return err
	}
	present, owned, err := ownedTask(exe, sid)
	if err != nil {
		return err
	}
	if present && !owned {
		return errors.New("计划任务属于另一份 RayDock 或其他账户，未修改")
	}
	if !create {
		if !present {
			return nil
		}
		return deleteTask(taskName)
	}
	return registerTask(taskName, taskXML(exe, sid))
}

// TaskHelper does the change of the logon task an unelevated RayDock asked for through UAC (changeTask),
// when args ask for one, without a window. It reports whether they did, and the exit code, 0 or the
// failure's HRESULT.
//
// The request is its only argument: a run of the logon task passes AutostartArg before what it is given,
// so that no program can have the task's administrator rights change the task through it.
func TaskHelper(args []string) (code int, ok bool) {
	if len(args) != 1 {
		return 0, false
	}
	if raw, found := strings.CutPrefix(args[0], taskHelperArg); found {
		op, sid, _ := strings.Cut(raw, ":")
		exe, err := os.Executable()
		if err == nil {
			if op == "create" || op == "remove" {
				err = applyTask(op == "create", exe, sid)
			} else {
				err = errors.New("未知任务操作")
			}
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
    <MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy>
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
    <Exec><Command>` + esc(exe) + `</Command><Arguments>` + AutostartArg + ` ` + taskArgs + `</Arguments><WorkingDirectory>` + esc(filepath.Dir(exe)) + `</WorkingDirectory></Exec>
  </Actions>
</Task>
`
}

func taskMissing(err error) bool {
	var hr hresult
	return errors.As(err, &hr) && uint32(hr) == hrFileNotFound
}

// taskInfo is what RayDock reads of a logon task: the program it starts, its arguments, and whether it
// starts it at the user's logon.
type taskInfo struct {
	cmd, args, user string
	on              bool
}

// takesArgs reports whether a run of the task can pass arguments (taskArgs).
func (t taskInfo) takesArgs() bool { return slices.Contains(strings.Fields(t.args), taskArgs) }

// taskDetails reads task name for the user; ok is false when there is none, or it cannot be read.
func taskDetails(name string) (taskInfo, bool) {
	raw, ok := taskDefinition(name)
	if !ok {
		return taskInfo{}, false
	}
	sid, err := userSID()
	if err != nil {
		return taskInfo{}, true
	}
	return parseTask(raw, sid), true
}

// runAdminTask starts exe with administrator rights through the user's logon task, with args, without a
// UAC prompt: the task runs it with the highest rights the user has, which are administrator rights only
// for an administrator (splitAdmin).
func runAdminTask(exe string, args []string) error {
	if !splitAdmin() {
		return errors.New("当前用户不是管理员，无法经自启任务获得管理员权限")
	}
	t, ok := taskDetails(taskName)
	if !ok || !t.on || !samePath(t.cmd, exe) || !t.takesArgs() {
		return errors.New("没有可以启动 RayDock 的管理员自启任务")
	}
	return runTask(taskName, strings.Join(args, " "))
}

// parseTask treats a disabled task or disabled logon trigger as inactive, so selecting admin mode can
// re-enable it rather than returning early with a stale enabled state.
func parseTask(raw string, user ...string) taskInfo {
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
			Exec  []struct{ Command, Arguments string }
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
		return taskInfo{}
	}
	on := task.Settings.Enabled == nil || *task.Settings.Enabled
	trigger := false
	for _, t := range task.Triggers.LogonTrigger {
		enabled := t.Enabled == nil || *t.Enabled
		if len(user) > 0 {
			enabled = enabled && (t.UserID == "" || sameUser(t.UserID, user[0]))
		}
		trigger = trigger || enabled
	}
	if len(user) > 0 {
		principal := false
		for _, p := range task.Principals.Principal {
			principal = principal || (sameUser(p.UserID, user[0]) && p.RunLevel == "HighestAvailable")
		}
		on = on && principal
	}
	exec := task.Actions.Exec[0]
	info := taskInfo{cmd: strings.Trim(strings.TrimSpace(exec.Command), `"`), args: exec.Arguments, on: on && trigger}
	if len(task.Principals.Principal) == 1 {
		info.user = task.Principals.Principal[0].UserID
	}
	return info
}

// sameUser reports whether a task's user id, a SID or an account name as Task Scheduler writes back the SID
// it was given (DOMAIN\name), is the user of SID sid.
func sameUser(id, sid string) bool {
	if strings.EqualFold(id, sid) {
		return true
	}
	s, _, _, err := windows.LookupSID("", id)
	return err == nil && strings.EqualFold(s.String(), sid)
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
