package nodra

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type memoryStorage struct {
	data []byte
	err  error
}

func (m *memoryStorage) Load(context.Context) ([]byte, error) {
	return append([]byte(nil), m.data...), m.err
}
func (m *memoryStorage) Save(_ context.Context, data []byte) error {
	if m.err != nil {
		return m.err
	}
	m.data = append([]byte(nil), data...)
	return nil
}

type jsonCodec[T any] struct{}

func (jsonCodec[T]) Encode(value T) ([]byte, error) { return json.Marshal(value) }
func (jsonCodec[T]) Decode(data []byte) (T, error) {
	var value T
	err := json.Unmarshal(data, &value)
	return value, err
}

func TestPersistenceLoadSave(t *testing.T) {
	storage := &memoryStorage{}
	store := New(map[string]int{"count": 1})
	p := NewPersistence(store, storage, jsonCodec[map[string]int]{})
	if err := p.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireNoError(t, store.Set(map[string]int{"count": 9}))
	if err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.Get()["count"] != 1 {
		t.Fatalf("restored = %#v", store.Get())
	}
	storage.err = errors.New("disk failed")
	if err := p.Save(context.Background()); !errors.Is(err, storage.err) {
		t.Fatalf("save error = %v", err)
	}
}

func TestAutoSaveWithErrorsReportsAndStops(t *testing.T) {
	storage := &memoryStorage{err: errors.New("disk failed")}
	store := New(0)
	p := NewPersistence(store, storage, jsonCodec[int]{})
	errs, stop := p.AutoSaveWithErrors(context.Background())
	requireNoError(t, store.Set(1))
	select {
	case err := <-errs:
		if !errors.Is(err, storage.err) {
			t.Fatalf("error = %v", err)
		}
	default:
		t.Fatal("expected persistence error")
	}
	stop()
	requireNoError(t, store.Set(2))
}

// gateCodec pauses the first encoding, before the storage write begins.
type gateCodec struct {
	entered chan struct{}
	release chan struct{}
}

func (c gateCodec) Encode(value int) ([]byte, error) {
	if value == 1 {
		close(c.entered)
		<-c.release
	}
	return json.Marshal(value)
}
func (gateCodec) Decode(data []byte) (int, error) {
	var value int
	err := json.Unmarshal(data, &value)
	return value, err
}

type lockedStorage struct {
	mu   sync.Mutex
	data []byte
}

func (s *lockedStorage) Load(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...), nil
}
func (s *lockedStorage) Save(_ context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append([]byte(nil), data...)
	return nil
}

func TestPersistenceConcurrentSavesKeepLatestState(t *testing.T) {
	store := New(1)
	codec := gateCodec{entered: make(chan struct{}), release: make(chan struct{})}
	storage := &lockedStorage{}
	p := NewPersistence(store, storage, codec)
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- p.Save(context.Background()) }()
	<-codec.entered
	requireNoError(t, store.Set(2))
	go func() { second <- p.Save(context.Background()) }()
	// Give a competing save an opportunity to finish. With serialization it
	// waits behind the blocked encode; without it the old write wins last.
	var secondErr error
	secondFinished := false
	select {
	case secondErr = <-second:
		secondFinished = true
	case <-time.After(50 * time.Millisecond):
	}
	// A queued save must read state after acquiring its serialization lock.
	requireNoError(t, store.Set(3))
	close(codec.release)
	requireNoError(t, <-first)
	if !secondFinished {
		secondErr = <-second
	}
	requireNoError(t, secondErr)
	data, err := storage.Load(context.Background())
	requireNoError(t, err)
	if string(data) != "3" {
		t.Fatalf("persisted = %s, want 3", data)
	}
}

func TestAutoSaveWithErrorsReleasesWatcher(t *testing.T) {
	for _, shutdown := range []string{"stop", "cancel", "close"} {
		t.Run(shutdown, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := New(0)
				ctx := context.Background()
				cancel := func() {}
				if shutdown == "cancel" {
					ctx, cancel = context.WithCancel(ctx)
				}
				defer cancel()
				p := NewPersistence(store, &memoryStorage{}, jsonCodec[int]{})
				errs, stop := p.AutoSaveWithErrors(ctx)
				switch shutdown {
				case "stop":
					stop()
				case "cancel":
					cancel()
				case "close":
					requireNoError(t, store.Close())
				}
				synctest.Wait()
				select {
				case _, ok := <-errs:
					if ok {
						t.Fatal("error channel is still open")
					}
				default:
					t.Fatal("error channel is still open")
				}
				stop()
				// synctest also requires every goroutine to exit when this
				// function returns, including the background stop watcher.
			})
		})
	}
}
