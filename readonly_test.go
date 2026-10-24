package nodra

import (
	"context"
	"maps"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo"
)

func TestReadOnlyCapabilityAndIsolation(t *testing.T) {
	store := NewWithClone(map[string]int{"count": 1}, maps.Clone[map[string]int])
	reader := store.ReadOnly()
	for _, name := range []string{"Set", "Update", "UpdateErr", "Reset", "Close", "Do", "SubscribeActionHooks"} {
		if _, ok := reflect.TypeOf(reader).MethodByName(name); ok {
			t.Fatalf("read-only view exposes %s", name)
		}
	}
	reader.Get()["count"] = 99
	if store.Get()["count"] != 1 {
		t.Fatal("read-only read bypassed cloning")
	}
	var seen int
	stop := SubscribeSelector(reader, func(v map[string]int) int { return v["count"] }, func(v int) { seen = v })
	defer stop()
	requireNoError(t, store.Set(map[string]int{"count": 2}))
	if seen != 2 {
		t.Fatal("read-only selector missed update")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch := reader.Watch(ctx, 1)
	requireNoError(t, store.Close())
	<-reader.Done()
	if _, ok := <-watch; ok {
		t.Fatal("watch remained open")
	}
}

func TestReadOnlyProjectionPipeline(t *testing.T) {
	var clones atomic.Int64
	store := NewWithClone([]int{1, 2}, func(v []int) []int { clones.Add(1); return append([]int(nil), v...) })
	reader := store.ReadOnly()
	clones.Store(0)
	derived := DeriveProjection(reader, func(v []int) int { return v[0] }, func(a, b int) bool { return a == b })
	defer func() { _ = derived.Close() }()
	stop := InvalidateProjection(reader, &mygo.Window{}, func(v []int) int { return v[0] })
	defer stop()
	if reader.Version() != 0 || Select(reader, func(v []int) int { return len(v) }).Value != 2 || clones.Load() != 0 {
		t.Fatal("projection attachment cloned the source")
	}
	requireNoError(t, store.Update(func(v *[]int) { (*v)[0] = 3 }))
	if derived.Get() != 3 || clones.Load() != 2 {
		t.Fatalf("projection pipeline: value=%d clones=%d", derived.Get(), clones.Load())
	}
}

func TestReadOnlyNilSources(t *testing.T) {
	var reader ReadOnly[int]
	var store *Store[int]
	var pointer *ReadOnly[int]
	for _, source := range []Reader[int]{nil, store, reader, pointer} {
		stop := Invalidate(source, &mygo.Window{})
		stop()
		stop = InvalidateSelector(source, &mygo.Window{}, func(v int) int { return v })
		stop()
		stop = InvalidateProjection(source, &mygo.Window{}, func(v int) int { return v })
		stop()
		derived := DeriveProjection(source, func(v int) int { return v }, nil)
		if derived.Get() != 0 {
			t.Fatal("invalid source returned state")
		}
	}
}
