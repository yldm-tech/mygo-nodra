package nodra

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

type counterState struct {
	Count int
	Name  string
}

// requireNoError fails the test immediately when a store operation that is expected to succeed returns an error.
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestStorePublishesSnapshotsAfterUpdates(t *testing.T) {
	store := New(counterState{Count: 1})
	var got []Snapshot[counterState]
	unsubscribe := store.Subscribe(func(snapshot Snapshot[counterState]) { got = append(got, snapshot) })

	requireNoError(t, store.Update(func(state *counterState) { state.Count++ }))
	requireNoError(t, store.Set(counterState{Count: 3, Name: "done"}))
	unsubscribe()
	requireNoError(t, store.Update(func(state *counterState) { state.Count++ }))

	if len(got) != 2 {
		t.Fatalf("notifications = %d, want 2", len(got))
	}
	if got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("versions = %d, %d", got[0].Version, got[1].Version)
	}
	if got[1].Value.Count != 3 || got[1].Value.Name != "done" {
		t.Fatalf("snapshot = %#v", got[1].Value)
	}
	if store.Version() != 3 || store.Snapshot().Value.Count != 4 {
		t.Fatalf("store = version %d, value %#v", store.Version(), store.Snapshot().Value)
	}
}

func TestBatchCoalescesNestedUpdates(t *testing.T) {
	store := New(0)
	var got []Snapshot[int]
	store.Subscribe(func(snapshot Snapshot[int]) { got = append(got, snapshot) })

	requireNoError(t, store.Batch(func() {
		requireNoError(t, store.Set(1))
		requireNoError(t, store.Batch(func() {
			requireNoError(t, store.Set(2))
			requireNoError(t, store.Set(3))
		}))
		requireNoError(t, store.Set(4))
	}))

	if !reflect.DeepEqual(got, []Snapshot[int]{{Value: 4, Version: 1}}) {
		t.Fatalf("snapshots = %#v", got)
	}
}

func TestSelectorOnlyPublishesWhenSelectionChanges(t *testing.T) {
	store := New(counterState{Count: 1, Name: "a"})
	var got []int
	unsubscribe := SubscribeSelector(store, func(state counterState) int { return state.Count }, func(value int) { got = append(got, value) })
	defer unsubscribe()

	requireNoError(t, store.Update(func(state *counterState) { state.Name = "b" }))
	requireNoError(t, store.Update(func(state *counterState) { state.Count = 2 }))
	requireNoError(t, store.Update(func(state *counterState) { state.Count = 2 }))

	if !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("selected values = %#v", got)
	}
}

func TestWatchStopsWithContext(t *testing.T) {
	store := New(0)
	ctx, cancel := context.WithCancel(context.Background())
	updates := store.Watch(ctx, 1)
	requireNoError(t, store.Set(1))
	select {
	case snapshot := <-updates:
		if snapshot.Value != 1 {
			t.Fatalf("value = %d", snapshot.Value)
		}
	case <-context.Background().Done():
		t.Fatal("timed out waiting for update")
	}
	cancel()
	requireNoError(t, store.Set(2))
	for range updates {
	}
}

func TestStoreSupportsConcurrentUpdates(t *testing.T) {
	store := New(0)
	const workers = 8
	const updates = 100
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range updates {
				if err := store.Update(func(value *int) { *value++ }); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if store.Snapshot().Value != workers*updates {
		t.Fatalf("value = %d", store.Snapshot().Value)
	}
}
