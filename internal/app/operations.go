package app

import "sync"

// Each core has one queue: mutexes inside Core protect execution, not the order in which UI actions
// were issued. The queue takes any number of operations, so issuing one never waits for those before it;
// cancellation is checked by the operations queued.
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

// latest hands the newest of a series of targets to one queued operation, which brings what runs to
// it: targets set while it waits replace each other, so only the newest is carried out. Operations
// queued in between (barriers) keep their place: a target set after one is carried out after it.
type latest[T any] struct {
	mu     sync.Mutex
	want   *T
	wantOp int             // the operation that carries want out
	queued int             // the operation queued since the last barrier and not run yet, 0 if none
	done   <-chan struct{} // closed once queued ran
	ops    int
}

// set makes t the target, carried out by run among the operations queue queues: by the one waiting
// since the last barrier when there is one. A target not carried out yet is replaced; merge, when set,
// combines it into t. It returns a channel closed once t was carried out. UI only.
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

// take returns the target operation id carries out, nil when a later one replaced it.
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

// coreDo queues op among the connection's core operations: a barrier, which every change planned before
// is carried out before and every one planned after, after.
func (a *App) coreDo(op func()) <-chan struct{} {
	a.coreWant.barrier()
	return queueOperation(a.coreOps, op)
}
