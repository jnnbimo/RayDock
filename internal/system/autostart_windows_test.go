package system

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestCommandExe(t *testing.T) {
	for cmd, want := range map[string]string{
		`"C:\Program Files\RayDock\RayDock.exe" --autostart`: `C:\Program Files\RayDock\RayDock.exe`,
		`C:\RayDock\RayDock.exe --autostart`:                 `C:\RayDock\RayDock.exe`,
		`  "C:\a b\x.exe"`:                                   `C:\a b\x.exe`,
	} {
		if got := commandExe(cmd); got != want {
			t.Errorf("%q: got %q", cmd, got)
		}
	}
	if !samePath(`c:\program files\raydock\RAYDOCK.exe`, `C:\Program Files\RayDock\.\RayDock.exe`) || samePath("", "") {
		t.Error("samePath")
	}
}

func TestTaskXML(t *testing.T) {
	exe := `C:\Tools & Apps\<RayDock>\RayDock.exe`
	var task struct {
		Principal struct {
			UserID   string `xml:"UserId"`
			RunLevel string
		} `xml:"Principals>Principal"`
		Exec struct {
			Command, Arguments, WorkingDirectory string
		} `xml:"Actions>Exec"`
		Priority int `xml:"Settings>Priority"`
	}
	// The UTF-16 declaration is rewritten for the decoder; Task Scheduler gets the text as UTF-16.
	src := strings.Replace(taskXML(exe, "S-1-5-21-1"), `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	if err := xml.Unmarshal([]byte(src), &task); err != nil {
		t.Fatal(err)
	}
	if task.Exec.Command != exe || task.Exec.Arguments != AutostartArg || task.Exec.WorkingDirectory != `C:\Tools & Apps\<RayDock>` ||
		task.Principal.RunLevel != "HighestAvailable" || task.Principal.UserID != "S-1-5-21-1" || task.Priority != 4 {
		t.Errorf("%+v", task)
	}
}

// The Task Scheduler is reached through its COM interface: a task that does not exist reads as absent.
func TestTaskSchedulerMissingTask(t *testing.T) {
	if _, ok := taskCommand(`RayDock Test \ no such task`); ok {
		t.Fatal("a missing task reads as present")
	}
	if err := deleteTask("RayDock Test no such task"); err != nil {
		t.Fatalf("deleting a missing task: %v", err)
	}
}

// Only the task argument makes a run the elevated helper; its exit code carries the HRESULT.
func TestTaskHelperArgs(t *testing.T) {
	if _, ok := TaskHelper([]string{"--connect", AutostartArg}); ok {
		t.Fatal("ordinary arguments taken for the helper")
	}
	if code := int(hresult(0x80070005)); uint32(code) != 0x80070005 || hresult(uint32(code)).Error() == "" {
		t.Fatalf("code %x", code)
	}
}
