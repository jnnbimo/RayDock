package gui

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
	"raydock/internal/store"
	"raydock/internal/system"
)

// Showing the window at logon is a choice once RayDock starts at logon, and it is saved.
func TestAutostartShowSetting(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	v := New(a)
	v.router.Push("/settings")
	tt := ui.NewTester(v.Build, MainWidth, MainHeight)
	tt.Frame()
	sw := func() ui.AccessInfo {
		t.Helper()
		info, ok := tt.AccessByRole(ui.RoleSwitch, "自启时打开窗口")
		if !ok {
			t.Fatalf("no switch: %q", tt.Texts())
		}
		return info
	}
	if !sw().Disabled {
		t.Fatal("the switch takes input without start at logon")
	}
	a.StartMode = system.StartUser
	tt.Frame()
	if info := sw(); info.Disabled || info.Checked {
		t.Fatalf("switch %+v", info)
	}
	b := sw().Bounds
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
	tt.Frame()
	if !a.State.Settings.AutostartShow || !sw().Checked {
		t.Fatal("the switch did not turn on")
	}
	st, err := store.Load(filepath.Join(a.DataDir, "state.json"))
	if err != nil || !st.Settings.AutostartShow {
		t.Fatalf("saved %v, %v", st.Settings.AutostartShow, err)
	}
}

// A newer release, found when asked, shows in 关于, whose button opens the download page; nothing else
// tells of it. A build without a version has no update row.
func TestUpdateOffer(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	v := New(a)
	v.router.Push("/settings")
	v.settingsTab = len(settingsTabs) - 1
	tt := ui.NewTester(v.Build, MainWidth, MainHeight)
	tt.Frame()
	if tt.HasText("检查更新") {
		t.Fatal("a build without a version offers to check")
	}
	a.Version = "0.1.7"
	tt.Frame()
	shows := func(s string) bool { return slices.Contains(tt.Texts(), s) }
	if !shows("检查更新") || shows("前往下载") {
		t.Fatalf("texts %q", tt.Texts())
	}
	a.UpdateLatest = "v0.1.8"
	tt.Frame()
	if !shows("有新版本 v0.1.8") || !shows("前往下载") || shows("检查更新") || shows("新版本") {
		t.Fatalf("texts %q", tt.Texts())
	}
	clickFrame(t, tt, "前往下载")
	if len(h.URLs) != 1 || h.URLs[0] != app.ReleasesPage {
		t.Fatalf("opened %q", h.URLs)
	}
}
