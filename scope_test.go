package nodra

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopeShutdownOrderAndFailures(t *testing.T) {
	scope := NewScope()
	var order []string
	failure := errors.New("flush failed")
	requireNoError(t, scope.Defer(func(context.Context) error { order = append(order, "store"); return nil }))
	requireNoError(t, scope.Defer(func(context.Context) error { order = append(order, "persistence"); return failure }))
	requireNoError(t, scope.Defer(func(context.Context) error { order = append(order, "action"); return nil }))
	requireNoError(t, scope.Observe(func() {
		if scope.Context().Err() == nil {
			panic("scope not canceled")
		}
		order = append(order, "unsubscribe")
	}))
	if err := scope.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("close: %v", err)
	}
	if !slices.Equal(order, []string{"unsubscribe", "action", "persistence", "store"}) {
		t.Fatalf("order: %v", order)
	}
	if err := scope.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatal("idempotent close lost error")
	}
}

func TestScopeConcurrentRegistrationAndClose(t *testing.T) {
	scope := NewScope()
	var stopped, drained atomic.Int64
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			_ = scope.Observe(func() { stopped.Add(1) })
			_ = scope.Defer(func(context.Context) error { drained.Add(1); return nil })
		})
	}
	wg.Go(func() { requireNoError(t, scope.Close(context.Background())) })
	wg.Wait()
	requireNoError(t, scope.Close(context.Background()))
	if stopped.Load() != 50 || drained.Load() != 50 {
		t.Fatalf("leaked registrations: %d %d", stopped.Load(), drained.Load())
	}
}

func TestScopeWaitCancellationAndPanicCleanup(t *testing.T) {
	scope := NewScope()
	entered, release := make(chan struct{}), make(chan struct{})
	requireNoError(t, scope.Defer(func(context.Context) error { panic("cleanup") }))
	requireNoError(t, scope.Defer(func(context.Context) error { close(entered); <-release; return nil }))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := scope.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	<-entered
	close(release)
	var panicErr *PanicError
	if err := scope.Close(context.Background()); !errors.As(err, &panicErr) {
		t.Fatalf("cleanup panic: %v", err)
	}
}
