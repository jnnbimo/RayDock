package app

import (
	"testing"
	"time"
)

// Issuing a core operation never waits, however many are queued before it.
func TestOperationQueueNeverBlocks(t *testing.T) {
	ops := newOperationQueue()
	defer close(ops)
	gate, entered := make(chan struct{}), make(chan struct{})
	queueOperation(ops, func() { close(entered); <-gate })
	<-entered
	issued := make(chan struct{})
	go func() {
		for range 200 {
			queueOperation(ops, func() {})
		}
		close(issued)
	}()
	select {
	case <-issued:
	case <-time.After(time.Second):
		t.Fatal("issuing waited for the operations before")
	}
	close(gate)
	waitOperation(t, queueOperation(ops, func() {}))
}
