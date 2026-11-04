package app_test

import (
	"testing"
	"time"

	"raydock/internal/app/apptest"
)

// Queued output reaches the log without being flushed by hand, and the flushing ends with the app, which
// Shutdown waits for.
func TestStartLogs(t *testing.T) {
	h := apptest.New(t)
	a := h.A
	a.StartLogs()
	a.QueueLog("[RayDock] 第一行")
	h.Wait(t, func() bool { return len(a.Logs) == 1 && a.Logs[0].Text == "[RayDock] 第一行" })

	done := make(chan struct{})
	go func() {
		a.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown waits for the log flushing forever")
	}
}
