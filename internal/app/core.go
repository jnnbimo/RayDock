package app

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"raydock/internal/xray"
)

// CoreSource provides core and rule data releases; production uses GitHub.
type CoreSource interface {
	Latest(ctx context.Context, client *http.Client) (xray.Release, error)
	Install(ctx context.Context, client *http.Client, rel xray.Release, dir string, progress func(done, total int64)) error
	LatestGeo(ctx context.Context, client *http.Client) (string, error)
	InstallGeo(ctx context.Context, client *http.Client, tag, dir string, progress func(done, total int64)) error
}

type OfficialCore struct{}

func (OfficialCore) Latest(ctx context.Context, client *http.Client) (xray.Release, error) {
	return xray.LatestRelease(ctx, client)
}

func (OfficialCore) Install(ctx context.Context, client *http.Client, rel xray.Release, dir string, progress func(done, total int64)) error {
	return xray.Install(ctx, client, rel, dir, progress)
}

func (OfficialCore) LatestGeo(ctx context.Context, client *http.Client) (string, error) {
	return xray.LatestGeo(ctx, client)
}

func (OfficialCore) InstallGeo(ctx context.Context, client *http.Client, tag, dir string, progress func(done, total int64)) error {
	return xray.InstallGeo(ctx, client, tag, dir, progress)
}

func (a *App) bundledReady() bool {
	return a.BundledCore != "" && len(xray.Missing(a.BundledCore)) == 0
}

// EnsureCore runs at startup and keeps the core in the data directory ready:
//   - missing: copy the bundled core, or download it;
//   - broken: download again;
//   - bundled core is newer (RayDock was upgraded): copy it.
//
// It never looks for a newer release: updating is the user's choice (CheckCoreUpdate, InstallCore).
// The core always runs from the data directory, so online updates survive reinstalling RayDock.
func (a *App) EnsureCore() {
	if a.CoreBusy || a.closing {
		return
	}
	if a.GeoBusy {
		a.afterGeo = a.EnsureCore
		return
	}
	if missing := xray.Missing(a.CoreDir()); len(missing) > 0 {
		if a.bundledReady() {
			a.seedCore()
			return
		}
		a.QueueLog("[RayDock] 缺少 " + strings.Join(missing, "、") + "，自动下载官方 Xray 核心")
		a.InstallCore()
		return
	}
	// Publish the local version before making any network request. Connect waits for this check.
	a.CoreBusy, a.coreHold, a.CoreMsg = true, true, "正在检查核心版本…"
	a.coreRevision++
	revision, exe, readVersion := a.coreRevision, a.coreExe(), a.ReadCoreVersion
	bundledExe := ""
	if a.bundledReady() {
		bundledExe = filepath.Join(a.BundledCore, xray.ExeName())
	}
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		v, err := readVersion(exe)
		bv := ""
		if err == nil && bundledExe != "" {
			bv, _ = readVersion(bundledExe)
		}
		a.Post(func() {
			if revision != a.coreRevision {
				return
			}
			a.CoreBusy, a.coreHold, a.CoreMsg, a.CoreVersion = false, false, "", v
			if err != nil {
				a.CoreVersion = ""
				a.QueueLog("[RayDock] Xray 核心无法运行（" + err.Error() + "），重新下载")
				a.InstallCore()
				return
			}
			if newerVersion(bv, v) && (a.Conn == StateOff || a.WaitCore || a.planned.core().tun() && tunTooOld(v)) {
				a.seedCore()
				return
			}
			if a.WaitCore && a.waitOpts.TUN.Enabled && tunTooOld(v) {
				a.InstallCore()
				return
			}
			if newerVersion(bv, v) {
				a.CoreLatest, a.latestBundled = bv, true
			}
			if a.seedGeo() {
				return // which then connects, and starts the core, as below
			}
			a.resumeConnect(change{})
			a.SyncCores()
			a.PinCertificates()
		})
	}()
}

