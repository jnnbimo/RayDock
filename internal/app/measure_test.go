package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Latency tests never run more than their limit at once, and start spaced out.
func TestForEachLimitsAndSpaces(t *testing.T) {
	var running, most atomic.Int32
	var mu sync.Mutex
	var starts []time.Time
	items := make([]int, 12)
	forEach(context.Background(), items, 3, func(int) {
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
	})
	if len(starts) != len(items) || most.Load() > 3 {
		t.Fatalf("ran %d, at most %d at once", len(starts), most.Load())
	}
	// Ticks may bunch after a slow consumer, but the whole run cannot be faster than its spacing.
	if span := starts[len(starts)-1].Sub(starts[0]); span < time.Duration(len(items)-2)*testSpacing {
		t.Fatalf("12 starts within %v", span)
	}
}

// An ended context stops starting tests and returns once those running finish.
func TestForEachStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int32
	done := make(chan struct{})
	go func() {
		forEach(ctx, make([]int, 100), 2, func(int) {
			if ran.Add(1) == 2 {
				cancel()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forEach did not return after cancel")
	}
	if n := ran.Load(); n >= 100 {
		t.Fatalf("kept starting after cancel: %d", n)
	}
}
