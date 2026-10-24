package nodra

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyedActionsDoNotCancelOtherKeys(t *testing.T) {
	type input struct {
		Key   string
		Value int
	}
	store := NewWithClone(map[string]int{}, maps.Clone[map[string]int])
	entered, release := make(chan struct{}), make(chan struct{})
	action, err := NewAsyncActionWithOptions(store, "load item", func(_ context.Context, in input, _ func(float64)) (input, error) {
		if in.Value == 1 {
			close(entered)
			<-release
		}
		return in, nil
	}, func(s *map[string]int, in input) error { (*s)[in.Key] = in.Value; return nil }, ActionOptions[input]{Key: func(in input) string { return in.Key }, MaxConcurrent: 2})
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	first, err := action.Run(context.Background(), input{"a", 1})
	requireNoError(t, err)
	<-entered
	other, err := action.Run(context.Background(), input{"b", 2})
	requireNoError(t, err)
	requireNoError(t, waitTask(t, other))
	latest, err := action.Run(context.Background(), input{"a", 3})
	requireNoError(t, err)
	requireNoError(t, waitTask(t, latest))
	close(release)
	if err := waitTask(t, first); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("first: %v", err)
	}
	if !maps.Equal(store.Get(), map[string]int{"a": 3, "b": 2}) {
		t.Fatalf("state: %v", store.Get())
	}
	if first.Status().Get().Phase != TaskSuperseded || other.Status().Get().Phase != TaskSucceeded {
		t.Fatal("per-task status incorrect")
	}
}

func TestActionQueueBoundAndQueuedCancellation(t *testing.T) {
	store := New(0)
	entered, release := make(chan int, 4), make(chan struct{})
	var active, peak atomic.Int64
	action, err := NewAsyncActionWithOptions(store, "batch", func(_ context.Context, in int, _ func(float64)) (int, error) {
		current := active.Add(1)
		for {
			old := peak.Load()
			if old >= current || peak.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- in
		<-release
		active.Add(-1)
		return in, nil
	}, func(s *int, in int) error { *s += in; return nil }, ActionOptions[int]{Policy: QueueAll, MaxConcurrent: 2, QueueLimit: 1})
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	first, err := action.Run(context.Background(), 1)
	requireNoError(t, err)
	<-entered
	second, err := action.Run(context.Background(), 2)
	requireNoError(t, err)
	<-entered
	queued, err := action.Run(context.Background(), 3)
	requireNoError(t, err)
	if queued.Status().Get().Phase != TaskQueued {
		t.Fatal("waiting task not queued")
	}
	if _, err := action.Run(context.Background(), 4); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue bound: %v", err)
	}
	queued.Cancel()
	if err := waitTask(t, queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancel: %v", err)
	}
	if queued.Status().Get().Phase != TaskCanceled {
		t.Fatal("queued cancellation not terminal")
	}
	last, err := action.Run(context.Background(), 4)
	requireNoError(t, err)
	close(release)
	for _, task := range []*Task{first, second, last} {
		requireNoError(t, waitTask(t, task))
	}
	if peak.Load() != 2 || store.Get() != 7 {
		t.Fatalf("peak=%d total=%d", peak.Load(), store.Get())
	}
}

func TestSerialQueuePreservesEveryAcceptedTask(t *testing.T) {
	store := New(0)
	entered, release := make(chan struct{}), make(chan struct{})
	var order []int
	action, err := NewAsyncActionWithOptions(store, "queue", func(_ context.Context, in int, _ func(float64)) (int, error) {
		if in == 1 {
			close(entered)
			<-release
		}
		return in, nil
	}, func(s *int, in int) error { order = append(order, in); *s = in; return nil }, ActionOptions[int]{Policy: QueueAll})
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	first, err := action.Run(context.Background(), 1)
	requireNoError(t, err)
	<-entered
	tasks := []*Task{first}
	for _, in := range []int{2, 3, 4} {
		task, err := action.Run(context.Background(), in)
		requireNoError(t, err)
		tasks = append(tasks, task)
	}
	close(release)
	for _, task := range tasks {
		requireNoError(t, waitTask(t, task))
	}
	if !slices.Equal(order, []int{1, 2, 3, 4}) {
		t.Fatalf("commit order: %v", order)
	}
}

