package nodra

import (
	"slices"
	"sync"
	"testing"

	"github.com/egoist/mygo"
)

func listenerCount[T any](store *Store[T]) int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.listeners)
}

func TestInvalidateHandlesNilInputs(t *testing.T) {
	var nilStore *Store[int]
	if stop := Invalidate(nilStore, &mygo.Window{}); stop == nil {
		t.Fatal("nil store returned nil stop")
	}
	store := New(0)
	stop := Invalidate(store, nil)
	if stop == nil {
		t.Fatal("nil window returned nil stop")
	}
	stop()
	if n := listenerCount(store); n != 0 {
		t.Fatalf("nil window subscribed %d listeners, want 0", n)
	}
}

func TestInvalidateSubscribesUntilStopped(t *testing.T) {
	store := New(0)
	// A window without native Content ignores Invalidate, so publishing exercises the subscription without a running MyGo app.
	stop := Invalidate(store, &mygo.Window{})
	if n := listenerCount(store); n != 1 {
		t.Fatalf("listeners after Invalidate = %d, want 1", n)
	}
	if err := store.Set(1); err != nil {
		t.Fatal(err)
	}
	stop()
	if n := listenerCount(store); n != 0 {
		t.Fatalf("listeners after stop = %d, want 0", n)
	}
	if err := store.Set(2); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidateSubscriptionsStopIndependently(t *testing.T) {
	store := New(0)
	window := &mygo.Window{}
	first, second := Invalidate(store, window), Invalidate(store, window)
	defer second()
	first()
	first()
	if n := listenerCount(store); n != 1 {
		t.Fatalf("listeners after stopping first subscription = %d, want 1", n)
	}
	requireNoError(t, store.Set(1))
	second()
	if n := listenerCount(store); n != 0 {
		t.Fatalf("listeners after stopping both subscriptions = %d, want 0", n)
	}
}

func TestInvalidateConcurrentStopAndUpdate(t *testing.T) {
	store := New(0)
	stop := Invalidate(store, &mygo.Window{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				requireNoError(t, store.Update(func(n *int) { *n++ }))
				stop()
			}
		})
	}
	wg.Wait()
	if n := listenerCount(store); n != 0 {
		t.Fatalf("listeners after concurrent stop = %d, want 0", n)
	}
}

func TestInvalidateSelectorOptionsAndStop(t *testing.T) {
	type state struct {
		Values []int
		Other  int
	}
	store := New(state{Values: []int{1}})
	comparisons := 0
	stop := InvalidateSelectorWith(store, &mygo.Window{}, func(s state) []int { return s.Values }, SelectorOptions[[]int]{
		Equals: func(a, b []int) bool {
			comparisons++
			return slices.Equal(a, b)
		},
	})
	defer stop()
	requireNoError(t, store.Update(func(s *state) { s.Other++ }))
	requireNoError(t, store.Update(func(s *state) { s.Values = []int{1, 2} }))
	if comparisons != 2 {
		t.Fatalf("custom equality comparisons = %d, want 2", comparisons)
	}
	stop()
	requireNoError(t, store.Set(state{}))
	if comparisons != 2 || listenerCount(store) != 0 {
		t.Fatal("selector subscription remained active after stop")
	}
}

func TestInvalidateSelectorNilInputs(t *testing.T) {
	store := New(0)
	selector := func(v int) int { return v }
	for _, stop := range []func(){
		InvalidateSelector[int, int](nil, &mygo.Window{}, selector),
		InvalidateSelector(store, nil, selector),
		InvalidateSelector[int, int](store, &mygo.Window{}, nil),
	} {
		if stop == nil {
			t.Fatal("nil input returned nil stop")
		}
		stop()
	}
	if n := listenerCount(store); n != 0 {
		t.Fatalf("nil inputs subscribed %d listeners, want 0", n)
	}
}

// invalidationSpy models the window's lifecycle without a native event loop. Like MyGo, closing snapshots listeners, and OnClosed does not replay past closes.
type invalidationSpy struct {
	mu            sync.Mutex
	destroyed     bool
	frames        int
	next          int
	closed        map[int]func()
	beforeOnClose func()
}

func (w *invalidationSpy) Invalidate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames++
}

func (w *invalidationSpy) IsDestroyed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.destroyed
}

func (w *invalidationSpy) OnClosed(fn func()) func() {
	if w.beforeOnClose != nil {
		w.beforeOnClose()
	}
	w.mu.Lock()
	if w.closed == nil {
		w.closed = make(map[int]func())
	}
	w.next++
	id := w.next
	w.closed[id] = fn
	w.mu.Unlock()
	return func() { w.mu.Lock(); delete(w.closed, id); w.mu.Unlock() }
}

