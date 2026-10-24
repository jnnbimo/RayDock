//go:build !windows

package localnet

import "syscall"

// Find returns nil: elsewhere the routes decide, as RayDock runs no TUN there.
func Find() (*Interface, error) { return nil, nil }

func bind(int) func(network, address string, c syscall.RawConn) error { return nil }
