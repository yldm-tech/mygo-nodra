package nodra

import (
	"fmt"
	"maps"
	"slices"
	"testing"
)

func BenchmarkStoreUpdate(b *testing.B) {
	for _, listeners := range []int{0, 1, 100} {
		b.Run(fmt.Sprintf("listeners=%d", listeners), func(b *testing.B) {
			store := New(0)
			for range listeners {
				store.Subscribe(func(Snapshot[int]) {})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = store.Update(func(v *int) { *v++ })
			}
		})
	}
}

func BenchmarkClonedMapUpdate(b *testing.B) {
	initial := make(map[int]int, 1000)
	for n := range 1000 {
		initial[n] = n
	}
	store := NewWithClone(initial, maps.Clone[map[int]int])
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = store.Update(func(v *map[int]int) { (*v)[0]++ })
	}
}

func BenchmarkSliceSelector(b *testing.B) {
	for _, custom := range []bool{false, true} {
		b.Run(fmt.Sprintf("custom=%v", custom), func(b *testing.B) {
			values := make([]int, 1000)
			store := New(values)
			options := SelectorOptions[[]int]{}
			if custom {
				options.Equals = slices.Equal[[]int]
			}
			stop := SubscribeSelectorWith(store, func(v []int) []int { return slices.Clone(v) }, func([]int, []int) {}, options)
			defer stop()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = store.Set(values)
			}
		})
	}
}
