package app

import (
	"errors"

	"raydock/internal/system"
)

// LoadAutostart reads the start-at-logon mode from the system, the only place it is kept, and warns of a
// start with administrator rights that others could take over.
func (a *App) LoadAutostart() {
	if a.Autostart == nil || a.closing {
		return
	}
	a.StartBusy = true
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		m, risk := a.Autostart.Get(), a.Autostart.AdminRisk()
		a.Post(func() {
			a.StartMode, a.StartBusy = m, false
			if m == system.StartAdmin && risk != nil {
				a.StartErr = "管理员自启存在风险：" + risk.Error() + "。也可以改为“普通”"
				a.QueueLog("[RayDock] " + a.StartErr)
			}
		})
	}()
}

// SetAutostart switches the start-at-logon mode. The admin mode may show a UAC
// prompt, so it runs off the UI thread; the result is read back from the system.
func (a *App) SetAutostart(m system.StartMode) {
	if a.closing || a.Autostart == nil || a.StartBusy || m == a.StartMode {
		return
	}
	a.StartBusy, a.StartErr = true, ""
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		err := a.Autostart.Set(m)
		got := a.Autostart.Get()
		a.Post(func() {
			a.StartMode, a.StartBusy = got, false
			switch {
			case errors.Is(err, system.ErrUACCancelled):
				a.StartErr = "已取消：管理员自启需要确认一次 UAC"
			case err != nil:
				a.StartErr = "设置开机自启失败：" + err.Error()
				a.QueueLog("[RayDock] " + a.StartErr)
			}
		})
	}()
}
