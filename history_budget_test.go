package nodra

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
)

func TestHistoryByteBudgetIncludesUndoAndRedo(t *testing.T) {
	store := New("aa")
	history := TrackWith(store, HistoryOptions[string]{Limit: 10, MaxBytes: 6, Size: func(v string) int64 { return int64(len(v)) }})
	defer func() { _ = history.Close() }()
	for _, value := range []string{"bbb", "cccc", "ddddd"} {
		requireNoError(t, store.Set(value))
	}
	state := history.Status().Get()
	if state.Bytes != 4 || state.UndoEntries != 1 || state.Dropped != 2 {
		t.Fatalf("budget: %+v", state)
	}
	requireNoError(t, history.Undo())
	state = history.Status().Get()
	if state.Bytes != 5 || state.RedoEntries != 1 || state.CanUndo || !state.CanRedo || store.Get() != "cccc" {
		t.Fatalf("undo budget: %+v", state)
	}
	requireNoError(t, history.Redo())
	if history.Status().Get().Bytes != 4 {
		t.Fatal("redo accounting")
	}
	requireNoError(t, store.Set(strings.Repeat("x", 100)))
	requireNoError(t, store.Set("z"))
	state = history.Status().Get()
	if state.Bytes != 0 || state.UndoEntries != 0 || state.Dropped != 5 {
		t.Fatalf("oversized value retained: %+v", state)
	}
}

func TestHistoryStatusGroupsCloseAndReentrancy(t *testing.T) {
	store := New(0)
	history := Track(store, 10)
	var observed []HistoryState
	stop := history.Status().Subscribe(func(s Snapshot[HistoryState]) {
		observed = append(observed, s.Value)
		_ = store.Get()
		_ = history.CanUndo()
	})
	defer stop()
	end, err := history.BeginGroup()
	requireNoError(t, err)
	requireNoError(t, store.Set(1))
	requireNoError(t, store.Set(2))
	if state := history.Status().Get(); !state.Grouped || state.CanUndo || state.UndoEntries != 1 {
		t.Fatalf("group status: %+v", state)
	}
	end()
	end()
	if !history.Status().Get().CanUndo {
		t.Fatal("end group did not notify availability")
	}
	requireNoError(t, history.Undo())
	requireNoError(t, history.Close())
	state := history.Status().Get()
	if !state.Closed || state.Bytes != 0 || state.CanRedo || state.CanUndo || len(observed) < 5 {
		t.Fatalf("closed status: %+v events=%d", state, len(observed))
	}
	select {
	case <-history.Status().Done():
	default:
		t.Fatal("status source not closed")
	}
}

func TestHistoryBudgetConcurrentAndInvalidEstimators(t *testing.T) {
	store := New(0)
	history := TrackWith(store, HistoryOptions[int]{Limit: 100, MaxBytes: math.MaxInt64, Size: func(int) int64 { return math.MaxInt64 }})
	defer func() { _ = history.Close() }()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if err := store.Update(func(v *int) { *v++ }); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	state := history.Status().Get()
	// Concurrent notifications can coalesce older versions; every accepted history step still obeys the budget.
	if state.Bytes != math.MaxInt64 || state.UndoEntries != 1 || state.Dropped == 0 || state.Revision != state.Dropped+1 {
		t.Fatalf("overflow/concurrency: %+v", state)
	}
	for _, options := range []HistoryOptions[int]{{Limit: 1, MaxBytes: 1}, {Limit: 1, MaxBytes: -1}, {Limit: 1, MaxBytes: 1, Size: func(int) int64 { return -1 }}} {
		func() {
			defer func() {
				if !errors.Is(asError(recover()), ErrInvalidHistory) {
					t.Error("invalid sizing did not panic")
				}
			}()
			TrackWith(New(0), options)
		}()
	}
}

func asError(value any) error { err, _ := value.(error); return err }
