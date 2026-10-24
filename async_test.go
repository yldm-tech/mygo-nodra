package nodra

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type asyncStorage struct {
	mu       sync.Mutex
	values   []int
	calls    int
	failures int
	err      error
	gate     <-chan struct{}
	entered  chan int
}

func (s *asyncStorage) Load(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.values) == 0 {
		return nil, errors.New("empty storage")
	}
	return json.Marshal(s.values[len(s.values)-1])
}
func (s *asyncStorage) Save(ctx context.Context, data []byte) error {
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if s.entered != nil {
		s.entered <- value
	}
	if s.gate != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.gate:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failures > 0 {
		s.failures--
		return s.err
	}
	s.values = append(s.values, value)
	return nil
}
func (s *asyncStorage) result() ([]int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.values...), s.calls
}

func TestAsyncDebounceAndFlushCoalesceWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, storage := New(0), &asyncStorage{}
		writer, err := NewPersistence(store, storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{Debounce: time.Hour})
		requireNoError(t, err)
		for i := 1; i <= 100; i++ {
			requireNoError(t, store.Set(i))
		}
		synctest.Wait()
		if _, calls := storage.result(); calls != 0 {
			t.Fatal("debounce wrote before its deadline")
		}
		requireNoError(t, writer.Flush(context.Background()))
		if values, _ := storage.result(); !reflect.DeepEqual(values, []int{100}) {
			t.Fatalf("flush writes = %v", values)
		}
		requireNoError(t, store.Set(101))
		synctest.Wait()
		time.Sleep(30 * time.Minute)
		requireNoError(t, store.Set(102))
		synctest.Wait()
		time.Sleep(30 * time.Minute)
		if _, calls := storage.result(); calls != 1 {
			t.Fatal("debounce was not reset by the latest update")
		}
		time.Sleep(30 * time.Minute)
		synctest.Wait()
		requireNoError(t, writer.Close(context.Background()))
		if values, _ := storage.result(); !reflect.DeepEqual(values, []int{100, 102}) {
			t.Fatalf("writes = %v", values)
		}
	})
}

func TestAsyncWritersStayResponsiveAndFlushWaitsForLatest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		storage := &asyncStorage{gate: gate, entered: make(chan int, 10)}
		store := New(0)
		writer, err := NewPersistence(store, storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{})
		requireNoError(t, err)
		if got := <-storage.entered; got != 0 {
			t.Fatalf("initial write = %d", got)
		}
		// These synchronous updates must finish while storage remains blocked.
		for i := 1; i <= 100; i++ {
			requireNoError(t, store.Set(i))
		}
		flushed := make(chan error, 1)
		go func() { flushed <- writer.Flush(context.Background()) }()
		synctest.Wait()
		select {
		case err := <-flushed:
			t.Fatalf("flush returned before storage completed: %v", err)
		default:
		}
		close(gate)
		requireNoError(t, <-flushed)
		requireNoError(t, writer.Close(context.Background()))
		if values, _ := storage.result(); !reflect.DeepEqual(values, []int{0, 100}) {
			t.Fatalf("writes during blocked storage = %v", values)
		}
	})
}

func TestAsyncFailureIsObservableAndFlushRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		want := errors.New("disk full")
		storage := &asyncStorage{failures: 2, err: want}
		writer, err := NewPersistence(New(7), storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{})
		requireNoError(t, err)
		synctest.Wait()
		if err := <-writer.Errors(); !errors.Is(err, want) {
			t.Fatalf("reported error = %v", err)
		}
		if err := writer.Flush(context.Background()); !errors.Is(err, want) {
			t.Fatalf("flush error = %v", err)
		}
		synctest.Wait()
		if _, calls := storage.result(); calls != 2 {
			t.Fatalf("failed writes retried without a request: calls = %d", calls)
		}
		requireNoError(t, writer.Flush(context.Background()))
		requireNoError(t, writer.Close(context.Background()))
		if values, calls := storage.result(); !reflect.DeepEqual(values, []int{7}) || calls != 3 {
			t.Fatalf("retry result = %v after %d calls", values, calls)
		}
	})
}

func TestAsyncFlushDeadlineDoesNotCancelWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		storage := &asyncStorage{gate: gate, entered: make(chan int, 10)}
		store := New(0)
		writer, err := NewPersistence(store, storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{})
		requireNoError(t, err)
		<-storage.entered
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := writer.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("flush error = %v", err)
		}
		requireNoError(t, store.Set(1))
		close(gate)
		requireNoError(t, writer.Close(context.Background()))
		if values, _ := storage.result(); !reflect.DeepEqual(values, []int{0, 1}) {
			t.Fatalf("worker was canceled by one flush: %v", values)
		}
	})
}

func TestAsyncCloseFailureAndDeadlineReleaseWorker(t *testing.T) {
	for _, reason := range []string{"failure", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				want := errors.New("disk full")
				storage := &asyncStorage{failures: 1, err: want}
				ctx := context.Background()
				cancel := func() {}
				if reason == "deadline" {
					storage.gate = make(chan struct{})
					want = context.DeadlineExceeded
					ctx, cancel = context.WithTimeout(ctx, time.Second)
				}
				defer cancel()
				store := New(3)
				writer, err := NewPersistence(store, storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{Debounce: time.Hour})
				requireNoError(t, err)
				if err := writer.Close(ctx); !errors.Is(err, want) {
					t.Fatalf("close error = %v, want %v", err, want)
				}
				synctest.Wait()
				for range writer.Errors() {
				}
				if err := writer.Flush(context.Background()); !errors.Is(err, ErrClosed) {
					t.Fatalf("flush after close = %v", err)
				}
				requireNoError(t, store.Set(4))
				if err := writer.Close(context.Background()); !errors.Is(err, want) {
					t.Fatalf("idempotent close error = %v", err)
				}
			})
		})
	}
}

