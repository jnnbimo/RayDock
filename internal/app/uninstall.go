package app

import (
	"fmt"
	"path/filepath"

	"raydock/internal/store"
	"raydock/internal/sysproxy"
)

// RestoreProxyBackup is the uninstall recovery path after graceful shutdown, including a backup left by
// an earlier crash. It neither starts a core nor takes over a proxy changed by another application.
func RestoreProxyBackup(dataDir string, proxy ProxySystem) error {
	path := filepath.Join(dataDir, "state.json")
	st, err := store.Load(path)
	if err != nil {
		return fmt.Errorf("无法读取代理恢复信息：%w", err)
	}
	if st.ProxyBackup == nil {
		return nil
	}
	_, err = restoreOwnedProxy(proxy, st.ProxyBackup, func(b *store.ProxyBackup) error {
		st.ProxyBackup = b
		return store.Save(path, st)
	})
	return err
}

// ownsProxy matches each prefix of Apply's Enable, Server, Override write order.
// While a write is pending its observed baseline supersedes all older addresses.
func ownsProxy(b *store.ProxyBackup, cur sysproxy.Settings) bool {
	if t := b.Transition; t != nil {
		s := t.From
		if s == cur {
			return true
		}
		s.Enabled = t.To.Enabled
		if s == cur {
			return true
		}
		s.Server = t.To.Server
		return s == cur || t.To == cur
	}
	if b.Owned != nil {
		return *b.Owned == cur
	}
	return cur.Enabled && b.Owns(cur.Server) // legacy journal, which only recorded addresses
}

func restoreOwnedProxy(proxy ProxySystem, b *store.ProxyBackup, save func(*store.ProxyBackup) error) (bool, error) {
	cur, err := proxy.Current()
	if err != nil {
		return false, fmt.Errorf("读取系统代理失败：%w", err)
	}
	owned := ownsProxy(b, cur)
	if owned {
		next := *b
		next.Transition = &store.ProxyTransition{From: cur, To: b.Previous}
		if err := save(&next); err != nil {
			return false, fmt.Errorf("保存代理恢复信息失败：%w", err)
		}
		if err := proxy.Apply(b.Previous); err != nil {
			return false, fmt.Errorf("恢复系统代理失败：%w", err)
		}
	}
	if err := save(nil); err != nil {
		return owned, fmt.Errorf("保存代理恢复结果失败：%w", err)
	}
	return owned, nil
}
