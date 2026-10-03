package nodra

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSubscribeOptionsImmediateAndEquality(t *testing.T) {
	store := New(10)
	var values []int
	stop := store.SubscribeWith(SubscribeOptions[int]{Immediate: true, Equals: func(a, b int) bool { return a == b }}, func(snapshot Snapshot[int]) { values = append(values, snapshot.Value) })
	store.Set(10)
	store.Set(11)
	stop()
	if !reflect.DeepEqual(values, []int{10, 11}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestCloseStopsUpdatesAndSubscriptions(t *testing.T) {
	store := New(1)
	called := 0
	store.Subscribe(func(Snapshot[int]) { called++ })
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(2); !errors.Is(err, ErrClosed) {
		t.Fatalf("Set error = %v", err)
	}
	if called != 0 {
		t.Fatalf("called = %d", called)
	}
	if err := store.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Close error = %v", err)
	}
}

func TestUpdateErrRollsBackAndPublishesOnlyOnSuccess(t *testing.T) {
	store := New(5)
	calls := 0
	store.Subscribe(func(Snapshot[int]) { calls++ })
	wantErr := errors.New("nope")
	if err := store.UpdateErr(func(value *int) error { *value = 9; return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
	if store.Get() != 5 || calls != 0 {
		t.Fatalf("after rollback value=%d calls=%d", store.Get(), calls)
	}
	if err := store.UpdateErr(func(value *int) error { *value = 9; return nil }); err != nil {
		t.Fatal(err)
	}
	if store.Get() != 9 || calls != 1 {
		t.Fatalf("after success value=%d calls=%d", store.Get(), calls)
	}
}

func TestWatchClosesWhenStoreCloses(t *testing.T) {
	store := New(0)
	updates := store.Watch(context.Background(), 1)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("watch still open")
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not close")
	}
}

func TestWatchRetainsLatestSnapshotWhenSlow(t *testing.T) {
	store := New(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := store.Watch(ctx, 1)
	for i := 1; i <= 4; i++ {
		_ = store.Set(i)
	}
	if got := <-updates; got.Value != 4 {
		t.Fatalf("value=%d want 4", got.Value)
	}
}
