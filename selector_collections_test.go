package nodra

import (
	"maps"
	"reflect"
	"slices"
	"testing"
)

func TestSelectorCollections(t *testing.T) {
	t.Run("slice default", func(t *testing.T) {
		store := New([]int{1})
		var values [][2][]int
		stop := SubscribeSelectorWith(store, func(v []int) []int { return v }, func(cur, prev []int) {
			values = append(values, [2][]int{slices.Clone(cur), slices.Clone(prev)})
		}, SelectorOptions[[]int]{Immediate: true})
		defer stop()
		requireNoError(t, store.Set([]int{1}))
		requireNoError(t, store.Set([]int{1, 2}))
		want := [][2][]int{{{1}, nil}, {{1, 2}, {1}}}
		if !reflect.DeepEqual(values, want) {
			t.Fatalf("values = %v", values)
		}
	})
	t.Run("map custom", func(t *testing.T) {
		store := New(map[string]int{"a": 1})
		var values []map[string]int
		stop := SubscribeSelectorWith(store, func(v map[string]int) map[string]int { return v }, func(cur, _ map[string]int) {
			values = append(values, maps.Clone(cur))
		}, SelectorOptions[map[string]int]{Equals: maps.Equal[map[string]int, map[string]int]})
		defer stop()
		requireNoError(t, store.Set(map[string]int{"a": 1}))
		requireNoError(t, store.Set(map[string]int{"a": 2}))
		if !reflect.DeepEqual(values, []map[string]int{{"a": 2}}) {
			t.Fatalf("values = %v", values)
		}
	})
	t.Run("slice convenience", func(t *testing.T) {
		store := New([]string{"a"})
		calls := 0
		stop := SubscribeSelector(store, func(v []string) []string { return v }, func([]string) { calls++ })
		defer stop()
		requireNoError(t, store.Set([]string{"a"}))
		requireNoError(t, store.Set([]string{"b"}))
		if calls != 1 {
			t.Fatalf("calls = %d", calls)
		}
	})
}

func TestSelectorDefaultEqualityPreservesComparableSemantics(t *testing.T) {
	for _, test := range []struct {
		name  string
		a, b  any
		equal bool
	}{
		{"nil", nil, nil, true},
		{"different types", 1, int64(1), false},
		{"slice in interface", []int{1}, []int{1}, true},
		{"map in interface", map[string]int{"a": 1}, map[string]int{"a": 2}, false},
		{"noncomparable nested interface", struct{ V any }{[]int{1}}, struct{ V any }{[]int{1}}, true},
		{"nil vs empty slice", []int(nil), []int{}, false},
		{"distinct pointers", new(int), new(int), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := New(test.a)
			calls := 0
			stop := SubscribeSelector(store, func(v any) any { return v }, func(any) { calls++ })
			defer stop()
			requireNoError(t, store.Set(test.b))
			if (calls == 0) != test.equal {
				t.Fatalf("calls=%d equal=%v", calls, test.equal)
			}
		})
	}
}

func TestSelectorCustomEqualityRunsBeforeDefaultComparison(t *testing.T) {
	store := New[any]([]int{1})
	calls := 0
	stop := SubscribeSelectorWith(store, func(v any) any { return v }, func(any, any) { calls++ }, SelectorOptions[any]{
		Equals: func(a, b any) bool { return slices.Equal(a.([]int), b.([]int)) },
	})
	defer stop()
	requireNoError(t, store.Set([]int{1}))
	requireNoError(t, store.Set([]int{2}))
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	var nilCallback func([]int)
	SubscribeSelector(New([]int{}), func(v []int) []int { return v }, nilCallback)()
}