// CheckCoreUpdate looks for a newer official release, when the user asks: it offers one (CoreLatest) and
// installs nothing. A lookup cannot delay local readiness or overwrite the result of a later installation.
func (a *App) CheckCoreUpdate() {
	if a.closing || a.CoreBusy || a.CoreChecking {
		return
	}
	a.CoreChecking, a.CoreNote = true, ""
	revision, current, client := a.coreRevision, a.CoreVersion, a.httpClient(0)
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(a.workCtx, 30*time.Second)
		defer cancel()
		rel, err := a.CoreSource.Latest(ctx, client)
		a.Post(func() {
			a.CoreChecking = false
			if revision != a.coreRevision {
				return
			}
			switch {
			case err != nil:
				a.CoreNote = "检查更新失败：" + err.Error()
				a.QueueLog("[RayDock] " + a.CoreNote)
			case newerVersion(rel.Tag, current):
				a.CoreLatest, a.latestBundled = rel.Tag, false
			default:
				a.CoreNote = "已是最新版本"
			}
		})
	}()
}

func (a *App) coreUsable(tun bool) bool {
	return a.CoreReady() && (!tun || !tunTooOld(a.CoreVersion))
}

// Unknown or malformed versions cannot prove TUN support.
func tunTooOld(v string) bool { return parseVersion(v) == nil || newerVersion(xray.MinTUNVersion, v) }

func parseVersion(s string) []int {
	var out []int
	for _, f := range strings.Split(strings.TrimPrefix(s, "v"), ".") {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func newerVersion(tag, cur string) bool {
	t, c := parseVersion(tag), parseVersion(cur)
	return t != nil && c != nil && slices.Compare(t, c) > 0
}

// UpdateCore installs the core offered (CoreLatest): the bundled one offline when it is that, else the newest
// official release.
func (a *App) UpdateCore() {
	if a.latestBundled && a.bundledReady() {
		a.seedCore()
		return
	}
	a.InstallCore()
}

// InstallCore downloads the newest official core and installs it (coreJob).
func (a *App) InstallCore() {
	a.coreJob("正在查询最新版本…", "下载", func(client *http.Client) (string, error) {
		ctx, cancel := context.WithTimeout(a.workCtx, 10*time.Minute)
		defer cancel()
		rel, err := a.CoreSource.Latest(ctx, client)
		if err != nil {
			return "", err
		}
		a.Post(func() { a.CoreMsg = "正在下载 Xray " + rel.Tag + "…" })
		dir := a.downloadDir()
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		last := time.Now()
		return dir, a.CoreSource.Install(ctx, client, rel, dir, func(done, total int64) {
			if total > 0 && (time.Since(last) > 100*time.Millisecond || done == total) {
				last = time.Now()
				p := float64(done) / float64(total)
				a.Post(func() { a.CoreProgress = p })
			}
		})
	})
}

// downloadDir is where a core download is placed until it replaces the core.
func (a *App) downloadDir() string { return filepath.Join(a.DataDir, "core-download") }

// seedCore installs the bundled core (coreJob), offline.
func (a *App) seedCore() {
	src := a.BundledCore
	a.coreJob("正在安装自带的 Xray 核心…", "安装", func(*http.Client) (string, error) { return src, nil })
}

// coreJob places a new core in the data directory. prepare, in the background, returns the directory it
// is in: a download, or the bundled core. Everything keeps running meanwhile, a connection too, which a
// download goes through, and while the files are replaced (replaceCore).
func (a *App) coreJob(msg, verb string, prepare func(client *http.Client) (string, error)) {
	if a.CoreBusy || a.closing {
		return
	}
	if a.GeoBusy {
		// Both write the core directory, and the rule data to keep depends on the update's outcome.
		a.afterGeo = func() { a.coreJob(msg, verb, prepare) }
		return
	}
	a.coreRevision++ // invalidates any older release discovery result
	a.CoreBusy, a.CoreProgress, a.CoreErr, a.CoreMsg = true, 0, "", msg
	client := a.httpClient(0)
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		defer client.CloseIdleConnections()
		src, err := prepare(client)
		if err != nil {
			_ = os.RemoveAll(a.downloadDir()) // a partial download; CoreBusy keeps other jobs out
		}
		a.Post(func() {
			if err != nil {
				a.coreDone(verb, false, "", err)
				return
			}
			a.replaceCore(verb, src)
		})
	}()
}

// replaceCore replaces the core files with those in src, while every core goes on running: Windows lets a
// running executable be moved aside, so the old processes keep their files and new ones start from the
// new. Nothing new may start from the files meanwhile: tests running are waited for, the core operations
// wait their turn. Then the cores restart from the new files (coreDone).
func (a *App) replaceCore(verb, src string) {
	a.CoreMsg = "正在替换 Xray 核心…"
	dir, exe, readVersion, download := a.CoreDir(), a.coreExe(), a.ReadCoreVersion, a.downloadDir()
	// The rule data installed stays (CopyCore): it is updated by itself, or by the installer's (seedGeo).
	// Where there is none, the core's comes with it: the installer's, of a known release, or the official
	// package's own, of none, reduced to a few categories: the installer's then replaces it (coreDone).
	geoTag, newGeo := "", slices.ContainsFunc(xray.GeoFiles, func(f string) bool { return slices.Contains(xray.Missing(dir), f) })
	if newGeo {
		geoTag = xray.ReadGeoTag(src)
	}
	a.replaceFiles(func() {
		err := xray.CopyCore(src, dir, xray.GeoFiles)
		if src == download {
			_ = os.RemoveAll(download)
		}
		v := ""
		installed := err == nil
		if installed {
			if v, err = readVersion(exe); err != nil {
				v, err = "", fmt.Errorf("核心版本验证失败：%w", err)
			}
		}
		a.Post(func() {
			if installed && newGeo {
				a.State.GeoTag = geoTag // the release of the rule data now installed, "" for unknown
				a.Save()
			}
			a.coreDone(verb, installed, v, err)
		})
	})
}

// replaceFiles is the shared commit barrier for executables and rule data. Downloads
// are already staged elsewhere. Stop new tests, end those running, await their workers, then commit
// in core-operation order so no startup or API config load sees a partial file set.
// replace runs on the core queue and posts completion to release coreHold.
func (a *App) replaceFiles(replace func()) {
	a.coreHold = true
	a.StopSpeedTest()
	a.stopLatencyTest()
	a.stopPinning()
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		a.tests.Wait()
		a.Post(func() { a.coreDo(replace) })
	}()
}

