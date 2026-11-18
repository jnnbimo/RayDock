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

	"raydock/internal/xray"
)

// GeoSource provides rule data releases; production uses GitHub. The core itself comes only with RayDock:
// each release brings the core it was made and tested with.
type GeoSource interface {
	LatestGeo(ctx context.Context, client *http.Client) (string, error)
	InstallGeo(ctx context.Context, client *http.Client, tag, dir string, progress func(done, total int64)) error
}

type OfficialGeo struct{}

func (OfficialGeo) LatestGeo(ctx context.Context, client *http.Client) (string, error) {
	return xray.LatestGeo(ctx, client)
}

func (OfficialGeo) InstallGeo(ctx context.Context, client *http.Client, tag, dir string, progress func(done, total int64)) error {
	return xray.InstallGeo(ctx, client, tag, dir, progress)
}

func (a *App) bundledReady() bool {
	return a.BundledCore != "" && len(xray.Missing(a.BundledCore)) == 0
}

// EnsureCore runs at startup, and when a connection finds no core it can use. It keeps the core in the
// data directory the one RayDock brings (BundledCore):
//   - missing or broken: install the bundled core again;
//   - the bundled core is newer (RayDock was upgraded): install it.
//
// RayDock never looks for or downloads a core: a newer one comes with a newer RayDock.
func (a *App) EnsureCore() {
	if a.CoreBusy || a.closing {
		return
	}
	if a.GeoBusy {
		a.afterGeo = a.EnsureCore
		return
	}
	a.CoreErr = "" // this check tells again
	recovering := xray.RecoveryPending(a.CoreDir())
	if !recovering {
		if missing := xray.Missing(a.CoreDir()); len(missing) > 0 {
			a.CoreVersion = ""
			a.repairCore("缺少 " + strings.Join(missing, "、"))
			return
		}
	}
	// Recovery owns the files before any version check or process can use them.
	a.CoreBusy, a.coreHold, a.CoreMsg = true, true, "正在检查核心版本…"
	a.coreRevision++
	revision, dir, exe, readVersion := a.coreRevision, a.CoreDir(), a.coreExe(), a.ReadCoreVersion
	bundledExe := ""
	if a.bundledReady() {
		bundledExe = filepath.Join(a.BundledCore, xray.ExeName())
	}
	check := func() {
		// No install or launch may run after a recovery failure.
		var recoveryErr error
		if recovering {
			recoveryErr = xray.RecoverFiles(dir)
		}
		var v string
		var err error
		if recoveryErr == nil {
			if missing := xray.Missing(dir); len(missing) > 0 {
				err = fmt.Errorf("缺少 %s", strings.Join(missing, "、"))
			} else {
				v, err = checkedVersion(readVersion, exe)
			}
		}
		bv := ""
		if recoveryErr == nil && err == nil && bundledExe != "" {
			bv, _ = checkedVersion(readVersion, bundledExe)
		}
		_, tagErr := os.Stat(filepath.Join(dir, xray.GeoTagFile))
		geoTag := xray.ReadGeoTag(dir)
		a.Post(func() {
			if revision != a.coreRevision {
				return
			}
			a.CoreBusy, a.coreHold, a.CoreMsg, a.CoreVersion = false, false, "", v
			if recoveryErr == nil && err == nil && tagErr == nil && a.State.GeoTag != geoTag {
				a.State.GeoTag = geoTag
				a.Save()
			}
			switch {
			case recoveryErr != nil:
				a.CoreErr = "核心文件恢复失败：" + recoveryErr.Error()
				a.QueueLog("[RayDock] " + a.CoreErr)
				if a.WaitCore {
					a.endConnection(a.CoreErr)
				}
			case err != nil:
				a.repairCore("Xray 核心不可用（" + err.Error() + "）")
			case newerVersion(bv, v):
				a.seedCore()
			case a.WaitCore && !a.coreUsable(a.waitOpts.TUN.Enabled):
				a.endConnection(a.unusableReason())
			case a.seedGeo():
				// which then connects, and starts the core, as below
			default:
				a.resumeConnect(change{})
				a.SyncCores()
				a.PinCertificates()
			}
		})
	}
	if recovering {
		a.replaceFiles(check)
	} else {
		a.jobs.Go(check)
	}
}

// repairCore installs the bundled core over a missing or broken one, for problem. Without a bundled core
// only reinstalling RayDock brings one back, and a connection waiting for it ends.
func (a *App) repairCore(problem string) {
	if a.bundledReady() {
		a.QueueLog("[RayDock] " + problem + "，重新安装自带的 Xray 核心")
		a.seedCore()
		return
	}
	a.CoreErr = problem + "，请重新安装 RayDock"
	a.QueueLog("[RayDock] " + a.CoreErr)
	if a.WaitCore {
		a.endConnection(a.CoreErr)
	}
}

