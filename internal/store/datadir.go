package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"raydock/internal/system"
)

// dataDirName is the folder beside RayDock.exe that holds its data: nodes, settings, traffic, the core.
// A copy of RayDock carries its data with it, and nothing is left in the user's profile.
const dataDirName = "data"

// DataDir returns the data directory beside the executable. Under go run and go test, whose
// executables are temporary, it is the one in the working directory.
func DataDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	base := filepath.Dir(exe)
	if goBuildDir(base) {
		if base, err = os.Getwd(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, dataDirName), nil
}

// goBuildDir reports whether dir is where go run or go test built an executable: $WORK/b001/exe or
// $WORK/b001, $WORK being a go-build temporary directory.
func goBuildDir(dir string) bool {
	for d, i := dir, 0; i < 3; d, i = filepath.Dir(d), i+1 {
		if strings.HasPrefix(filepath.Base(d), "go-build") {
			return true
		}
	}
	return false
}

// LegacyDataDir is where RayDock 0.1.4 and earlier kept their data: %APPDATA%\RayDock.
func LegacyDataDir() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cfg, "RayDock")
}

// PrepareDataDir creates dir, private to the user, and moves the data of an earlier version into it.
// An error means RayDock must not start: it would begin with empty data and, saving it, never move the
// earlier data. A warning reports leftovers of a move that otherwise succeeded.
func PrepareDataDir(dir string) (warning, err error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, fmt.Errorf("无法在程序所在目录创建数据目录 %s：%w。请把 RayDock 放到当前用户可写的位置", dir, err)
		}
		if err := system.PrivateDir(dir); err != nil {
			return nil, fmt.Errorf("无法限制数据目录 %s 的访问权限：%w", dir, err)
		}
	} else if err != nil {
		return nil, err
	}
	return moveLegacyData(LegacyDataDir(), dir)
}

// legacyData is what an earlier version's directory holds of lasting worth, state.json last. The rest,
// core and measurement configs and download staging, is written anew whenever it is used.
var legacyData = []string{"traffic.json", "window-state.json", "core", "state.json"}

// moveLegacyData moves the data in legacy into dir, unless dir already has data of its own. state.json
// is copied last: a move cut short leaves dir without it and is done again at the next start, while the
// earlier directory is only removed once all of its data is in dir.
func moveLegacyData(legacy, dir string) (warning, err error) {
	if legacy == "" || strings.EqualFold(filepath.Clean(legacy), filepath.Clean(dir)) ||
		!fileExists(filepath.Join(legacy, "state.json")) || fileExists(filepath.Join(dir, "state.json")) {
		return nil, nil
	}
	for _, name := range legacyData {
		src, dst := filepath.Join(legacy, name), filepath.Join(dir, name)
		fi, err := os.Stat(src)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
		case fi.IsDir():
			err = copyTree(src, dst)
		default:
			err = copyFile(src, dst)
		}
		if err != nil {
			return nil, fmt.Errorf("迁移旧数据目录 %s 到 %s 失败，旧数据未改动：%w", legacy, dir, err)
		}
	}
	if err := os.RemoveAll(legacy); err != nil {
		return fmt.Errorf("旧数据已迁移到 %s，但旧目录 %s 未能完全删除：%w", dir, legacy, err), nil
	}
	return nil, nil
}

// copyTree copies the regular files in src, and its folders, into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case !d.Type().IsRegular():
			return nil
		}
		return copyFile(path, target)
	})
}

// copyFile copies src to dst through a temporary file, so dst is either whole or absent.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
