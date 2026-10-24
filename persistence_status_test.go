package nodra

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestPersistenceStatusPendingSavingDurableAndClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		storage := &asyncStorage{gate: gate, entered: make(chan int, 4)}
		store := New(0)
		writer, err := NewPersistence(store, storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{Debounce: time.Second})
		requireNoError(t, err)
		if state := writer.Status().Get(); !state.Pending || state.HasSaved || state.Saving {
			t.Fatalf("initial: %+v", state)
		}
		var mu sync.Mutex
		var observed []PersistenceState
		stop := writer.Status().Subscribe(func(s Snapshot[PersistenceState]) {
			// Reading both source and status from a callback must not deadlock.
			_ = writer.Status().Get()
			_ = store.Get()
			mu.Lock()
			observed = append(observed, s.Value)
			mu.Unlock()
		})
		defer stop()
		requireNoError(t, store.Set(1))
		time.Sleep(time.Second)
		<-storage.entered
		if state := writer.Status().Get(); !state.Saving || !state.Pending || state.RequestedVersion != 1 || state.HasSaved {
			t.Fatalf("saving: %+v", state)
		}
		requireNoError(t, store.Set(2))
		close(gate)
		requireNoError(t, writer.Close(context.Background()))
		state := writer.Status().Get()
		if !state.Closed || state.Pending || state.Saving || !state.HasSaved || state.SavedVersion != 2 || state.RequestedVersion != 2 {
			t.Fatalf("durable closed: %+v", state)
		}
		select {
		case <-writer.Status().Done():
		default:
			t.Fatal("status not closed")
		}
		mu.Lock()
		defer mu.Unlock()
		for n := 1; n < len(observed); n++ {
			if observed[n].Revision <= observed[n-1].Revision || observed[n].SavedVersion < observed[n-1].SavedVersion {
				t.Fatalf("status regressed: %+v", observed)
			}
		}
	})
}

func TestPersistenceStatusFailureRetryAndObserverPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		want := errors.New("disk full")
		storage := &asyncStorage{failures: 1, err: want}
		writer, err := NewPersistence(New(0), storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{Debounce: time.Hour})
		requireNoError(t, err)
		stop := writer.Status().Subscribe(func(Snapshot[PersistenceState]) { panic("observer") })
		defer stop()
		if err := writer.Flush(context.Background()); !errors.Is(err, want) {
			t.Fatalf("failure: %v", err)
		}
		synctest.Wait()
		if state := writer.Status().Get(); !state.Pending || state.HasSaved || state.Error != want.Error() || state.Saving {
			t.Fatalf("failed: %+v", state)
		}
		requireNoError(t, writer.Flush(context.Background()))
		requireNoError(t, writer.Close(context.Background()))
		if state := writer.Status().Get(); !state.Closed || state.Pending || state.Error != "" || !state.HasSaved || state.SavedVersion != 0 {
			t.Fatalf("retried version zero: %+v", state)
		}
	})
}

func TestPersistenceStatusCanceledWithPendingWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		store := New(0)
		writer, err := NewPersistence(store, &asyncStorage{}, JSONCodec[int]{}).AutoSaveAsync(ctx, AsyncOptions{Debounce: time.Hour})
		requireNoError(t, err)
		requireNoError(t, store.Set(1))
		cancel()
		<-writer.done
		state := writer.Status().Get()
		if !state.Closed || !state.Pending || state.HasSaved || state.RequestedVersion != 1 {
			t.Fatalf("canceled: %+v", state)
		}
	})
}