// unusableReason tells why the core cannot serve the connection waiting for it.
func (a *App) unusableReason() string {
	if a.CoreErr != "" {
		return a.CoreErr
	}
	if !a.CoreReady() {
		return "Xray 核心未通过验证，请重新安装 RayDock"
	}
	return "TUN 模式需要 Xray " + xray.MinTUNVersion + " 或更新的核心，当前是 " + cmp.Or(a.CoreVersion, "未知版本") + "，请更新 RayDock"
}

func (a *App) coreUsable(tun bool) bool {
	return a.CoreReady() && (!tun || !tunTooOld(a.CoreVersion))
}

func checkedVersion(read func(string) (string, error), exe string) (string, error) {
	v, err := read(exe)
	if err != nil {
		return "", err
	}
	if len(parseVersion(v)) != 3 {
		return "", fmt.Errorf("无法识别的 Xray 版本 %q", v)
	}
	return v, nil
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

// seedCore installs the core RayDock brings into the data directory (replaceCore), offline. Everything
// keeps running meanwhile, a connection too.
func (a *App) seedCore() {
	if a.CoreBusy || a.closing {
		return
	}
	if a.GeoBusy {
		// Both write the core directory, and the rule data to keep depends on the update's outcome.
		a.afterGeo = a.seedCore
		return
	}
	a.coreRevision++ // a version check still running reads files about to be replaced
	a.CoreBusy, a.CoreErr, a.CoreMsg = true, "", "正在安装自带的 Xray 核心…"
	a.replaceCore(a.BundledCore)
}

// replaceCore replaces the core files with those in src, while every core goes on running: Windows lets a
// running executable be moved aside, so the old processes keep their files and new ones start from the
// new. Nothing new may start from the files meanwhile: tests running are waited for, the core operations
// wait their turn. Then the cores restart from the new files (coreDone).
func (a *App) replaceCore(src string) {
	dir, readVersion := a.CoreDir(), a.ReadCoreVersion
	// Publish the new executable and bundled rules in a single commit if both are needed.
	geoTag, keep := xray.ReadGeoTag(src), xray.GeoFiles
	missing := xray.Missing(dir)
	newGeo := xray.NewerGeo(geoTag, a.State.GeoTag) || slices.ContainsFunc(xray.GeoFiles, func(f string) bool { return slices.Contains(missing, f) })
	if newGeo {
		keep = nil
	}
	a.replaceFiles(func() {
		candidate, err := os.MkdirTemp(filepath.Dir(dir), "core-candidate-")
		v := ""
		if err == nil {
			defer os.RemoveAll(candidate)
			err = xray.CopyCore(src, candidate, nil)
			if err == nil && newGeo && geoTag == "" {
				err = os.WriteFile(filepath.Join(candidate, xray.GeoTagFile), nil, 0o600)
			}
		}
		if err == nil {
			v, err = checkedVersion(readVersion, filepath.Join(candidate, xray.ExeName()))
			if err != nil {
				err = fmt.Errorf("核心版本验证失败：%w", err)
			}
		}
		if err == nil {
			err = xray.CopyCore(candidate, dir, keep)
		}
		installed := err == nil
		a.Post(func() {
			if installed && newGeo {
				a.State.GeoTag = geoTag // the release of the rule data now installed, "" for unknown
				a.Save()
			}
			a.coreDone(installed, v, err)
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
	a.jobs.Go(func() {
		a.tests.Wait()
		a.Post(func() { a.coreDo(replace) })
	})
}

// coreDone ends installing the core: installed tells whether the files were replaced, v is the version
// then verified. A connection waiting for the core starts, on the old core if the new one could not be
// placed, or ends with the reason when no core it can use is there; a core running restarts from the new
// files.
func (a *App) coreDone(installed bool, v string, err error) {
	a.CoreBusy, a.coreHold, a.CoreMsg = false, false, ""
	defer a.SyncCores()
	defer a.PinCertificates()
	if installed {
		a.CoreVersion = v
	} else if xray.RecoveryPending(a.CoreDir()) {
		a.CoreVersion = "" // the previous file set could not be restored
	}
	if err != nil {
		a.CoreErr = "安装 Xray 核心失败：" + err.Error()
		a.QueueLog("[RayDock] " + a.CoreErr)
	} else {
		a.QueueLog("[RayDock] 已安装 Xray " + cmp.Or(v, "核心"))
	}
	switch {
	case a.WaitCore && !a.coreUsable(a.waitOpts.TUN.Enabled):
		a.endConnection(a.unusableReason())
	case a.WaitCore:
		a.resumeConnect(change{files: installed})
	case installed && a.planned.core() != nil:
		a.QueueLog("[RayDock] 改用新核心")
		a.RenewCores(change{files: true})
	}
}
