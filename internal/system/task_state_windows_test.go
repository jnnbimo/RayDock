package system

import (
	"strings"
	"testing"
)

func TestTaskDisabledState(t *testing.T) {
	src := taskXML(`C:\safe\RayDock.exe`, "S-1-5-21-1")
	if cmd, on := parseTask(src); cmd != `C:\safe\RayDock.exe` || !on {
		t.Fatal("enabled task not recognized", cmd, on)
	}
	for _, raw := range []string{strings.ReplaceAll(src, "<Enabled>true</Enabled>", "<Enabled>false</Enabled>"), strings.Replace(src, "<LogonTrigger><Enabled>true</Enabled>", "<LogonTrigger><Enabled>false</Enabled>", 1)} {
		if cmd, on := parseTask(raw); cmd != `C:\safe\RayDock.exe` || on {
			t.Fatal("disabled task reported active", cmd, on)
		}
	}
	if _, on := parseTask("<broken"); on {
		t.Fatal("malformed task reported active")
	}
}
