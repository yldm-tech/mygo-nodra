package nodra

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Delay a notification after commit, while allowing newer writers to publish.
func delayedNotificationStore() (*Store[int], <-chan struct{}, chan<- struct{}) {
	entered, release := make(chan struct{}), make(chan struct{})
	var copies atomic.Int32
	store := NewWithClone(0, func(v int) int {
		if v == 1 && copies.Add(1) == 2 {
			close(entered)
			<-release
		}
		return v
	})
	return store, entered, release
}

func TestHistoryIgnoresDelayedOlderNotifications(t *testing.T) {
	store, entered, release := delayedNotificationStore()
	history := Track(store, 10)
	defer func() { _ = history.Close() }()
	done := make(chan error, 1)
	go func() { done <- store.Set(1) }()
	<-entered
	requireNoError(t, store.Set(2))
	close(release)
	requireNoError(t, <-done)
	requireNoError(t, history.Undo())
	if got := store.Get(); got != 0 {
		t.Fatalf("undo = %d, want 0", got)
	}
	requireNoError(t, history.Redo())
	if got := store.Get(); got != 2 {
		t.Fatalf("redo regressed to delayed state: %d", got)
	}
}

func TestHistoryFailedTravelPreservesStacks(t *testing.T) {
	store := New(0)
	history := Track(store, 10)
	defer func() { _ = history.Close() }()
	requireNoError(t, store.Set(1))
	requireNoError(t, store.Close())
	if err := history.Undo(); !errors.Is(err, ErrClosed) {
		t.Fatalf("undo error = %v", err)
	}
	if !history.CanUndo() || history.CanRedo() {
		t.Fatal("failed undo changed history stacks")
	}
}

func TestHistoryClonePanicPreservesStacks(t *testing.T) {
	fail := false
	store := NewWithClone(0, func(v int) int {
		if fail && v == 1 {
			panic("clone failed")
		}
		return v
	})
	history := Track(store, 10)
	defer func() { _ = history.Close() }()
	requireNoError(t, store.Set(1))
	requireNoError(t, store.Set(2))
	fail = true
	func() {
		defer func() {
			if got := recover(); got != "clone failed" {
				t.Fatalf("panic = %v", got)
			}
		}()
		_ = history.Undo()
	}()
	fail = false
	if history.CanRedo() || store.Get() != 2 {
		t.Fatal("failed undo modified history or state")
	}
	requireNoError(t, history.Undo())
	if got := store.Get(); got != 1 {
		t.Fatalf("retry undo = %d, want 1", got)
	}
	requireNoError(t, history.Redo())
	if got := store.Get(); got != 2 {
		t.Fatalf("redo = %d, want 2", got)
	}
}

func TestWatchRetainsNewerVersionAfterDelayedNotification(t *testing.T) {
	store, entered, release := delayedNotificationStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := store.Watch(ctx, 1)
	done := make(chan error, 1)
	go func() { done <- store.Set(1) }()
	<-entered
	requireNoError(t, store.Set(2))
	close(release)
	requireNoError(t, <-done)
	if got := <-updates; got.Version != 2 || got.Value != 2 {
		t.Fatalf("watch regressed: %+v", got)
	}
}

func TestSubscribeEqualityDoesNotRegressAfterDelayedNotification(t *testing.T) {
	store, entered, release := delayedNotificationStore()
	var values []int
	stop := store.SubscribeWith(SubscribeOptions[int]{Equals: func(a, b int) bool { return a == b }}, func(s Snapshot[int]) { values = append(values, s.Value) })
	defer stop()
	done := make(chan error, 1)
	go func() { done <- store.Set(1) }()
	<-entered
	requireNoError(t, store.Set(2))
	close(release)
	requireNoError(t, <-done)
	requireNoError(t, store.Set(2))
	if !reflect.DeepEqual(values, []int{2}) {
		t.Fatalf("values = %v, want [2]", values)
	}
}

