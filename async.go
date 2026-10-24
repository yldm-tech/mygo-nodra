package nodra

import (
	"context"
	"sync"
	"time"
)

// AsyncOptions configures write-behind persistence. Debounce is a trailing-edge
// delay for bursts of updates; zero starts immediately. Flush bypasses the delay.
type AsyncOptions struct {
	Debounce time.Duration
}

// AsyncPersistence owns one worker and a bounded error stream. It coalesces
// updates without blocking writer goroutines on encoding or storage. Failed
// writes remain pending; the next update or Flush retries them without spinning.
// Values must be immutable or the store must use NewWithClone.
type AsyncPersistence[T any] struct {
	p           *Persistence[T]
	ctx         context.Context
	cancel      context.CancelFunc
	unsubscribe func()
	options     AsyncOptions
	wake        chan struct{}
	done        chan struct{}
	errors      chan error
	mu          sync.Mutex
	changed     chan struct{}
	requested   uint64
	saved       uint64
	hasSaved    bool
	completed   uint64
	lastErr     error
	force       bool
	stopped     bool
	terminalErr error
	closing     bool
	closeDone   chan struct{}
	closeErr    error
	state       *Store[PersistenceState]
	revision    uint64
	saving      bool
}

// AutoSaveAsync subscribes and schedules the current state once, then all
// subsequent versions. Use Close before closing the store to drain writes.
// Parent cancellation and store closure stop the worker without a guaranteed
// drain. Storage.Save must respect context cancellation to bound shutdown time.
func (p *Persistence[T]) AutoSaveAsync(ctx context.Context, options AsyncOptions) (*AsyncPersistence[T], error) {
	if p == nil || p.store == nil || p.storage == nil || p.codec == nil || options.Debounce < 0 {
		return nil, ErrInvalidPersistence
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-p.store.done:
		return nil, ErrClosed
	default:
	}
	workerCtx, cancel := context.WithCancel(ctx)
	a := &AsyncPersistence[T]{
		p: p, ctx: workerCtx, cancel: cancel, options: options,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		errors: make(chan error, 8), changed: make(chan struct{}), closeDone: make(chan struct{}),
		state: New(PersistenceState{Pending: true}),
	}
	a.unsubscribe = p.store.Subscribe(func(snapshot Snapshot[T]) { a.request(snapshot.Version) })
	a.request(p.store.Version())
	go a.run()
	return a, nil
}

// Errors reports failed write attempts. The bounded stream may drop errors if
// unread; Flush and Close return their write error directly. It closes on exit.
func (a *AsyncPersistence[T]) Errors() <-chan error { return a.errors }

func (a *AsyncPersistence[T]) pendingLocked() bool {
	return !a.hasSaved || a.saved < a.requested
}

func (a *AsyncPersistence[T]) signalLocked() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *AsyncPersistence[T]) changedLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

func (a *AsyncPersistence[T]) request(version uint64) {
	a.mu.Lock()
	if a.stopped || a.closing {
		a.mu.Unlock()
		return
	}
	a.requested = max(a.requested, version)
	if a.pendingLocked() {
		a.signalLocked()
	}
	a.revision++
	a.mu.Unlock()
	a.publishStatus()
}

// Flush waits until the version visible when called has been saved, bypassing
// debounce and retrying a previous failure. Canceling this wait does not cancel
// the shared worker. Later updates may be included in the same write.
func (a *AsyncPersistence[T]) Flush(ctx context.Context) error {
	return a.flush(ctx, false)
}

func (a *AsyncPersistence[T]) flush(ctx context.Context, allowClosing bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target := a.p.store.Version()
	a.mu.Lock()
	if a.closing && !allowClosing {
		a.mu.Unlock()
		return ErrClosed
	}
	if a.stopped {
		err := a.terminalErr
		a.mu.Unlock()
		return err
	}
	if a.hasSaved && a.saved >= target {
		a.mu.Unlock()
		return nil
	}
	started := a.completed
	a.requested = max(a.requested, target)
	a.force = true
	a.signalLocked()
	a.revision++
	a.mu.Unlock()
	a.publishStatus()
	a.mu.Lock()
	for {
		// An overlapping Close may have stopped the worker after satisfying
		// this barrier. Report the durable write, not a spurious cancellation.
		if a.hasSaved && a.saved >= target {
			a.mu.Unlock()
			return nil
		}
		if a.stopped {
			err := a.terminalErr
			a.mu.Unlock()
			return err
		}
		if a.completed > started && a.lastErr != nil {
			err := a.lastErr
			a.mu.Unlock()
			return err
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		a.mu.Lock()
	}
}

// Close stops observing updates, flushes the final observed state, then stops
// the worker even if the flush fails. It is idempotent; concurrent callers wait
// on the same result with their own context. Do not mutate the store concurrently
// with Close if those changes must be included. Retry failed shutdown writes
// with Persistence.Save; a closed async writer cannot be restarted.
func (a *AsyncPersistence[T]) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.closeDone:
			a.mu.Lock()
			err := a.closeErr
			a.mu.Unlock()
			return err
		}
	}
	a.closing = true
	a.mu.Unlock()
	a.unsubscribe()
	err := a.flush(ctx, true)
	a.cancel()
	select {
	case <-a.done:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	a.mu.Lock()
	a.closeErr = err
	close(a.closeDone)
	a.mu.Unlock()
	return err
}

func (a *AsyncPersistence[T]) run() {
	terminal := ErrClosed
	defer func() {
		a.unsubscribe()
		a.cancel()
		a.mu.Lock()
		a.stopped, a.terminalErr = true, terminal
		a.saving = false
		a.revision++
		a.changedLocked()
		a.mu.Unlock()
		a.publishStatus()
		_ = taskBoundary(func() error { return a.state.Close() })
		close(a.errors)
		close(a.done)
	}()
	for {
		select {
		case <-a.ctx.Done():
			terminal = a.ctx.Err()
			return
		case <-a.p.store.done:
			return
		case <-a.wake:
		}
		if !a.waitDebounce() {
			if a.ctx.Err() != nil {
				terminal = a.ctx.Err()
			}
			return
		}
		for {
			a.mu.Lock()
			pending := a.pendingLocked()
			a.force = false
			if pending {
				a.saving = true
				a.revision++
			}
			a.mu.Unlock()
			if !pending {
				break
			}
			a.publishStatus()
			version, err := a.p.save(a.ctx)
			a.mu.Lock()
			a.saving = false
			a.revision++
			a.completed++
			a.lastErr = err
			if err == nil {
				a.hasSaved, a.saved = true, max(a.saved, version)
			}
			a.changedLocked()
			a.mu.Unlock()
			a.publishStatus()
			if err != nil {
				select {
				case a.errors <- err:
				default:
				}
				break
			}
		}
	}
}

func (a *AsyncPersistence[T]) waitDebounce() bool {
	if a.options.Debounce == 0 {
		return true
	}
	timer := time.NewTimer(a.options.Debounce)
	defer timer.Stop()
	for {
		a.mu.Lock()
		force := a.force
		a.mu.Unlock()
		if force {
			return true
		}
		select {
		case <-a.ctx.Done():
			return false
		case <-a.p.store.done:
			return false
		case <-timer.C:
			return true
		case <-a.wake:
			timer.Reset(a.options.Debounce)
		}
	}
}
