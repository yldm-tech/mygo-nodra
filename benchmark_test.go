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

func BenchmarkLargeStateReads(b *testing.B) {
	store := NewWithClone(make([]int, 10000), slices.Clone[[]int])
	for _, projected := range []bool{false, true} {
		b.Run(fmt.Sprintf("projection=%v", projected), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if projected {
					_ = Select(store, func(v []int) int { return len(v) })
				} else {
					_ = len(store.Get())
				}
			}
		})
	}
}

func BenchmarkLargeStateSelectors(b *testing.B) {
	for _, projected := range []bool{false, true} {
		for _, listeners := range []int{1, 10} {
			b.Run(fmt.Sprintf("projection=%v/listeners=%d", projected, listeners), func(b *testing.B) {
				store := NewWithClone(make([]int, 10000), slices.Clone[[]int])
				project := func(v []int) int { return v[0] }
				for range listeners {
					if projected {
						SubscribeProjection(store, project, func(int) {})
					} else {
						SubscribeSelector(store, project, func(int) {})
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						_ = store.Update(func(v *[]int) { (*v)[0]++ })
					}
				})
			})
		}
	}
}
