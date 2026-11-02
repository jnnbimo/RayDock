//go:build !windows

package system

import (
	"log"
	"os"
)

// PrivateDir limits dir to its owner.
func PrivateDir(dir string) error { return os.Chmod(dir, 0o700) }

// ShowError reports an error that stops RayDock before it has a window to show it in.
func ShowError(title, text string) { log.Print(title + "：" + text) }
