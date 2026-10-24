package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"raydock/internal/xray"
)

// UpdateGeo fetches the newest rule data (geoip.dat, geosite.dat) when it differs from the installed one.
// The download goes through RayDock while connected and the core keeps running; a connection then
// reconnects to load the new data.
func (a *App) UpdateGeo() {
	if a.closing || a.GeoBusy || a.CoreBusy {
		return
	}
	if !a.CoreReady() {
		a.GeoErr = "请先安装 Xray 核心"
		return
	}
	a.GeoBusy, a.GeoProgress, a.GeoErr, a.GeoMsg = true, 0, "", ""
	client, dir, current := a.httpClient(0), a.CoreDir(), a.State.GeoTag
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(a.workCtx, 10*time.Minute)
		defer cancel()
		tag, err := a.CoreSource.LatestGeo(ctx, client)
		if err == nil && tag != current {
			last := time.Now()
			err = a.CoreSource.InstallGeo(ctx, client, tag, dir, func(done, total int64) {
				if total > 0 && (time.Since(last) > 100*time.Millisecond || done == total) {
					last = time.Now()
					p := float64(done) / float64(total)
					a.Post(func() { a.GeoProgress = p })
				}
			})
		}
		a.Post(func() {
			a.GeoBusy = false
			updated := false
			switch {
			case err != nil:
				a.GeoErr = "更新规则数据失败：" + err.Error()
				a.QueueLog("[RayDock] " + a.GeoErr)
			case tag == current:
				a.GeoMsg = "已是最新"
			default:
				a.State.GeoTag = tag
				a.Save()
				a.GeoMsg = "已更新"
				a.QueueLog("[RayDock] 已更新规则数据 " + tag)
				updated = true
			}
			if job := a.afterGeo; job != nil {
				a.afterGeo = nil
				job() // restarts a connection itself
			} else if updated && a.run != nil && a.run.b.Connected() {
				a.RenewCores() // a core loads rule data when it starts: a new backend core takes the new
			}
		})
	}()
}

// GeoVersion describes the installed rule data: the release date of an update, else the date of the
// files that came with the core. Empty when there are none.
func (a *App) GeoVersion() string {
	if t, err := time.Parse("200601021504", a.State.GeoTag); err == nil {
		return t.Format("2006-01-02")
	}
	if a.State.GeoTag != "" {
		return a.State.GeoTag
	}
	fi, err := os.Stat(filepath.Join(a.CoreDir(), xray.GeoFiles[1]))
	if err != nil {
		return ""
	}
	return "随核心安装 · " + fi.ModTime().Format("2006-01-02")
}
