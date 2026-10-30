package app

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

// coreDo queues op after the core operations queued before; the channel closes once it ran.
func (a *App) coreDo(op func()) <-chan struct{} { return queueOperation(a.coreOps, op) }
