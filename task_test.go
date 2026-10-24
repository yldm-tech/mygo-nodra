package nodra

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func waitTask(t *testing.T, task *Task) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return task.Wait(ctx)
}

func TestAsyncActionLatestResultAndProgress(t *testing.T) {
	store := New(0)
	entered, release := make(chan struct{}), make(chan struct{})
	action, err := NewAsyncAction(store, "search", func(ctx context.Context, input int, report func(float64)) (int, error) {
		if input == 1 {
			close(entered)
			<-release
		} // Deliberately ignore cancellation to test stale result protection.
		report(.6)
		report(.2)
		report(math.NaN())
		report(2)
		return input, nil
	}, func(s *int, result int) error { *s = result; return nil })
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	first, err := action.Run(context.Background(), 1)
	requireNoError(t, err)
	<-entered
	second, err := action.Run(context.Background(), 2)
	requireNoError(t, err)
	requireNoError(t, waitTask(t, second))
	close(release)
	if err := waitTask(t, first); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("stale task: %v", err)
	}
	if got := store.Get(); got != 2 {
		t.Fatalf("stale result overwrote state: %d", got)
	}
	status := action.Status().Get()
	if status.ID != second.ID || status.Running || status.Progress != 1 || status.Error != "" {
		t.Fatalf("status: %+v", status)
	}
}

func TestAsyncActionFailuresAndPanics(t *testing.T) {
	for _, mode := range []string{"work error", "work panic", "apply error", "apply panic", "hook panic"} {
		t.Run(mode, func(t *testing.T) {
			store := New(0)
			failure := errors.New("rejected")
			action, err := NewAsyncAction(store, "load", func(context.Context, int, func(float64)) (int, error) {
				if mode == "work panic" {
					panic("work")
				}
				if mode == "work error" {
					return 0, failure
				}
				return 7, nil
			}, func(s *int, result int) error {
				*s = result
				if mode == "apply panic" {
					panic("apply")
				}
				if mode == "apply error" {
					return failure
				}
				return nil
			})
			requireNoError(t, err)
			defer func() { _ = action.Close(context.Background()) }()
			if mode == "hook panic" {
				stop := store.SubscribeActionHooks(ActionHooks{Before: func(ActionEvent) { panic("hook") }})
				defer stop()
			}
			task, err := action.Run(context.Background(), 0)
			requireNoError(t, err)
			if err := waitTask(t, task); err == nil {
				t.Fatal("expected failure")
			}
			if store.Get() != 0 || store.Version() != 0 {
				t.Fatal("failed action committed")
			}
			if status := action.Status().Get(); status.Running || status.Error == "" {
				t.Fatalf("status: %+v", status)
			}
			requireNoError(t, store.Set(9))
		})
	}
}

func TestAsyncActionCancellationAndLifecycle(t *testing.T) {
	for _, mode := range []string{"task", "parent", "source", "close"} {
		t.Run(mode, func(t *testing.T) {
			store := New(0)
			entered := make(chan struct{})
			action, err := NewAsyncAction(store, "wait", func(ctx context.Context, _ int, _ func(float64)) (int, error) {
				close(entered)
				<-ctx.Done()
				return 7, ctx.Err()
			}, func(s *int, v int) error { *s = v; return nil })
			requireNoError(t, err)
			defer func() { _ = action.Close(context.Background()) }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			task, err := action.Run(ctx, 0)
			requireNoError(t, err)
			<-entered
			switch mode {
			case "task":
				task.Cancel()
			case "parent":
				cancel()
			case "source":
				requireNoError(t, store.Close())
			case "close":
				requireNoError(t, action.Close(context.Background()))
			}
			if err := waitTask(t, task); err == nil {
				t.Fatal("canceled action succeeded")
			}
			if store.Get() != 0 {
				t.Fatal("canceled result committed")
			}
		})
	}
}

func TestAsyncActionDoesNotHoldLockWhileWorkingOrNotifying(t *testing.T) {
	store := New(0)
	entered, release := make(chan struct{}), make(chan struct{})
	action, err := NewAsyncAction(store, "load", func(ctx context.Context, input int, _ func(float64)) (int, error) {
		if input == 1 {
			close(entered)
			<-release
		}
		return input, nil
	}, func(s *int, v int) error { *s = v; return nil })
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	first, err := action.Run(context.Background(), 1)
	requireNoError(t, err)
	<-entered
	requireNoError(t, store.Set(3))
	var next *Task
	stop := store.Subscribe(func(s Snapshot[int]) {
		if s.Value == 1 {
			var runErr error
			next, runErr = action.Run(context.Background(), 2)
			requireNoError(t, runErr)
		}
	})
	defer stop()
	close(release)
	requireNoError(t, waitTask(t, first))
	if next == nil {
		t.Fatal("subscriber did not reenter")
	}
	requireNoError(t, waitTask(t, next))
	if store.Get() != 2 {
		t.Fatal("reentrant action did not commit")
	}
}

func TestAsyncActionCloseTimeoutAndConcurrentRuns(t *testing.T) {
	store := New(0)
	release := make(chan struct{})
	action, err := NewAsyncAction(store, "load", func(context.Context, int, func(float64)) (int, error) { <-release; return 1, nil }, func(s *int, v int) error { *s = v; return nil })
	requireNoError(t, err)
	var wg sync.WaitGroup
	for n := range 20 {
		wg.Go(func() { _, runErr := action.Run(context.Background(), n); requireNoError(t, runErr) })
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := action.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: %v", err)
	}
	close(release)
	requireNoError(t, action.Close(context.Background()))
	if _, err := action.Run(context.Background(), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("Run after Close: %v", err)
	}
	if store.Get() != 0 {
		t.Fatal("closed action committed")
	}
}

func TestAsyncStatusPanicStillFinishesTask(t *testing.T) {
	action, err := NewAsyncAction(New(0), "load", func(context.Context, int, func(float64)) (int, error) { return 1, nil }, func(s *int, v int) error { *s = v; return nil })
	requireNoError(t, err)
	stop := action.Status().Subscribe(func(Snapshot[AsyncState]) { panic("status observer") })
	task, err := action.Run(context.Background(), 0)
	requireNoError(t, err)
	if err := waitTask(t, task); err == nil {
		t.Fatal("observer panic not reported")
	}
	stop()
	requireNoError(t, action.Close(context.Background()))
}
