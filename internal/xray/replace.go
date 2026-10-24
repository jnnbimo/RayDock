package xray

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// commitFiles replaces one verified set of *.new files. Old files are moved aside
// first; a failed move/install rolls the entire set back. Rollback failures retain
// the backup directory and are returned with the original error, never hidden.
// Files in use may be replaced too: Windows lets a running executable or loaded
// library be moved, not deleted, so its backup directory stays until a later
// commit finds it unused and removes it.
func commitFiles(staged []string) error {
	if len(staged) == 0 {
		return nil
	}
	dir := filepath.Dir(staged[0])
	old, _ := filepath.Glob(filepath.Join(dir, "rollback-*"))
	for _, d := range old {
		_ = os.RemoveAll(d) // what a core still runs from stays for the next time
	}
	backupDir, err := os.MkdirTemp(dir, "rollback-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(backupDir)
		}
	}()
	type replacement struct {
		dst, backup string
		installed   bool
	}
	files := make([]replacement, len(staged))
	rollback := func(cause error) error {
		errs := []error{fmt.Errorf("替换文件失败：%w", cause)}
		for i := len(files) - 1; i >= 0; i-- {
			f := files[i]
			if f.installed {
				if err := os.Remove(f.dst); err != nil {
					errs = append(errs, fmt.Errorf("移除新文件 %s 失败：%w", f.dst, err))
					cleanup = false
				}
			}
			if f.backup != "" {
				if err := os.Rename(f.backup, f.dst); err != nil {
					errs = append(errs, fmt.Errorf("恢复 %s 失败（备份保留在 %s）：%w", f.dst, backupDir, err))
					cleanup = false
				}
			}
		}
		return errors.Join(errs...)
	}
	for i, p := range staged {
		dst := strings.TrimSuffix(p, ".new")
		files[i].dst = dst
		fi, err := os.Lstat(dst)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return rollback(err)
		}
		if !fi.Mode().IsRegular() {
			return rollback(fmt.Errorf("%s 不是普通文件", dst))
		}
		backup := filepath.Join(backupDir, filepath.Base(dst))
		if err := os.Rename(dst, backup); err != nil {
			return rollback(err)
		}
		files[i].backup = backup
	}
	for i, p := range staged {
		if err := os.Rename(p, files[i].dst); err != nil {
			return rollback(err)
		}
		files[i].installed = true
	}
	return nil
}
