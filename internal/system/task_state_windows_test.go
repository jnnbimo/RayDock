package system

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTaskDisabledState(t *testing.T) {
	src := taskXML(`C:\safe\RayDock.exe`, "S-1-5-21-1")
	if ti := parseTask(src); ti.cmd != `C:\safe\RayDock.exe` || !ti.on || !ti.takesArgs() {
		t.Fatal("enabled task not recognized", ti)
	}
	for _, raw := range []string{strings.ReplaceAll(src, "<Enabled>true</Enabled>", "<Enabled>false</Enabled>"), strings.Replace(src, "<LogonTrigger><Enabled>true</Enabled>", "<LogonTrigger><Enabled>false</Enabled>", 1)} {
		if ti := parseTask(raw); ti.cmd != `C:\safe\RayDock.exe` || ti.on {
			t.Fatal("disabled task reported active", ti)
		}
	}
	if parseTask("<broken").on {
		t.Fatal("malformed task reported active")
	}
	// An earlier version's task takes no arguments: RayDock does not relaunch through it.
	if ti := parseTask(strings.Replace(src, " "+taskArgs+"</Arguments>", "</Arguments>", 1)); !ti.on || ti.takesArgs() {
		t.Fatal("an earlier task read as taking arguments", ti)
	}
}

// A run of a task passes its arguments in place of $(Arg0) (runTask), as a relaunch through the logon task
// needs. The task here runs with the user's own rights, and goes after.
func TestRunTaskPassesArguments(t *testing.T) {
	name := "RayDock Test Run " + strconv.Itoa(os.Getpid())
	out := filepath.Join(t.TempDir(), "args.txt")
	xml := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Principals><Principal id="Author"><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy></Settings>
  <Actions Context="Author"><Exec><Command>cmd.exe</Command><Arguments>/c echo ` + AutostartArg + ` ` + taskArgs + `&gt;"` + out + `"</Arguments></Exec></Actions>
</Task>`
	if err := registerTask(name, xml); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deleteTask(name) })
	if err := runTask(name, "--wait-pid=42 --connect"); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if got, _ = os.ReadFile(out); len(got) > 0 {
			break
		}
	}
	if strings.TrimSpace(string(got)) != AutostartArg+" --wait-pid=42 --connect" {
		t.Fatalf("the task ran with %q", got)
	}
}

// Task Scheduler stores the logon trigger's user, registered as a SID, as DOMAIN\name: the task read back
// is the user's, and another user's is not.
func TestTaskUserAsStored(t *testing.T) {
	sid, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	s, err := windows.StringToSid(sid)
	if err != nil {
		t.Fatal(err)
	}
	account, domain, _, err := s.LookupAccount("")
	if err != nil {
		t.Fatal(err)
	}
	src := taskXML(`D:\RayDock\RayDock.exe`, sid)
	stored := strings.Replace(src, "<UserId>"+sid+"</UserId><Delay>", "<UserId>"+domain+`\`+account+"</UserId><Delay>", 1)
	if stored == src {
		t.Fatal("the trigger's user was not rewritten")
	}
	if ti := parseTask(stored, sid); ti.cmd != `D:\RayDock\RayDock.exe` || !ti.on {
		t.Fatalf("the user's task read as %+v", ti)
	}
	other := strings.Replace(src, "<UserId>"+sid+"</UserId><Delay>", `<UserId>NT AUTHORITY\SYSTEM</UserId><Delay>`, 1)
	if parseTask(other, sid).on {
		t.Fatal("another user's logon task read as the user's")
	}
}
