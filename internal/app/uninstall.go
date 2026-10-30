package app

import (
	"fmt"
	"path/filepath"

	"raydock/internal/store"
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
	if _, err = restoreOwnedProxy(proxy, st.ProxyBackup); err != nil {
		return err
	}
	st.ProxyBackup = nil
	if err = store.Save(path, st); err != nil {
		return fmt.Errorf("无法保存代理恢复结果：%w", err)
	}
	return nil
}

func restoreOwnedProxy(proxy ProxySystem, b *store.ProxyBackup) (bool, error) {
	cur, err := proxy.Current()
	if err != nil {
		return false, fmt.Errorf("读取系统代理失败：%w", err)
	}
	if cur.Enabled && b.Owns(cur.Server) {
		if err := proxy.Apply(b.Previous); err != nil {
			return false, fmt.Errorf("恢复系统代理失败：%w", err)
		}
		return true, nil
	}
	return false, nil
}
