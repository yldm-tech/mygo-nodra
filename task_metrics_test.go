package nodra

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestTaskMetricsSeparateDebounceWorkAndCommitNotifications(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := New(0)
		store.SubscribeMutations(func(Mutation[int]) { time.Sleep(25 * time.Millisecond) })
		store.SubscribeActionHooks(ActionHooks{Before: func(ActionEvent) { time.Sleep(10 * time.Millisecond) }, After: func(ActionEvent) { time.Sleep(20 * time.Millisecond) }})
		var completion ActionEvent
		store.SubscribeActions(func(event ActionEvent) { completion = event })
		action, err := NewAsyncActionWithOptions(store, "timed", func(context.Context, int, func(float64)) (int, error) {
			time.Sleep(200 * time.Millisecond)
			return 1, nil
		}, func(s *int, v int) error { time.Sleep(50 * time.Millisecond); *s = v; return nil }, ActionOptions[int]{Debounce: 100 * time.Millisecond})
		requireNoError(t, err)
		defer func() { _ = action.Close(context.Background()) }()
		task, err := action.Run(context.Background(), 0)
		requireNoError(t, err)
		requireNoError(t, task.Wait(context.Background()))
		metrics := task.Status().Get().Metrics
		if metrics.Debounce != 100*time.Millisecond || metrics.Queue != 0 || metrics.Work != 200*time.Millisecond || metrics.Commit != 75*time.Millisecond || metrics.ActiveWorkers != 1 || metrics.QueueDepth != 0 {
			t.Fatalf("phases: %+v", metrics)
		}
		if completion.Metrics != metrics || !completion.Committed || completion.Version != 1 || completion.Duration != 385*time.Millisecond {
			t.Fatalf("completion: %+v, status: %+v", completion, metrics)
		}
	})
}

func TestTaskMetricsSupersededDuringDebounce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		action, err := NewAsyncActionWithOptions(New(0), "debounce", func(context.Context, int, func(float64)) (int, error) { return 1, nil }, func(*int, int) error { return nil }, ActionOptions[int]{Debounce: 100 * time.Millisecond})
		requireNoError(t, err)
		defer func() { _ = action.Close(context.Background()) }()
		first, err := action.Run(context.Background(), 1)
		requireNoError(t, err)
		synctest.Wait()
		time.Sleep(40 * time.Millisecond)
		second, err := action.Run(context.Background(), 2)
		requireNoError(t, err)
		if !errors.Is(first.Wait(context.Background()), ErrSuperseded) {
			t.Fatal("not superseded")
		}
		metrics := first.Status().Get().Metrics
		if metrics.Debounce != 40*time.Millisecond || metrics.Queue != 0 || metrics.Work != 0 || metrics.Commit != 0 {
			t.Fatalf("superseded debounce: %+v", metrics)
		}
		requireNoError(t, second.Wait(context.Background()))
	})
}

func TestTaskMetricsQueueCancellationAndFailedCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate, entered := make(chan struct{}), make(chan struct{}, 2)
		want := errors.New("apply failed")
		action, err := NewAsyncActionWithOptions(New(0), "queue", func(context.Context, int, func(float64)) (int, error) {
			entered <- struct{}{}
			<-gate
			return 1, nil
		}, func(*int, int) error { time.Sleep(10 * time.Millisecond); return want }, ActionOptions[int]{Policy: QueueAll})
		requireNoError(t, err)
		defer func() { _ = action.Close(context.Background()) }()
		first, err := action.Run(context.Background(), 1)
		requireNoError(t, err)
		<-entered
		second, err := action.Run(context.Background(), 2)
		requireNoError(t, err)
		if metrics := second.Status().Get().Metrics; metrics.QueueDepth != 1 || metrics.ActiveWorkers != 1 {
			t.Fatalf("acceptance counts: %+v", metrics)
		}
		synctest.Wait()
		time.Sleep(300 * time.Millisecond)
		second.Cancel()
		if !errors.Is(second.Wait(context.Background()), context.Canceled) {
			t.Fatal("queued cancellation")
		}
		metrics := second.Status().Get().Metrics
		if metrics.Queue != 300*time.Millisecond || metrics.Debounce != 0 || metrics.Work != 0 || metrics.Commit != 0 {
			t.Fatalf("canceled queue: %+v", metrics)
		}
		close(gate)
		if !errors.Is(first.Wait(context.Background()), want) {
			t.Fatal("failed commit")
		}
		if metrics := first.Status().Get().Metrics; metrics.Work != 300*time.Millisecond || metrics.Commit != 10*time.Millisecond {
			t.Fatalf("failed phases: %+v", metrics)
		}
	})
}
