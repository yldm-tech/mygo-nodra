package nodra

import (
	"slices"
	"sync"
	"testing"
)

func TestProjectedDerivedChainPreservesCloneBoundary(t *testing.T) {
	copies := 0
	store := NewWithClone(make([]int, 1000), func(value []int) []int { copies++; return slices.Clone(value) })
	projected := DeriveProjection(store, func(value []int) int { return value[0] }, nil)
	chained := Derive(projected, func(value int) int { return value * 2 }, nil)
	defer func() { _ = chained.Close(); _ = projected.Close() }()
	copies = 0
	requireNoError(t, store.Update(func(value *[]int) { (*value)[0] = 3 }))
	if chained.Get() != 6 || copies != 2 {
		t.Fatalf("projection chain: value=%d clones=%d", chained.Get(), copies)
	}
}

func TestObservableNilBoundariesAndDerivedSelector(t *testing.T) {
	var store *Store[int]
	var derived *Derived[int, int]
	for _, source := range []Observable[int]{nil, store, derived, ReadOnly[int]{}} {
		value := Derive(source, func(v int) int { return v }, nil)
		if value.Get() != 0 {
			t.Fatal("invalid source produced value")
		}
		_ = value.Close()
		SubscribeSelector(source, func(v int) int { return v }, func(int) { t.Fatal("invalid source notified") })()
		Invalidate(source, nil)()
	}
	source := New(0)
	value := Derive(source, func(v int) int { return v * 2 }, nil)
	defer func() { _ = value.Close() }()
	var got int
	stop := SubscribeSelector(value, func(v int) int { return v + 1 }, func(v int) { got = v })
	defer stop()
	requireNoError(t, source.Set(2))
	if got != 5 {
		t.Fatal("selector did not accept derived value")
	}
}

func TestCombineAndDerivedChain(t *testing.T) {
	user, permissions := New("alex"), New(1)
	combined := Combine(user.ReadOnly(), permissions, func(name string, level int) string {
		if level < 2 {
			return "guest"
		}
		return name
	}, func(a, b string) bool { return a == b })
	defer func() { _ = combined.Close() }()
	length := Derive(combined, func(value string) int { return len(value) }, func(a, b int) bool { return a == b })
	defer func() { _ = length.Close() }()
	var changes []int
	stop := length.Subscribe(func(value int) { changes = append(changes, value) })
	defer stop()
	requireNoError(t, user.Set("sam"))
	if length.Get() != 5 || len(changes) != 0 {
		t.Fatal("equal combination notified chain")
	}
	requireNoError(t, permissions.Set(2))
	if length.Get() != 3 || len(changes) != 1 {
		t.Fatalf("chain: %d %v", length.Get(), changes)
	}
	requireNoError(t, combined.Close())
	requireNoError(t, user.Set("longer"))
	if length.Get() != 3 {
		t.Fatal("closed combination changed")
	}
	if listenerCount(user) != 0 || listenerCount(permissions) != 0 {
		t.Fatal("combination leaked source listeners")
	}
}

func TestCombineCatchesInitializationAndRejectsOldComputation(t *testing.T) {
	first, second := New(0), New(0)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	combined := Combine(first, second, func(a, b int) int {
		if a == 0 && b == 0 {
			requireNoError(t, second.Set(1))
		}
		if a == 1 {
			close(entered)
			<-release
		}
		return a + b
	}, func(a, b int) bool { return a == b })
	defer func() { _ = combined.Close() }()
	if combined.Get() != 1 {
		t.Fatal("initial catch-up missed update")
	}
	go func() { done <- first.Set(1) }()
	<-entered
	requireNoError(t, first.Set(2))
	close(release)
	requireNoError(t, <-done)
	if combined.Get() != 3 {
		t.Fatal("old projection replaced current value")
	}
}

func TestCombineConcurrentSourcesAndReentrantObserver(t *testing.T) {
	a, b := New(0), New(0)
	combined := Combine(a, b, func(a, b int) int { return a + b }, func(a, b int) bool { return a == b })
	defer func() { _ = combined.Close() }()
	stop := combined.Subscribe(func(value int) {
		if value == 1 {
			_ = b.Set(1)
		}
	})
	requireNoError(t, a.Set(1))
	stop()
	var wg sync.WaitGroup
	for _, store := range []*Store[int]{a, b} {
		wg.Go(func() {
			for range 100 {
				if err := store.Update(func(v *int) { *v++ }); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if combined.Get() != 202 {
		t.Fatalf("combination did not converge: %d", combined.Get())
	}
}

func TestCombineInitializationPanicDetachesSources(t *testing.T) {
	a, b := New(0), New(0)
	func() {
		defer func() {
			if recover() != "projection" {
				t.Error("expected projection panic")
			}
		}()
		Combine(a, b, func(aValue, _ int) int {
			if aValue == 0 {
				_ = a.Set(1)
				return 0
			}
			panic("projection")
		}, nil)
	}()
	if listenerCount(a) != 0 || listenerCount(b) != 0 {
		t.Fatal("panic leaked source listeners")
	}
}
