package nodra

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDebounceKeyedReplacementAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type input struct {
			Key   string
			Value int
		}
		store := New(0)
		var calls atomic.Int32
		action, err := NewAsyncActionWithOptions(store, "search", func(_ context.Context, value input, _ func(float64)) (int, error) {
			calls.Add(1)
			return value.Value, nil
		}, func(s *int, v int) error { *s += v; return nil }, ActionOptions[input]{Key: func(v input) string { return v.Key }, Debounce: 40 * time.Millisecond, MaxConcurrent: 1})
		requireNoError(t, err)
		defer func() { _ = action.Close(context.Background()) }()
		first, err := action.Run(context.Background(), input{"a", 1})
		requireNoError(t, err)
		second, err := action.Run(context.Background(), input{"a", 2})
		requireNoError(t, err)
		other, err := action.Run(context.Background(), input{"b", 4})
		requireNoError(t, err)
		canceled, err := action.Run(context.Background(), input{"c", 8})
		requireNoError(t, err)
		canceled.Cancel()
		if first.Status().Get().Phase != TaskQueued && first.Status().Get().Phase != TaskSuperseded {
			t.Fatal("debounce not queued")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if !errors.Is(first.Wait(ctx), ErrSuperseded) {
			t.Fatal("old input not superseded")
		}
		if !errors.Is(canceled.Wait(ctx), context.Canceled) {
			t.Fatal("canceled debounce ran")
		}
		requireNoError(t, second.Wait(ctx))
		requireNoError(t, other.Wait(ctx))
		if calls.Load() != 2 || store.Get() != 6 {
			t.Fatalf("calls=%d state=%d", calls.Load(), store.Get())
		}
	})
}

func TestDebounceDoesNotOccupySlotAndCloseDrainsTimers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := New(0)
		entered := make(chan int, 2)
		action, err := NewAsyncActionWithOptions(store, "debounce", func(_ context.Context, v int, _ func(float64)) (int, error) { entered <- v; return v, nil }, func(s *int, v int) error { *s = v; return nil }, ActionOptions[int]{Debounce: time.Hour, MaxConcurrent: 1, QueueLimit: 1})
		requireNoError(t, err)
		first, err := action.Run(context.Background(), 1)
		requireNoError(t, err)
		second, err := action.Run(context.Background(), 2)
		requireNoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		requireNoError(t, action.Close(ctx))
		if !errors.Is(first.Wait(ctx), ErrSuperseded) || !errors.Is(second.Wait(ctx), ErrClosed) {
			t.Fatal("close did not cancel timers")
		}
		select {
		case <-entered:
			t.Fatal("debounce work started")
		default:
		}
	})
}

func TestDebounceIndependentKeysDoNotResetEachOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := New(0)
		action, err := NewAsyncActionWithOptions(store, "keys", func(_ context.Context, v string, _ func(float64)) (string, error) { return v, nil }, func(s *int, _ string) error { *s++; return nil }, ActionOptions[string]{Key: func(v string) string { return v }, Debounce: 30 * time.Millisecond, MaxConcurrent: 1})
		requireNoError(t, err)
		defer func() { _ = action.Close(context.Background()) }()
		first, err := action.Run(context.Background(), "a")
		requireNoError(t, err)
		time.Sleep(10 * time.Millisecond)
		second, err := action.Run(context.Background(), "b")
		requireNoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		requireNoError(t, first.Wait(ctx))
		if second.Status().Get().Phase != TaskQueued {
			t.Fatal("different key changed the first deadline")
		}
		requireNoError(t, second.Wait(ctx))
		if store.Get() != 2 {
			t.Fatal("independent key canceled")
		}
		_, err = NewAsyncActionWithOptions(store, "invalid", func(context.Context, int, func(float64)) (int, error) { return 0, nil }, func(*int, int) error { return nil }, ActionOptions[int]{Policy: QueueAll, Debounce: time.Second})
		if !errors.Is(err, ErrInvalidAction) {
			t.Fatal("QueueAll accepted contradictory debounce")
		}
	})
}