func TestActionTraceCorrelatesProgressCommitAndCompletion(t *testing.T) {
	store := New(0)
	var mu sync.Mutex
	var traces []ActionTrace
	var mutation Mutation[int]
	store.SubscribeActionTrace(func(event ActionTrace) { mu.Lock(); traces = append(traces, event); mu.Unlock() })
	store.SubscribeMutations(func(event Mutation[int]) { mutation = event })
	action, err := NewAsyncAction(store, "trace", func(_ context.Context, _ int, report func(float64)) (int, error) {
		report(.25)
		report(.75)
		return 8, nil
	}, func(s *int, v int) error { *s = v; return nil })
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	task, err := action.Run(context.Background(), 0)
	requireNoError(t, err)
	requireNoError(t, waitTask(t, task))
	mu.Lock()
	defer mu.Unlock()
	if len(traces) < 5 {
		t.Fatalf("trace too short: %+v", traces)
	}
	last := traces[len(traces)-1]
	if last.Phase != TaskSucceeded || !last.Committed || last.Version != 1 || last.ID != mutation.ActionID || last.RunID != task.ID {
		t.Fatalf("commit correlation: %+v mutation=%+v", last, mutation)
	}
	for n, event := range traces {
		if event.ID != last.ID || (n > 0 && event.Progress < traces[n-1].Progress) {
			t.Fatal("trace identity/progress changed")
		}
	}
}

func TestConcurrentProgressDoesNotRegressStatus(t *testing.T) {
	action, err := NewAsyncAction(New(0), "progress", func(_ context.Context, _ int, report func(float64)) (int, error) {
		var wg sync.WaitGroup
		for n := range 50 {
			wg.Go(func() { report(float64(n) / 50) })
		}
		wg.Wait()
		return 1, nil
	}, func(s *int, v int) error { *s = v; return nil })
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	var mu sync.Mutex
	var latestRevision uint64
	var progress float64
	var metrics TaskMetrics
	var regressed bool
	stop := action.Status().Subscribe(func(snapshot Snapshot[AsyncState]) {
		mu.Lock()
		defer mu.Unlock()
		if snapshot.Value.Revision > latestRevision {
			if snapshot.Value.Progress < progress || snapshot.Value.Metrics.Work < metrics.Work || snapshot.Value.Metrics.Queue < metrics.Queue || snapshot.Value.Metrics.Commit < metrics.Commit {
				regressed = true
			}
			progress = snapshot.Value.Progress
			metrics = snapshot.Value.Metrics
			latestRevision = snapshot.Value.Revision
		}
	})
	defer stop()
	task, err := action.Run(context.Background(), 0)
	requireNoError(t, err)
	requireNoError(t, waitTask(t, task))
	if regressed || action.Status().Get().Progress != 1 {
		t.Fatal("concurrent progress or timing metrics regressed")
	}
}

func TestCanceledWorkStillOccupiesConcurrencySlot(t *testing.T) {
	store := New(0)
	entered, release := make(chan int, 3), make(chan struct{})
	action, err := NewAsyncActionWithOptions(store, "bounded latest", func(_ context.Context, in int, _ func(float64)) (int, error) {
		entered <- in
		if in == 1 {
			<-release
		}
		return in, nil
	}, func(s *int, v int) error { *s = v; return nil }, ActionOptions[int]{MaxConcurrent: 1, QueueLimit: 1})
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	first, err := action.Run(context.Background(), 1)
	requireNoError(t, err)
	<-entered
	second, err := action.Run(context.Background(), 2)
	requireNoError(t, err)
	select {
	case n := <-entered:
		t.Fatalf("canceled work freed its slot prematurely: %d", n)
	default:
	}
	third, err := action.Run(context.Background(), 3)
	requireNoError(t, err)
	if err := waitTask(t, second); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("replaced queue entry: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := third.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("replacement bypassed occupied slot: %v", err)
	}
	close(release)
	if err := waitTask(t, first); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old work: %v", err)
	}
	requireNoError(t, waitTask(t, third))
	if n := <-entered; n != 3 || store.Get() != 3 {
		t.Fatal("wrong replacement ran")
	}
	if len(store.ActiveTasks()) != 0 {
		t.Fatal("terminal task retained by source")
	}
}

func TestProgressObserverPanicEndsTaskAndReleasesSourceEntry(t *testing.T) {
	store := New(0)
	entered, release := make(chan struct{}), make(chan struct{})
	action, err := NewAsyncAction(store, "report", func(_ context.Context, _ int, report func(float64)) (int, error) {
		close(entered)
		<-release
		report(.5)
		return 9, nil
	}, func(s *int, v int) error { *s = v; return nil })
	requireNoError(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	task, err := action.Run(context.Background(), 0)
	requireNoError(t, err)
	<-entered
	stop := task.Status().Subscribe(func(snapshot Snapshot[AsyncState]) {
		if snapshot.Value.Progress > 0 {
			panic("progress observer")
		}
	})
	defer stop()
	close(release)
	var panicErr *PanicError
	if err := waitTask(t, task); !errors.As(err, &panicErr) {
		t.Fatalf("progress observer: %v", err)
	}
	if store.Get() != 0 || len(store.ActiveTasks()) != 0 || task.Status().Get().Phase != TaskFailed {
		t.Fatal("failed progress left committed state or active task")
	}
}
