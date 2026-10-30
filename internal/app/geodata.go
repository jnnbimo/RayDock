package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"raydock/internal/xray"
)

// UpdateGeo downloads a verified rule set away from the live core directory. Only
// its final commit shares the core-file barrier; connections keep running meanwhile.
func (a *App) UpdateGeo() {
	if a.closing || a.GeoBusy || a.CoreBusy {
		return
	}
	if !a.CoreReady() {
		a.GeoErr = "请先安装 Xray 核心"
		return
	}
	a.GeoBusy, a.GeoProgress, a.GeoErr, a.GeoMsg = true, 0, "", ""
	client, dir, current := a.httpClient(0), a.geoDownloadDir(), a.State.GeoTag
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(a.workCtx, 10*time.Minute)
		defer cancel()
		tag, err := a.CoreSource.LatestGeo(ctx, client)
		if err == nil && tag != current {
			err = os.RemoveAll(dir)
			if err == nil {
				err = os.MkdirAll(dir, 0o755)
			}
			if err == nil {
				last := time.Now()
				err = a.CoreSource.InstallGeo(ctx, client, tag, dir, func(done, total int64) {
					if total > 0 && (time.Since(last) > 100*time.Millisecond || done == total) {
						last = time.Now()
						p := float64(done) / float64(total)
						a.Post(func() { a.GeoProgress = p })
					}
				})
			}
		}
		a.Post(func() {
			if err != nil || tag == current {
				_ = os.RemoveAll(dir)
				a.geoDone(tag, current, err)
				return
			}
			a.GeoMsg = "正在替换规则数据…"
			liveDir := a.CoreDir()
			a.replaceFiles(func() {
				err := xray.CopyGeo(dir, liveDir)
				_ = os.RemoveAll(dir)
				a.Post(func() {
					a.coreHold = false
					a.geoDone(tag, current, err)
				})
			})
		})
	}()
}

func (a *App) geoDownloadDir() string { return filepath.Join(a.DataDir, "geo-download") }

// bundledGeoTag is the release of the rule data RayDock brings (xray.GeoTagFile), "" when unknown.
func (a *App) bundledGeoTag() string {
	if !a.bundledReady() {
		return ""
	}
	return xray.ReadGeoTag(a.BundledCore)
}

// seedGeo installs the rule data RayDock brings where it is newer than the installed: a RayDock upgrade
// brings its rules to an installation that never updated them, or did so before them. Installing a core
// keeps the rule data (replaceCore), so this is how they arrive. It reports whether it started; when it
// ends, it does what an update does (geoDone).
func (a *App) seedGeo() bool {
	tag := a.bundledGeoTag()
	if a.closing || a.GeoBusy || a.CoreBusy || !a.CoreReady() || !xray.NewerGeo(tag, a.State.GeoTag) {
		return false
	}
	a.GeoBusy, a.GeoProgress, a.GeoErr, a.GeoMsg = true, 0, "", "正在安装自带的规则数据…"
	src, dir, current := a.BundledCore, a.CoreDir(), a.State.GeoTag
	a.replaceFiles(func() {
		err := xray.CopyGeo(src, dir)
		a.Post(func() {
			a.coreHold = false
			a.geoDone(tag, current, err)
		})
	})
	return true
}

func (a *App) geoDone(tag, previous string, err error) {
	a.GeoBusy, a.GeoMsg = false, ""
	updated := err == nil && tag != previous
	switch {
	case err != nil:
		a.GeoErr = "更新规则数据失败：" + err.Error()
		a.QueueLog("[RayDock] " + a.GeoErr)
	case !updated:
		a.GeoMsg = "已是最新"
	default:
		a.State.GeoTag = tag
		a.Save()
		a.GeoMsg = "已更新"
		a.QueueLog("[RayDock] 已更新规则数据 " + tag)
	}
	if a.WaitCore {
		a.resumeConnect(change{geo: updated})
	} else if updated {
		a.RenewCores(change{geo: true})
	}
	a.SyncCores()
	a.PinCertificates()
	if job := a.afterGeo; job != nil {
		a.afterGeo = nil
		job()
	}
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
