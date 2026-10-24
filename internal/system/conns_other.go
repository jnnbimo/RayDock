//go:build !windows

package system

// Connections cannot count connections here: a core finishing its connections stops at its time limit.
func Connections(...int) int { return -1 }
