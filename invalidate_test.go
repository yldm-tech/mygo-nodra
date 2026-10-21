package nodra

import (
	"testing"

	"github.com/egoist/mygo"
)

func listenerCount[T any](store *Store[T]) int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.listeners)
}

func TestInvalidateHandlesNilInputs(t *testing.T) {
	var nilStore *Store[int]
	if stop := Invalidate(nilStore, &mygo.Window{}); stop == nil {
		t.Fatal("nil store returned nil stop")
	}
	store := New(0)
	stop := Invalidate(store, nil)
	if stop == nil {
		t.Fatal("nil window returned nil stop")
	}
	stop()
	if n := listenerCount(store); n != 0 {
		t.Fatalf("nil window subscribed %d listeners, want 0", n)
	}
}

func TestInvalidateSubscribesUntilStopped(t *testing.T) {
	store := New(0)
	// A window without native Content ignores Invalidate, so publishing exercises the subscription without a running MyGo app.
	stop := Invalidate(store, &mygo.Window{})
	if n := listenerCount(store); n != 1 {
		t.Fatalf("listeners after Invalidate = %d, want 1", n)
	}
	if err := store.Set(1); err != nil {
		t.Fatal(err)
	}
	stop()
	if n := listenerCount(store); n != 0 {
		t.Fatalf("listeners after stop = %d, want 0", n)
	}
	if err := store.Set(2); err != nil {
		t.Fatal(err)
	}
}
