package nodra

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"
)

type cloneState struct {
	Items map[string][]int
	Count *int
}

func cloneStateValue(v cloneState) cloneState {
	v.Items = maps.Clone(v.Items)
	for key, items := range v.Items {
		v.Items[key] = slices.Clone(items)
	}
	if v.Count != nil {
		count := *v.Count
		v.Count = &count
	}
	return v
}

func initialCloneState() cloneState {
	count := 1
	return cloneState{Items: map[string][]int{"a": {1, 2}}, Count: &count}
}

func damageCloneState(v cloneState) {
	v.Items["a"][0] = 99
	*v.Count = 99
}

func TestCloneIsolatesOwnershipAndSnapshots(t *testing.T) {
	initial := initialCloneState()
	store := NewWithClone(initial, cloneStateValue)
	damageCloneState(initial)
	damageCloneState(store.Get())
	damageCloneState(store.Snapshot().Value)
	if !reflect.DeepEqual(store.Get(), initialCloneState()) {
		t.Fatal("initial state or reads escaped store ownership")
	}
	input := initialCloneState()
	requireNoError(t, store.Set(input))
	damageCloneState(input)
	var retained cloneState
	requireNoError(t, store.Update(func(v *cloneState) {
		v.Items["a"][0] = 3
		*v.Count = 3
		retained = *v
	}))
	damageCloneState(retained)
	if got := store.Get(); got.Items["a"][0] != 3 || *got.Count != 3 {
		t.Fatalf("retained update candidate escaped: %+v", got)
	}
	requireNoError(t, store.Reset())
	if !reflect.DeepEqual(store.Get(), initialCloneState()) {
		t.Fatal("Reset did not preserve independent initial state")
	}
}

func TestCloneRollsBackReferencesOnErrorAndPanic(t *testing.T) {
	for _, failure := range []string{"error", "panic"} {
		t.Run(failure, func(t *testing.T) {
			store := NewWithClone(initialCloneState(), cloneStateValue)
			calls := 0
			store.Subscribe(func(Snapshot[cloneState]) { calls++ })
			want := errors.New("abort")
			func() {
				if failure == "panic" {
					defer func() {
						if got := recover(); got != want {
							t.Fatalf("panic = %v", got)
						}
					}()
				}
				err := store.Transaction(func(v *cloneState) error {
					damageCloneState(*v)
					if failure == "panic" {
						panic(want)
					}
					return want
				})
				if !errors.Is(err, want) {
					t.Fatalf("error = %v", err)
				}
			}()
			if !reflect.DeepEqual(store.Get(), initialCloneState()) || calls != 0 || store.Version() != 0 {
				t.Fatal("failed transaction changed referenced state or published")
			}
			requireNoError(t, store.Update(func(v *cloneState) { *v.Count = 2 }))
		})
	}
}

func TestCloneSubscribersAndMutationEventsAreIndependent(t *testing.T) {
	store := NewWithClone(initialCloneState(), cloneStateValue)
	for range 4 {
		store.Subscribe(func(s Snapshot[cloneState]) {
			if s.Value.Items["a"][0] != 2 {
				t.Error("subscriber received another listener's mutation")
			}
			damageCloneState(s.Value)
		})
		store.SubscribeMutations(func(m Mutation[cloneState]) {
			if m.Previous.Items["a"][0] != 1 || m.Value.Items["a"][0] != 2 {
				t.Error("mutation metadata was modified by another listener")
			}
			damageCloneState(m.Previous)
			damageCloneState(m.Value)
		})
	}
	requireNoError(t, store.Update(func(v *cloneState) { v.Items["a"][0] = 2 }))
	if store.Get().Items["a"][0] != 2 {
		t.Fatal("listener mutated owned state")
	}
}

func TestCloneSubscriberEqualityKeepsPrivatePreviousValue(t *testing.T) {
	store := NewWithClone(initialCloneState(), cloneStateValue)
	calls := 0
	store.SubscribeWith(SubscribeOptions[cloneState]{Immediate: true, Equals: func(a, b cloneState) bool {
		return *a.Count == *b.Count
	}}, func(s Snapshot[cloneState]) {
		calls++
		damageCloneState(s.Value)
	})
	requireNoError(t, store.Set(initialCloneState()))
	if calls != 1 {
		t.Fatalf("listener mutation corrupted equality baseline: %d calls", calls)
	}
}

func TestCloneKeepsHistoryAndBatchValues(t *testing.T) {
	store := NewWithClone(initialCloneState(), cloneStateValue)
	history := Track(store, 10)
	defer func() { _ = history.Close() }()
	var before int
	store.SubscribeMutations(func(m Mutation[cloneState]) { before = m.Previous.Items["a"][0] })
	requireNoError(t, store.Batch(func() {
		requireNoError(t, store.Update(func(v *cloneState) { v.Items["a"][0] = 2 }))
		requireNoError(t, store.Update(func(v *cloneState) { v.Items["a"][0] = 3 }))
	}))
	if before != 1 {
		t.Fatalf("batch Previous = %d", before)
	}
	requireNoError(t, history.Undo())
	if store.Get().Items["a"][0] != 1 {
		t.Fatal("undo lost historical references")
	}
	requireNoError(t, history.Redo())
	if store.Get().Items["a"][0] != 3 {
		t.Fatal("redo lost historical references")
	}
}

func TestClonePanicReleasesStoreLock(t *testing.T) {
	fail := false
	store := NewWithClone(initialCloneState(), func(v cloneState) cloneState {
		if fail {
			panic("clone failed")
		}
		return cloneStateValue(v)
	})
	func() {
		defer func() {
			if got := recover(); got != "clone failed" {
				t.Fatalf("panic = %v", got)
			}
		}()
		_ = store.Update(func(v *cloneState) {
			damageCloneState(*v)
			fail = true // fail the commit copy, after mutating the candidate
		})
	}()
	fail = false
	if !reflect.DeepEqual(store.Get(), initialCloneState()) {
		t.Fatal("clone panic committed state")
	}
	requireNoError(t, store.Reset())
}

func TestNamedCloneStoresAndConcurrentSnapshots(t *testing.T) {
	shared := initialCloneState()
	definition := DefineWithClone("shared", func() cloneState { return shared }, cloneStateValue)
	store, other := definition.New(), definition.New()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				damageCloneState(store.Get())
				if err := store.Update(func(v *cloneState) { *v.Count++ }); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if *store.Get().Count != 201 || *other.Get().Count != 1 || store.ID() != "shared" {
		t.Fatal("cloned stores are not independent or lost concurrent updates")
	}
}
