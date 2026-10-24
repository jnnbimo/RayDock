//go:build !windows

// Package system wraps the OS integration RayDock needs: elevation, start at
// logon and the DNS cache.
package system

import (
	"errors"
	"os"
	"time"
)

var ErrUACCancelled = errors.New("已取消管理员授权")

func IsElevated() bool { return os.Geteuid() == 0 }

func RelaunchElevated([]string) error {
	return errors.New("当前平台请以 root 身份运行 RayDock 后再开启 TUN 模式")
}

func WaitProcessExit(int, time.Duration) {}

func FlushDNS() {}

type StartMode int

const (
	StartOff StartMode = iota
	StartUser
	StartAdmin
)

const AutostartArg = "--autostart"

func AutostartSupported() bool { return false }

// TaskHelper reports that args ask for no change of a logon task, which only Windows has.
func TaskHelper([]string) (int, bool) { return 0, false }

func Autostart(string) StartMode { return StartOff }

func SetAutostart(StartMode, string) error { return errors.New("当前平台暂不支持开机自启") }
