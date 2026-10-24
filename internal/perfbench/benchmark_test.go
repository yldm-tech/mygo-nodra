// Package perfbench holds identical workloads copied into an isolated baseline checkout by scripts/benchmark.sh. Keep these workloads compatible with the minimum compared release (v0.6.0).
package perfbench

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/yldm-tech/nodra"
	"github.com/yldm-tech/nodra/devtools"
)

func BenchmarkStateUpdate(b *testing.B) {
	for _, listeners := range []int{0, 1, 100} {
		b.Run(fmt.Sprintf("listeners=%d", listeners), func(b *testing.B) {
			store := nodra.New(0)
			for range listeners {
				store.Subscribe(func(nodra.Snapshot[int]) {})
			}
			b.Cleanup(func() { _ = store.Close() })
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Update(func(v *int) { *v++ }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkProjectedDerived(b *testing.B) {
	for _, projected := range []bool{false, true} {
		b.Run(fmt.Sprintf("projection=%v", projected), func(b *testing.B) {
			store := nodra.NewWithClone(make([]int, 10000), slices.Clone[[]int])
			selectValue := func(v []int) int { return v[0] }
			var value *nodra.Derived[[]int, int]
			if projected {
				value = nodra.DeriveProjection(store, selectValue, nil)
			} else {
				value = nodra.Derive(store, selectValue, nil)
			}
			b.Cleanup(func() { _ = value.Close(); _ = store.Close() })
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Update(func(v *[]int) { (*v)[0]++ }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkScheduledAction(b *testing.B) {
	for _, policy := range []nodra.ActionPolicy{nodra.LatestWins, nodra.QueueAll} {
		b.Run(fmt.Sprintf("policy=%d", policy), func(b *testing.B) {
			store := nodra.New(0)
			action, err := nodra.NewAsyncActionWithOptions(store, "increment", func(_ context.Context, v int, progress func(float64)) (int, error) { progress(.5); return v, nil }, func(s *int, v int) error { *s += v; return nil }, nodra.ActionOptions[int]{Policy: policy, MaxConcurrent: 4})
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

func BenchmarkInspectorUpdate(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("enabled=%v", enabled), func(b *testing.B) {
			store := nodra.NewWithClone(make([]int, 1000), slices.Clone[[]int])
			if enabled {
				inspector := devtools.New(devtools.Options{})
				if _, err := devtools.Attach(inspector, "state", store); err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = inspector.Close() })
			}
			b.Cleanup(func() { _ = store.Close() })
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Update(func(v *[]int) { (*v)[0]++ }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
