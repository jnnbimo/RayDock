// Package sysproxy sets the Windows (WinINet) system proxy, which browsers and most apps follow.
package sysproxy

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const keyPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// Bypass lists addresses that skip the proxy.
const Bypass = "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;" +
	"172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"

type Settings struct {
	Enabled  bool   `json:"enabled"`
	Server   string `json:"server,omitempty"`
	Override string `json:"override,omitempty"`
}

func Supported() bool { return true }

func Current() (Settings, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return Settings{}, err
	}
	defer k.Close()
	var s Settings
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		s.Enabled = v != 0
	}
	s.Server, _, _ = k.GetStringValue("ProxyServer")
	s.Override, _, _ = k.GetStringValue("ProxyOverride")
	return s, nil
}

// Apply writes the settings and tells the system to apply them now.
func Apply(s Settings) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	enable := uint32(0)
	if s.Enabled {
		enable = 1
	}
	if err := k.SetDWordValue("ProxyEnable", enable); err != nil {
		return err
	}
	if err := setOrDelete(k, "ProxyServer", s.Server); err != nil {
		return err
	}
	if err := setOrDelete(k, "ProxyOverride", s.Override); err != nil {
		return err
	}
	return notify()
}

func setOrDelete(k registry.Key, name, v string) error {
	if v == "" {
		if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(name, v)
}

var (
	wininet           = windows.NewLazySystemDLL("wininet.dll")
	internetSetOption = wininet.NewProc("InternetSetOptionW")
)

const (
	optionRefresh         = 37
	optionSettingsChanged = 39
)

// notify makes running programs reload the proxy settings.
func notify() error {
	if err := internetSetOption.Find(); err != nil {
		return err
	}
	for _, opt := range []uintptr{optionSettingsChanged, optionRefresh} {
		if r, _, err := internetSetOption.Call(0, opt, 0, 0); r == 0 {
			return err
		}
	}
	return nil
}
