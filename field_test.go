package nodra

import (
	"errors"
	"maps"
	"testing"
)

func TestFieldEditPreservesConcurrentFieldsAndReturnsValidation(t *testing.T) {
	type state struct {
		Name  string
		Count int
	}
	store := New(state{Name: "before"})
	invalid := errors.New("empty name")
	name, err := BindField(store, func(s state) string { return s.Name }, func(s *state, value string) error {
		s.Name = value
		if value == "" {
			return invalid
		}
		return nil
	})
	requireNoError(t, err)
	requireNoError(t, name.Edit(func(value *string) bool {
		_ = store.Update(func(s *state) { s.Count++ })
		*value = "after"
		return true
	}))
	if store.Get() != (state{Name: "after", Count: 1}) {
		t.Fatal("field edit replaced concurrent state")
	}
	if !errors.Is(name.Set(""), invalid) || name.Get() != "after" {
		t.Fatal("invalid edit changed state")
	}
	version := store.Version()
	requireNoError(t, name.Edit(func(*string) bool { return false }))
	if store.Version() != version {
		t.Fatal("unchanged editor committed")
	}
	requireNoError(t, store.Close())
	if !errors.Is(name.Set("new"), ErrClosed) {
		t.Fatal("closed store accepted field edit")
	}
}

func TestFieldReferenceIsolation(t *testing.T) {
	store := NewWithClone(map[string]int{"a": 1}, maps.Clone[map[string]int])
	field, err := BindField(store, func(s map[string]int) map[string]int { return s }, func(s *map[string]int, value map[string]int) error { *s = value; return nil })
	requireNoError(t, err)
	requireNoError(t, field.Edit(func(value *map[string]int) bool { (*value)["a"] = 2; return false }))
	if store.Get()["a"] != 1 {
		t.Fatal("uncommitted editor mutated shared state")
	}
}