func TestProjectionEqualityPanicDoesNotPoisonSubscription(t *testing.T) {
	for _, kind := range []string{"selector", "derived"} {
		t.Run(kind, func(t *testing.T) {
			store := New(0)
			var values []int
			equals := func(a, b int) bool {
				if b == 1 {
					panic("compare failed")
				}
				return a == b
			}
			var stop func()
			if kind == "selector" {
				stop = SubscribeSelectorWith(store, func(v int) int { return v }, func(cur, _ int) { values = append(values, cur) }, SelectorOptions[int]{Equals: equals})
			} else {
				derived := Derive(store, func(v int) int { return v }, equals)
				derived.Subscribe(func(v int) { values = append(values, v) })
				stop = func() { _ = derived.Close() }
			}
			func() {
				defer func() {
					if got := recover(); got != "compare failed" {
						t.Fatalf("panic = %v", got)
					}
				}()
				_ = store.Set(1)
			}()
			done := make(chan error, 1)
			go func() { done <- store.Set(2) }()
			select {
			case err := <-done:
				requireNoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("equality panic left the subscription locked")
			}
			stop()
			if !reflect.DeepEqual(values, []int{2}) {
				t.Fatalf("values = %v, want [2]", values)
			}
		})
	}
}

func TestEqualityCallbacksCanReenter(t *testing.T) {
	for _, kind := range []string{"subscription", "selector", "derived"} {
		t.Run(kind, func(t *testing.T) {
			store := New(0)
			var values []int
			var derived *Derived[int, int]
			equals := func(a, b int) bool {
				if derived != nil {
					_ = derived.Get()
				}
				if b == 1 {
					requireNoError(t, store.Set(2))
				}
				return a == b
			}
			var stop func()
			switch kind {
			case "subscription":
				stop = store.SubscribeWith(SubscribeOptions[int]{Immediate: true, Equals: equals}, func(s Snapshot[int]) { values = append(values, s.Value) })
				values = nil
			case "selector":
				stop = SubscribeSelectorWith(store, func(v int) int { return v }, func(cur, _ int) { values = append(values, cur) }, SelectorOptions[int]{Equals: equals})
			case "derived":
				derived = Derive(store, func(v int) int { return v }, equals)
				derived.Subscribe(func(v int) { values = append(values, v) })
				stop = func() { _ = derived.Close() }
			}
			done := make(chan error, 1)
			go func() { done <- store.Set(1) }()
			select {
			case err := <-done:
				requireNoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("reentrant equality deadlocked")
			}
			stop()
			if store.Get() != 2 || !reflect.DeepEqual(values, []int{2}) {
				t.Fatalf("state = %d, values = %v", store.Get(), values)
			}
		})
	}
}

func TestHistorySupportsReentrantTravel(t *testing.T) {
	store := New(0)
	history := Track(store, 10)
	defer func() { _ = history.Close() }()
	requireNoError(t, store.Set(1))
	requireNoError(t, store.Set(2))
	nested := false
	stop := store.Subscribe(func(s Snapshot[int]) {
		if s.Value == 1 && !nested {
			nested = true
			requireNoError(t, history.Undo())
		}
	})
	requireNoError(t, history.Undo())
	stop()
	if !nested || store.Get() != 0 || history.CanUndo() {
		t.Fatalf("nested undo: state=%d canUndo=%v", store.Get(), history.CanUndo())
	}
	for _, want := range []int{1, 2} {
		requireNoError(t, history.Redo())
		if got := store.Get(); got != want {
			t.Fatalf("redo=%d, want %d", got, want)
		}
	}
}

func TestHistoryConcurrentTravelPreservesEveryStep(t *testing.T) {
	store := New(0)
	history := Track(store, 20)
	defer func() { _ = history.Close() }()
	for n := 1; n <= 20; n++ {
		requireNoError(t, store.Set(n))
	}
	done := make(chan error, 20)
	for range 20 {
		go func() { done <- history.Undo() }()
	}
	for range 20 {
		requireNoError(t, <-done)
	}
	if store.Get() != 0 || history.CanUndo() {
		t.Fatalf("state = %d, canUndo = %v", store.Get(), history.CanUndo())
	}
	for want := 1; want <= 20; want++ {
		requireNoError(t, history.Redo())
		if got := store.Get(); got != want {
			t.Fatalf("redo=%d, want %d", got, want)
		}
	}
}

