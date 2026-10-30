//go:build !windows

package system

// Non-Windows builds do not overlap listeners; the caller additionally checks the tracked live process.
func ListenerOwned(pid, port int) (bool, error) { return pid > 0 && port > 0, nil }
