//go:build !windows

package xray

import "os/exec"

func hideWindow(*exec.Cmd) {}

type processGuard struct{}

var newProcessGuard = func() *processGuard { return nil }

func (g *processGuard) add(int) {}