func (w *invalidationSpy) close() {
	w.mu.Lock()
	if w.destroyed {
		w.mu.Unlock()
		return
	}
	w.destroyed = true
	fns := make([]func(), 0, len(w.closed))
	for _, fn := range w.closed {
		fns = append(fns, fn)
	}
	w.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func TestWindowInvalidationLifecycle(t *testing.T) {
	for _, mode := range []string{"stop", "close", "already closed", "close before listener"} {
		t.Run(mode, func(t *testing.T) {
			store := New(0)
			window := &invalidationSpy{}
			if mode == "already closed" {
				window.close()
			}
			if mode == "close before listener" {
				window.beforeOnClose = window.close
			}
			stop := bindInvalidation(window, func(redraw func()) func() {
				return store.Subscribe(func(Snapshot[int]) { redraw() })
			})
			requireNoError(t, store.Set(1))
			if mode == "stop" || mode == "close" {
				if window.frames != 1 {
					t.Fatalf("frames before stopping = %d, want 1", window.frames)
				}
			} else if window.frames != 0 {
				t.Fatal("closed window requested a frame")
			}
			if mode == "stop" {
				stop()
			} else {
				window.close()
			}
			before := window.frames
			requireNoError(t, store.Set(2))
			stop()
			stop()
			if window.frames != before || len(window.closed) != 0 || listenerCount(store) != 0 {
				t.Fatal("window or store subscription remained after stopping")
			}
		})
	}
}

func TestWindowClosesWhileSubscriptionRegisters(t *testing.T) {
	store := New(0)
	window := &invalidationSpy{}
	registered, release := make(chan struct{}), make(chan struct{})
	done := make(chan func())
	go func() {
		done <- bindInvalidation(window, func(redraw func()) func() {
			stop := store.Subscribe(func(Snapshot[int]) { redraw() })
			close(registered)
			<-release
			return stop
		})
	}()
	<-registered
	window.close()
	// An already captured notification must not schedule a frame while its unsubscribe function is still being returned.
	requireNoError(t, store.Set(1))
	close(release)
	stop := <-done
	stop()
	if window.frames != 0 || len(window.closed) != 0 || listenerCount(store) != 0 {
		t.Fatal("late registration leaked or invalidated a closed window")
	}
}

func TestWindowSelectorFiltersUnrelatedAndEqualUpdates(t *testing.T) {
	type state struct {
		Values []int
		Other  int
	}
	store := New(state{Values: []int{1}})
	window := &invalidationSpy{}
	stop := bindInvalidation(window, func(redraw func()) func() {
		return SubscribeSelectorWith(store, func(s state) []int { return s.Values }, func(_, _ []int) { redraw() }, SelectorOptions[[]int]{
			Immediate: true,
			Equals:    slices.Equal[[]int],
		})
	})
	defer stop()
	if window.frames != 1 {
		t.Fatalf("immediate frames = %d, want 1", window.frames)
	}
	requireNoError(t, store.Update(func(s *state) { s.Other++ }))
	requireNoError(t, store.Update(func(s *state) { s.Values = []int{1} }))
	if window.frames != 1 {
		t.Fatal("unrelated or equal update requested a frame")
	}
	requireNoError(t, store.Update(func(s *state) { s.Values = []int{1, 2} }))
	if window.frames != 2 {
		t.Fatalf("frames after selected change = %d, want 2", window.frames)
	}
	window.close()
	requireNoError(t, store.Set(state{}))
	if window.frames != 2 || listenerCount(store) != 0 {
		t.Fatal("selector remained active after its window closed")
	}
}

func TestWindowInvalidationInitializationPanic(t *testing.T) {
	window := &invalidationSpy{}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("selector panic was swallowed")
			}
		}()
		bindInvalidation(window, func(redraw func()) func() {
			return SubscribeSelector(New(0), func(int) int { panic("selector failed") }, func(int) { redraw() })
		})
	}()
	if len(window.closed) != 0 {
		t.Fatal("initialization panic leaked a window listener")
	}
}

func TestWindowInvalidationConcurrentCloseStopAndPublish(t *testing.T) {
	store := New(0)
	window := &invalidationSpy{}
	stop := bindInvalidation(window, func(redraw func()) func() {
		return store.Subscribe(func(Snapshot[int]) { redraw() })
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				requireNoError(t, store.Update(func(v *int) { *v++ }))
				window.close()
				stop()
			}
		})
	}
	wg.Wait()
	if len(window.closed) != 0 || listenerCount(store) != 0 {
		t.Fatal("concurrent shutdown leaked a subscription")
	}
}
