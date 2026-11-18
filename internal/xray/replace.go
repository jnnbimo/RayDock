package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const transactionFile = "transaction.json"

type replacement struct {
	Name   string `json:"name"`
	HadOld bool   `json:"hadOld"`
}

// RecoveryPending excludes legacy/manual backup directories with no journal.
func RecoveryPending(dir string) bool {
	pending, err := pendingTransactions(dir)
	return err != nil || len(pending) > 0
}

func pendingTransactions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "rollback-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, err := os.Lstat(filepath.Join(path, transactionFile)); err == nil {
			pending = append(pending, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return pending, nil
}

// RecoverFiles rolls back a transaction interrupted before the entire generation was committed.
// Call before starting or inspecting any core files, with exclusive ownership of dir.
func RecoverFiles(dir string) error {
	pending, err := pendingTransactions(dir)
	if err != nil {
		return err
	}
	if len(pending) > 1 {
		return fmt.Errorf("存在多个未完成的替换事务，请检查备份：%v", pending)
	}
	for _, path := range pending {
		data, err := os.ReadFile(filepath.Join(path, transactionFile))
		if err != nil {
			return err
		}
		var files []replacement
		if err := json.Unmarshal(data, &files); err != nil {
			return fmt.Errorf("读取恢复记录 %s：%w", path, err)
		}
		seen := map[string]bool{}
		if len(files) == 0 {
			return fmt.Errorf("恢复记录 %s 未包含文件", path)
		}
		for _, f := range files {
			if f.Name == "" || f.Name == "." || f.Name == ".." || f.Name == transactionFile || strings.ContainsAny(f.Name, `/\:`) || seen[f.Name] {
				return fmt.Errorf("恢复记录 %s 包含无效文件名称", path)
			}
			seen[f.Name] = true
		}
		if err := rollbackFiles(path, files); err != nil {
			return err
		}
	}
	return nil
}

func rollbackFiles(backupDir string, files []replacement) error {
	var errs []error
	dir := filepath.Dir(backupDir)
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		dst, backup := filepath.Join(dir, f.Name), filepath.Join(backupDir, f.Name)
		if f.HadOld {
			if _, err := os.Lstat(backup); errors.Is(err, fs.ErrNotExist) {
				if _, err := os.Lstat(dst); err != nil {
					errs = append(errs, err)
				}
				continue // the old file was not moved, or was already restored
			} else if err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		if f.HadOld {
			if err := os.Rename(backup, dst); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("恢复文件失败（备份保留在 %s）：%w", backupDir, err)
	}
	return os.RemoveAll(backupDir)
}

// commitFiles installs a staged *.new set and records enough information to undo an interrupted commit.
// An unsuccessful rollback leaves a journal for recovery. Only retired backups may be swept.
func commitFiles(staged []string) error {
	if len(staged) == 0 {
		return nil
	}
	dir := filepath.Dir(staged[0])
	old, _ := filepath.Glob(filepath.Join(dir, "retired-*"))
	for _, d := range old {
		_ = os.RemoveAll(d)
	}
	files := make([]replacement, len(staged))
	for i, p := range staged {
		dst := strings.TrimSuffix(p, ".new")
		files[i].Name = filepath.Base(dst)
		fi, err := os.Lstat(dst)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s 不是普通文件", dst)
		}
		files[i].HadOld = true
	}
	backupDir, err := os.MkdirTemp(dir, "rollback-")
	if err != nil {
		return err
	}
	if err := writeJournal(backupDir, files); err != nil {
		_ = os.RemoveAll(backupDir)
		return err
	}
	rollback := func(cause error) error {
		return errors.Join(fmt.Errorf("替换文件失败：%w", cause), rollbackFiles(backupDir, files))
	}
	for _, f := range files {
		if f.HadOld {
			if err := os.Rename(filepath.Join(dir, f.Name), filepath.Join(backupDir, f.Name)); err != nil {
				return rollback(err)
			}
		}
	}
	for i, p := range staged {
		if err := os.Rename(p, filepath.Join(dir, files[i].Name)); err != nil {
			return rollback(err)
		}
	}
	retired := filepath.Join(dir, "retired-"+strings.TrimPrefix(filepath.Base(backupDir), "rollback-"))
	if err := os.Rename(backupDir, retired); err != nil {
		return rollback(err)
	}
	_ = os.RemoveAll(retired) // Windows may still be running the previous executable
	return nil
}

func writeJournal(dir string, files []replacement) error {
	f, err := os.Create(filepath.Join(dir, "transaction.tmp"))
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(files)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, transactionFile))
}
