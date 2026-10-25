package app

import "sync"

// Serialize core operations in UI issue order without blocking the caller.
// Operations check their own cancellation.
func newOperationQueue() chan func() {
	in, run := make(chan func()), make(chan func())
	go func() {
		var pending []func()
		for src := in; src != nil || len(pending) > 0; {
			var out chan func()
			var next func()
			if len(pending) > 0 {
				out, next = run, pending[0]
			}
			select {
			case op, ok := <-src:
				if !ok {
					src = nil
					continue
				}
				pending = append(pending, op)
			case out <- next:
				pending[0] = nil
				pending = pending[1:]
			}
		}
		close(run)
	}()
	go func() {
		for op := range run {
			op()
		}
	}()
	return in
}

func queueOperation(ops chan func(), op func()) <-chan struct{} {
	done := make(chan struct{})
	ops <- func() { defer close(done); op() }
	return done
}

// latest coalesces waiting targets while preserving intervening operation barriers.
type latest[T any] struct {
	mu     sync.Mutex
	want   *T
	wantOp int             // the operation that carries want out
	queued int             // the operation queued since the last barrier and not run yet, 0 if none
	done   <-chan struct{} // closed once queued ran
	ops    int
}

// set replaces a waiting target after the last barrier; merge preserves required transitions.
// The returned channel closes after execution. UI thread only.
func (l *latest[T]) set(t *T, merge func(old, t *T), queue func(op func()) <-chan struct{}, run func(*T)) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.want != nil && merge != nil {
		merge(l.want, t)
	}
	if l.queued == 0 {
		l.ops++
		id := l.ops
		l.queued = id
		l.done = queue(func() {
			if t := l.take(id); t != nil {
				run(t)
			}
		})
	}
	l.want, l.wantOp = t, l.queued
	return l.done
}

func (l *latest[T]) take(id int) *T {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.queued == id {
		l.queued = 0
	}
	if l.want == nil || l.wantOp != id {
		return nil
	}
	t := l.want
	l.want = nil
	return t
}

// barrier has the next target queue an operation of its own, after one queued now. UI only.
func (l *latest[T]) barrier() {
	l.mu.Lock()
	l.queued = 0
	l.mu.Unlock()
}

// coreDo inserts a barrier between earlier and later planned changes.
func (a *App) coreDo(op func()) <-chan struct{} {
	a.coreWant.barrier()
	return queueOperation(a.coreOps, op)
}
