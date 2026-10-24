package devtools

import (
	"context"
	"testing"
	"time"

	"github.com/yldm-tech/nodra"
)

func BenchmarkInspectorTaskProgress(b *testing.B) {
	for _, mode := range []string{"off", "every", "coalesced"} {
		b.Run(mode, func(b *testing.B) {
			store := nodra.New(0)
			var inspector *Inspector
			if mode != "off" {
				options := Options{Limit: 200}
				if mode == "coalesced" {
					options.TaskProgressInterval = time.Hour
				}
				inspector = New(options)
				if _, err := Attach(inspector, "source", store); err != nil {
					b.Fatal(err)
				}
			}
			action, err := nodra.NewAsyncAction(store, "progress", func(_ context.Context, value int, progress func(float64)) (int, error) {
				for n := 1; n <= 128; n++ {
					progress(float64(n) / 129)
				}
				return value, nil
			}, func(state *int, value int) error { *state = value; return nil })
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				_ = action.Close(context.Background())
				if inspector != nil {
					_ = inspector.Close()
				}
				_ = store.Close()
			})
			b.ReportAllocs()
			var iterations uint64
			for b.Loop() {
				task, err := action.Run(context.Background(), 1)
				if err != nil {
					b.Fatal(err)
				}
				if err := task.Wait(context.Background()); err != nil {
					b.Fatal(err)
				}
				iterations++
			}
			if inspector != nil && iterations > 0 {
				inspector.mu.Lock()
				events := inspector.next - 1
				inspector.mu.Unlock()
				b.ReportMetric(float64(events)/float64(iterations), "events/op")
			}
		})
	}
}
