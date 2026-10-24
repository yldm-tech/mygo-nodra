package nodra

import (
	"context"
	"errors"
	"sync"
)

// Scope owns a module's lifetime. Close cancels Context, stops observations, then runs deferred drains in reverse registration order. Register stores first, persistence second, and actions last so work finishes before persistence drains and stores close. Shared stores need not belong to a Scope.
type Scope struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	stops    []func()
	drains   []func(context.Context) error
	closing  bool
	shutdown context.Context
	done     chan struct{}
	err      error
}

func NewScope() *Scope {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scope{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (s *Scope) Context() context.Context { return s.ctx }
func (s *Scope) Done() <-chan struct{}    { return s.done }

// Observe owns an unsubscribe function. A registration racing with Close is stopped immediately instead of leaking. Already executing source callbacks are not awaited.
func (s *Scope) Observe(stop func()) error {
	if stop == nil {
		return nil
	}
	s.mu.Lock()
	if !s.closing {
		s.stops = append(s.stops, stop)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	stop()
	return ErrClosed
}

// Defer registers a context-aware drain. Late registrations run immediately with the original shutdown context and return ErrClosed plus any cleanup error. Drains must respect context cancellation and must not synchronously Close their own Scope.
func (s *Scope) Defer(cleanup func(context.Context) error) error {
	if cleanup == nil {
		return nil
	}
	s.mu.Lock()
	if !s.closing {
		s.drains = append(s.drains, cleanup)
		s.mu.Unlock()
		return nil
	}
	ctx := s.shutdown
	s.mu.Unlock()
	return errors.Join(ErrClosed, taskBoundary(func() error { return cleanup(ctx) }))
}

// Close starts shutdown once. Concurrent callers share its result but can stop waiting with their own context. The first caller supplies the drain context; resources receive that deadline even if the caller stops waiting. Persistence that must drain should use an independent worker context, not Scope.Context, which is canceled first.
func (s *Scope) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.closing {
		s.closing, s.shutdown = true, ctx
		stops, drains := s.stops, s.drains
		s.stops, s.drains = nil, nil
		s.cancel()
		go func() {
			var err error
			for n := len(stops) - 1; n >= 0; n-- {
				err = errors.Join(err, taskBoundary(func() error { stops[n](); return nil }))
			}
			for n := len(drains) - 1; n >= 0; n-- {
				err = errors.Join(err, taskBoundary(func() error { return drains[n](ctx) }))
			}
			s.mu.Lock()
			s.err = err
			close(s.done)
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return s.err
	default:
	}
	select {
	case <-s.done:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
