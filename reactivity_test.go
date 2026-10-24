package nodra

import (
	"reflect"
	"sync"
	"testing"
)

func TestSelectorDeliversFirstChange(t *testing.T) {
	store := New(0)
	var got [][2]int
	stop := SubscribeSelectorWith(store, func(v int) int { return v }, func(cur, prev int) {
		got = append(got, [2]int{cur, prev})
	}, SelectorOptions[int]{})
	defer stop()
	requireNoError(t, store.Set(1))
	requireNoError(t, store.Set(1))
	requireNoError(t, store.Set(2))
	if !reflect.DeepEqual(got, [][2]int{{1, 0}, {2, 1}}) {
		t.Fatalf("changes = %v", got)
	}
}

func TestSelectorCatchesUpdateDuringInitialization(t *testing.T) {
	store := New(0)
	var got []int
	stop := SubscribeSelector(store, func(v int) int {
		if v == 0 {
			requireNoError(t, store.Set(1))
		}
		return v
	}, func(v int) { got = append(got, v) })
	defer stop()
	if !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("changes = %v, want [1]", got)
	}
}

func TestSelectorIgnoresOutdatedComputation(t *testing.T) {
	store := New(0)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var got [][2]int
	stop := SubscribeSelectorWith(store, func(v int) int {
		if v == 1 {
			close(entered)
			<-release
		}
		return v
	}, func(cur, prev int) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, [2]int{cur, prev})
	}, SelectorOptions[int]{})
	defer stop()
	go func() {
		defer close(done)
		if err := store.Set(1); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	requireNoError(t, store.Set(2))
	close(release)
	<-done
	requireNoError(t, store.Set(3))
	if !reflect.DeepEqual(got, [][2]int{{2, 0}, {3, 2}}) {
		t.Fatalf("changes = %v", got)
	}
}

func TestDerivedCatchesUpdateDuringInitialization(t *testing.T) {
	store := New(0)
	derived := Derive(store, func(v int) int {
		if v == 0 {
			requireNoError(t, store.Set(1))
		}
		return v
	}, func(a, b int) bool { return a == b })
	defer func() { _ = derived.Close() }()
	if got := derived.Get(); got != 1 {
		t.Fatalf("derived = %d, want 1", got)
	}
}

func TestDerivedIgnoresOutdatedComputation(t *testing.T) {
	for _, latest := range []int{0, 2} {
		t.Run(map[int]string{0: "equal", 2: "changed"}[latest], func(t *testing.T) {
			store := New(0)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			derived := Derive(store, func(v int) int {
				if v == 1 {
					close(entered)
					<-release
				}
				return v
			}, func(a, b int) bool { return a == b })
			defer func() { _ = derived.Close() }()
			var mu sync.Mutex
			var got []int
			stop := derived.Subscribe(func(v int) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, v)
			})
			defer stop()
			go func() {
				defer close(done)
				if err := store.Set(1); err != nil {
					t.Error(err)
				}
			}()
			<-entered
			requireNoError(t, store.Set(latest))
			close(release)
			<-done
			if value := derived.Get(); value != latest {
				t.Fatalf("derived = %d, want %d", value, latest)
			}
			var want []int
			if latest != 0 {
				want = []int{latest}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("changes = %v, want %v", got, want)
			}
		})
	}
}