// coreDone ends a core job: installed tells whether the files were replaced, v is the version then
// verified. A connection waiting for the core starts, on the old core if the new one could not be placed,
// or ends with the reason when no core it can use is there; a core running restarts from the new files.
func (a *App) coreDone(verb string, installed bool, v string, err error) {
	a.CoreBusy, a.coreHold, a.CoreMsg = false, false, ""
	defer a.SyncCores()
	defer a.PinCertificates()
	if installed {
		a.CoreVersion = v
	}
	if err != nil {
		// Replacement failures keep the old core; verification failures leave no trusted version.
		a.CoreErr = verb + " Xray 核心失败：" + err.Error()
		a.QueueLog("[RayDock] " + a.CoreErr)
	} else {
		a.CoreLatest, a.latestBundled = "", false
		a.QueueLog("[RayDock] 已安装 Xray " + cmp.Or(v, "核心"))
	}
	// The installer's rule data, where newer, comes first: the connection waiting starts with it, and the
	// core running restarts once (geoDone).
	seeding := a.seedGeo()
	switch {
	case a.WaitCore && !a.coreUsable(a.waitOpts.TUN.Enabled):
		reason := a.CoreErr
		if err == nil {
			reason = "TUN 模式需要 Xray " + xray.MinTUNVersion + " 或更新的核心，当前可用的是 " + v
		}
		a.endConnection(reason)
	case seeding:
	case a.WaitCore:
		a.resumeConnect(change{files: installed && err == nil})
	case installed && err == nil && a.planned.core() != nil:
		a.QueueLog("[RayDock] 改用新核心")
		a.RenewCores(change{files: true})
	}
}
