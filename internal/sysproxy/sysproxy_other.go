//go:build !windows

package sysproxy

import "errors"

const Bypass = "localhost;127.*;10.*;192.168.*;<local>"

type Settings struct {
	Enabled  bool   `json:"enabled"`
	Server   string `json:"server,omitempty"`
	Override string `json:"override,omitempty"`
}

var errUnsupported = errors.New("当前平台暂不支持设置系统代理")

func Supported() bool            { return false }
func Current() (Settings, error) { return Settings{}, errUnsupported }
func Apply(Settings) error       { return errUnsupported }