func TestAsyncParentAndStoreShutdownReleaseSubscriptions(t *testing.T) {
	for _, reason := range []string{"context", "store"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := New(0)
				writer, err := NewPersistence(store, &asyncStorage{}, JSONCodec[int]{}).AutoSaveAsync(ctx, AsyncOptions{Debounce: time.Hour})
				requireNoError(t, err)
				want := context.Canceled
				if reason == "store" {
					want = ErrClosed
					requireNoError(t, store.Close())
				} else {
					cancel()
				}
				synctest.Wait()
				for range writer.Errors() {
				}
				if err := writer.Flush(context.Background()); !errors.Is(err, want) {
					t.Fatalf("flush after shutdown = %v", err)
				}
				if count := listenerCount(store); count != 0 {
					t.Fatalf("remaining subscriptions = %d", count)
				}
			})
		})
	}
}

func TestAsyncConcurrentFlushAndCloseAreIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, storage := New(0), &asyncStorage{}
		writer, err := NewPersistence(store, storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{Debounce: time.Hour})
		requireNoError(t, err)
		requireNoError(t, store.Set(9))
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				if err := writer.Flush(context.Background()); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		for range 10 {
			wg.Go(func() {
				if err := writer.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if values, _ := storage.result(); !reflect.DeepEqual(values, []int{9}) {
			t.Fatalf("concurrent flush/close writes = %v", values)
		}
	})
}

func TestAsyncCloseOverlapsAnExistingFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		storage := &asyncStorage{gate: gate, entered: make(chan int, 10)}
		writer, err := NewPersistence(New(5), storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{})
		requireNoError(t, err)
		<-storage.entered
		flushed, closed := make(chan error, 1), make(chan error, 1)
		go func() { flushed <- writer.Flush(context.Background()) }()
		synctest.Wait()
		go func() { closed <- writer.Close(context.Background()) }()
		synctest.Wait()
		close(gate)
		requireNoError(t, <-flushed)
		requireNoError(t, <-closed)
	})
}

func TestAsyncErrorStreamDoesNotBlockShutdownWhenFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		want := errors.New("unwritable storage")
		storage := &asyncStorage{failures: 100, err: want}
		writer, err := NewPersistence(New(0), storage, JSONCodec[int]{}).AutoSaveAsync(context.Background(), AsyncOptions{Debounce: time.Hour})
		requireNoError(t, err)
		for range 20 {
			if err := writer.Flush(context.Background()); !errors.Is(err, want) {
				t.Fatalf("flush error = %v", err)
			}
		}
		if err := writer.Close(context.Background()); !errors.Is(err, want) {
			t.Fatalf("close error = %v", err)
		}
		for range writer.Errors() {
		}
	})
}

func TestAsyncClonedVersionedStateRoundTripsThroughFile(t *testing.T) {
	store := NewWithClone(map[string]int{"count": 0}, func(v map[string]int) map[string]int { return maps.Clone(v) })
	storage := FileStorage{Path: filepath.Join(t.TempDir(), "state.json")}
	codec := VersionedJSONCodec[map[string]int]{Version: 1}
	p := NewPersistence(store, storage, codec)
	writer, err := p.AutoSaveAsync(context.Background(), AsyncOptions{})
	requireNoError(t, err)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				if err := store.Update(func(v *map[string]int) { (*v)["count"]++ }); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requireNoError(t, writer.Close(ctx))
	restored := New(map[string]int{})
	requireNoError(t, NewPersistence(restored, storage, codec).Load(ctx))
	if count := restored.Get()["count"]; count != 400 {
		t.Fatalf("restored count = %d, want 400", count)
	}
}

func TestAsyncRejectsInvalidConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	valid := NewPersistence(New(0), &asyncStorage{}, JSONCodec[int]{})
	closed := New(0)
	requireNoError(t, closed.Close())
	for _, test := range []struct {
		name    string
		p       *Persistence[int]
		ctx     context.Context
		options AsyncOptions
		want    error
	}{
		{"nil", nil, nil, AsyncOptions{}, ErrInvalidPersistence},
		{"codec", NewPersistence[int](New(0), &asyncStorage{}, nil), nil, AsyncOptions{}, ErrInvalidPersistence},
		{"debounce", valid, nil, AsyncOptions{Debounce: -1}, ErrInvalidPersistence},
		{"canceled", valid, ctx, AsyncOptions{}, context.Canceled},
		{"closed", NewPersistence(closed, &asyncStorage{}, JSONCodec[int]{}), nil, AsyncOptions{}, ErrClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer, err := test.p.AutoSaveAsync(test.ctx, test.options)
			if writer != nil || !errors.Is(err, test.want) {
				t.Fatalf("writer, error = %v, %v", writer, err)
			}
		})
	}
}
