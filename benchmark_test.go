package nodra

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
)

func BenchmarkActionScheduling(b *testing.B) {
	for _, policy := range []ActionPolicy{LatestWins, QueueAll} {
		b.Run(fmt.Sprintf("policy=%d", policy), func(b *testing.B) {
			store := New(0)
			action, err := NewAsyncActionWithOptions(store, "increment", func(_ context.Context, value int, progress func(float64)) (int, error) {
				progress(.5)
				return value, nil
			}, func(state *int, value int) error { *state += value; return nil }, ActionOptions[int]{Policy: policy, MaxConcurrent: 4})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = action.Close(context.Background()); _ = store.Close() })
			b.ReportAllocs()
			for b.Loop() {
				task, err := action.Run(context.Background(), 1)
				if err != nil {
					b.Fatal(err)
				}
				if err := task.Wait(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDerivedProjectionPipeline(b *testing.B) {
	for _, projected := range []bool{false, true} {
		b.Run(fmt.Sprintf("projection=%v", projected), func(b *testing.B) {
			store := NewWithClone(make([]int, 10000), slices.Clone[[]int])
			project := func(value []int) int { return value[0] }
			var derived *Derived[[]int, int]
			if projected {
				derived = DeriveProjection(store, project, nil)
			} else {
				derived = Derive(store, project, nil)
			}
			chained := Derive(derived, func(value int) int { return value * 2 }, nil)
			b.Cleanup(func() { _ = chained.Close(); _ = derived.Close(); _ = store.Close() })
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Update(func(value *[]int) { (*value)[0]++ }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCombinedValues(b *testing.B) {
	a, bSource := New(0), New(1)
	combined := Combine(a, bSource, func(a, b int) int { return a + b }, nil)
	b.Cleanup(func() { _ = combined.Close(); _ = a.Close(); _ = bSource.Close() })
	b.ReportAllocs()
	for b.Loop() {
		if err := a.Update(func(value *int) { *value++ }); err != nil {
			b.Fatal(err)
		}
	}
}

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