func TestHistoryRejectsTravelInsideBatch(t *testing.T) {
	store := New(0)
	history := Track(store, 10)
	defer func() { _ = history.Close() }()
	requireNoError(t, store.Set(1))
	requireNoError(t, store.Batch(func() {
		if err := history.Undo(); !errors.Is(err, ErrHistoryBatch) {
			t.Fatalf("undo in batch = %v", err)
		}
		requireNoError(t, store.Set(2))
	}))
	requireNoError(t, history.Undo())
	if got := store.Get(); got != 1 {
		t.Fatalf("undo = %d, want 1", got)
	}
}

func TestEqualityRetriesAgainstNewerBaseline(t *testing.T) {
	for _, kind := range []string{"subscription", "selector", "derived"} {
		t.Run(kind, func(t *testing.T) {
			store := New(0)
			entered1, entered2 := make(chan struct{}), make(chan struct{})
			release1, release2 := make(chan struct{}), make(chan struct{})
			done1, done2 := make(chan error, 1), make(chan error, 1)
			var compared2 atomic.Int32
			equals := func(a, b int) bool {
				if b == 1 {
					close(entered1)
					<-release1
				}
				if b == 2 && compared2.Add(1) == 1 {
					close(entered2)
					<-release2
				}
				if b == 2 && a == 1 {
					return true
				}
				return a == b
			}
			var values []int
			var stop func()
			switch kind {
			case "subscription":
				stop = store.SubscribeWith(SubscribeOptions[int]{Immediate: true, Equals: equals}, func(s Snapshot[int]) { values = append(values, s.Value) })
				values = nil
			case "selector":
				stop = SubscribeSelectorWith(store, func(v int) int { return v }, func(cur, _ int) { values = append(values, cur) }, SelectorOptions[int]{Equals: equals})
			case "derived":
				derived := Derive(store, func(v int) int { return v }, equals)
				derived.Subscribe(func(v int) { values = append(values, v) })
				stop = func() { _ = derived.Close() }
			}
			defer stop()
			go func() { done1 <- store.Set(1) }()
			<-entered1
			go func() { done2 <- store.Set(2) }()
			<-entered2
			close(release1)
			requireNoError(t, <-done1)
			close(release2)
			requireNoError(t, <-done2)
			if compared2.Load() != 2 || !reflect.DeepEqual(values, []int{1}) {
				t.Fatalf("comparisons=%d values=%v", compared2.Load(), values)
			}
		})
	}
}

func TestSubscriptionInitializationPanicReleasesRegistration(t *testing.T) {
	for _, kind := range []string{"subscription", "selector", "derived"} {
		t.Run(kind, func(t *testing.T) {
			store := New(0)
			equals := func(_, _ int) bool { panic("setup failed") }
			selector := func(v int) int {
				if v == 0 {
					requireNoError(t, store.Set(1))
				}
				return v
			}
			func() {
				defer func() {
					if got := recover(); got != "setup failed" {
						t.Fatalf("panic = %v", got)
					}
				}()
				switch kind {
				case "subscription":
					store.SubscribeWith(SubscribeOptions[int]{Immediate: true}, func(Snapshot[int]) { panic("setup failed") })
				case "selector":
					SubscribeSelectorWith(store, selector, func(int, int) {}, SelectorOptions[int]{Equals: equals})
				case "derived":
					Derive(store, selector, equals)
				}
			}()
			store.mu.RLock()
			registrations := len(store.listeners)
			store.mu.RUnlock()
			if registrations != 0 {
				t.Fatalf("failed setup leaked %d subscriptions", registrations)
			}
			requireNoError(t, store.Set(2))
		})
	}
}

func TestHistoryInitializationPanicReleasesRegistration(t *testing.T) {
	armed, copies := false, 0
	store := NewWithClone(0, func(v int) int {
		if armed {
			copies++
			if copies == 2 {
				panic("snapshot failed")
			}
		}
		return v
	})
	armed = true
	func() {
		defer func() {
			if got := recover(); got != "snapshot failed" {
				t.Fatalf("panic = %v", got)
			}
		}()
		Track(store, 10)
	}()
	store.mu.RLock()
	registrations := len(store.listeners)
	store.mu.RUnlock()
	if registrations != 0 {
		t.Fatalf("failed setup leaked %d subscriptions", registrations)
	}
	requireNoError(t, store.Set(1))
}
