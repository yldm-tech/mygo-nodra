package nodra

import (
	"errors"
	"testing"
)

func TestHistoryGroupingAndIgnoredFields(t *testing.T) {
	type state struct {
		Text     string
		Progress int
	}
	store := New(state{Text: "before"})
	history := TrackWith(store, HistoryOptions[state]{Limit: 10, Equals: func(a, b state) bool { return a.Text == b.Text }, Restore: func(current, recorded state) state { current.Text = recorded.Text; return current }})
	defer func() { _ = history.Close() }()
	end, err := history.BeginGroup()
	requireNoError(t, err)
	nested, err := history.BeginGroup()
	requireNoError(t, err)
	for _, text := range []string{"b", "be", "between", "after"} {
		requireNoError(t, store.Update(func(s *state) { s.Text = text; s.Progress++ }))
	}
	if err := history.Undo(); !errors.Is(err, ErrHistoryGroup) {
		t.Fatalf("group undo: %v", err)
	}
	nested()
	nested()
	end()
	end()
	requireNoError(t, store.Update(func(s *state) { s.Progress = 99 }))
	requireNoError(t, history.Undo())
	if got := store.Get(); got != (state{Text: "before", Progress: 99}) {
		t.Fatalf("undo: %+v", got)
	}
	if history.CanUndo() {
		t.Fatal("one interaction recorded multiple steps")
	}
	requireNoError(t, store.Update(func(s *state) { s.Progress = 100 }))
	requireNoError(t, history.Redo())
	if got := store.Get(); got != (state{Text: "after", Progress: 100}) {
		t.Fatalf("redo: %+v", got)
	}
}

func TestHistoryEmptyGroupAndBoundedEntries(t *testing.T) {
	store := New(0)
	history := TrackWith(store, HistoryOptions[int]{Limit: 2, Equals: func(a, b int) bool { return a == b }})
	defer func() { _ = history.Close() }()
	end, err := history.BeginGroup()
	requireNoError(t, err)
	requireNoError(t, store.Set(0))
	end()
	if history.CanUndo() {
		t.Fatal("empty group recorded")
	}
	for n := 1; n <= 4; n++ {
		requireNoError(t, store.Set(n))
	}
	for _, want := range []int{3, 2} {
		requireNoError(t, history.Undo())
		if store.Get() != want {
			t.Fatalf("undo: %d", store.Get())
		}
	}
	if err := history.Undo(); !errors.Is(err, ErrNoUndo) {
		t.Fatalf("limit: %v", err)
	}
}
