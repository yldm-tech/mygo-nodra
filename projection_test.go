package nodra

import (
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProjectionAvoidsWholeStateCopiesAndOwnsResult(t *testing.T) {
	var copies atomic.Int64
	store := NewWithClone(map[string][]int{"items": {1, 2}}, func(s map[string][]int) map[string][]int {
		copies.Add(1)
		out := maps.Clone(s)
		out["items"] = slices.Clone(s["items"])
		return out
	})
	copies.Store(0)
	read := Select(store, func(s map[string][]int) []int { return slices.Clone(s["items"]) })
	read.Value[0] = 99
	if copies.Load() != 0 {
		t.Fatal("projection cloned the whole state")
	}
	if Select(store, func(s map[string][]int) int { return s["items"][0] }).Value != 1 {
		t.Fatal("projection result aliased state")
	}
	var got []int
	stop := SubscribeProjection(store, func(s map[string][]int) []int { return slices.Clone(s["items"]) }, func(v []int) { got = v })
	defer stop()
	requireNoError(t, store.Update(func(s *map[string][]int) { (*s)["items"][0] = 2 }))
	if !slices.Equal(got, []int{2, 2}) {
		t.Fatalf("projection: %v", got)
	}
	if copies.Load() != 2 {
		t.Fatalf("copies=%d, want only candidate and commit copies", copies.Load())
	}
	if read.Version != 0 || store.Version() != 1 {
		t.Fatal("projection lost source version")
	}
}

func TestProjectionConcurrentVersionOrdering(t *testing.T) {
	store := New(0)
	entered, release := make(chan struct{}), make(chan struct{})
	var got []int
	stop := SubscribeProjection(store, func(v int) int {
		if v == 1 {
			close(entered)
			<-release
		}
		return v
	}, func(v int) { got = append(got, v) })
	defer stop()
	var wg sync.WaitGroup
	wg.Go(func() { requireNoError(t, store.Set(1)) })
	<-entered
	requireNoError(t, store.Set(2))
	close(release)
	wg.Wait()
	if !slices.Equal(got, []int{2}) {
		t.Fatalf("obsolete projection: %v", got)
	}
}

func TestProjectionPanicAndCleanup(t *testing.T) {
	store := New(0)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		Select(store, func(int) int { panic("read") })
	}()
	requireNoError(t, store.Set(1))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		SubscribeProjectionWith(store, func(v int) int { return v }, func(int, int) { panic("initial") }, SelectorOptions[int]{Immediate: true})
	}()
	if listenerCount(store) != 0 {
		t.Fatal("failed projection leaked subscription")
	}
}
