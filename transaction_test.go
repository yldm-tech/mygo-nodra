package nodra

import (
	"errors"
	"sync"
	"testing"
)

func TestTransactionPublishesOnceAndRollsBackOnError(t *testing.T) {
	store := New(1)
	calls := 0
	store.Subscribe(func(Snapshot[int]) { calls++ })
	want := errors.New("fail")
	if err := store.Transaction(func(value *int) error { *value = 9; return want }); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	if store.Get() != 1 || calls != 0 {
		t.Fatalf("rollback value=%d calls=%d", store.Get(), calls)
	}
	if err := store.Transaction(func(value *int) error { *value = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	if store.Get() != 2 || calls != 1 {
		t.Fatalf("commit value=%d calls=%d", store.Get(), calls)
	}
}

func TestTransactionSerializesConcurrentWriters(t *testing.T) {
	store := New(0)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = store.Transaction(func(value *int) error { close(entered); <-release; *value = 1; return nil })
	}()
	<-entered
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = store.Set(2) }()
	close(release)
	<-done
	wg.Wait()
	if store.Get() != 2 {
		t.Fatalf("value=%d", store.Get())
	}
}
