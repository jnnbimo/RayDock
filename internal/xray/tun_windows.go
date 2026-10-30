package xray

import (
	"context"
	"fmt"
	"net"
	"time"
)

// WaitTUNReleased waits for a retired adapter's removal before a new core reuses its name. Process exit
// closes the Wintun handles, but Windows removes the adapter asynchronously; opening an adapter still
// being deleted can fail with ERROR_NOT_FOUND while starting its session or looking up its interface.
// The old, active core stays running while this waits. A foreign adapter with the same name is not taken.
func WaitTUNReleased(ctx context.Context, name string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ifs, err := net.Interfaces()
		if err != nil {
			return fmt.Errorf("读取虚拟网卡状态失败：%w", err)
		}
		present := false
		for _, iface := range ifs {
			if iface.Name == name {
				present = true
				break
			}
		}
		if !present {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("虚拟网卡 %s 尚未释放，或正被其他程序使用", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
