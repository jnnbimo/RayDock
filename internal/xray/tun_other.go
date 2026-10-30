//go:build !windows

package xray

import "context"

// WaitTUNReleased needs no adapter handover outside Windows (Overlap).
func WaitTUNReleased(ctx context.Context, _ string) error { return ctx.Err() }
