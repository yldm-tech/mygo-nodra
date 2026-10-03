// Package nodra provides small, concurrency-safe reactive stores for Go UI
// applications. Nodra is framework agnostic; a UI can subscribe and request
// its own redraw when a snapshot changes.
package nodra

import (
	"context"
	"sync"
)

// Snapshot is an immutable view of a store at one version. Value is a copy of
// the value held by the store; reference fields still follow normal Go aliasing
// rules, so callers should use value types or copy reference fields in updates.
type Snapshot[T any] struct {
	Value   T
	Version uint64
}

type listener[T any] func(Snapshot[T])

// Store owns application state of type T. Update and Set are safe from any
// goroutine. Subscribers run synchronously on the goroutine that performed the
// update, after the store has released its lock.
type Store[T any] struct {
	mu        sync.RWMutex
	value     T
	version   uint64
	nextID    uint64
	listeners map[uint64]listener[T]
	batch     int
	dirty     bool
}

// New creates a store containing initial.
func New[T any](initial T) *Store[T] {
	return &Store[T]{value: initial, listeners: make(map[uint64]listener[T])}
}

// Snapshot returns the current state and version.
func (s *Store[T]) Snapshot() Snapshot[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot[T]{Value: s.value, Version: s.version}
}

// Get returns the current state value.
func (s *Store[T]) Get() T { return s.Snapshot().Value }

// Version returns the number of published updates.
func (s *Store[T]) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Set replaces the state and publishes one update, unless inside Batch.
func (s *Store[T]) Set(value T) { s.mutate(func() { s.value = value }) }

// Update mutates the state and publishes one update, unless inside Batch.
func (s *Store[T]) Update(fn func(*T)) {
	if fn == nil {
		return
	}
	s.mutate(func() { fn(&s.value) })
}

func (s *Store[T]) mutate(mutator func()) {
	s.mu.Lock()
	mutator()
	if s.batch > 0 {
		s.dirty = true
		s.mu.Unlock()
		return
	}
	s.version++
	snapshot := Snapshot[T]{Value: s.value, Version: s.version}
	listeners := s.listenersCopyLocked()
	s.mu.Unlock()
	notify(listeners, snapshot)
}

// Batch groups nested Set and Update calls into one notification. The final
// state is published when fn returns. Nested batches are coalesced.
func (s *Store[T]) Batch(fn func()) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	s.batch++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.batch--
		if s.batch != 0 || !s.dirty {
			s.mu.Unlock()
			return
		}
		s.dirty = false
		s.version++
		snapshot := Snapshot[T]{Value: s.value, Version: s.version}
		listeners := s.listenersCopyLocked()
		s.mu.Unlock()
		notify(listeners, snapshot)
	}()
	fn()
}

// Subscribe registers fn and returns an idempotent unsubscribe function. The
// listener is not called for the initial value.
func (s *Store[T]) Subscribe(fn func(Snapshot[T])) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.listeners[id] = fn
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); delete(s.listeners, id); s.mu.Unlock() }) }
}

func (s *Store[T]) listenersCopyLocked() []listener[T] {
	out := make([]listener[T], 0, len(s.listeners))
	for _, fn := range s.listeners {
		out = append(out, fn)
	}
	return out
}

func notify[T any](listeners []listener[T], snapshot Snapshot[T]) {
	for _, fn := range listeners {
		fn(snapshot)
	}
}

// SubscribeSelector observes a projection of the store and calls fn only when
// the selected comparable value changes. It returns an idempotent unsubscribe.
func SubscribeSelector[T any, S comparable](store *Store[T], selectFn func(T) S, fn func(S)) func() {
	if store == nil || selectFn == nil || fn == nil {
		return func() {}
	}
	selected := selectFn(store.Get())
	var selectMu sync.Mutex
	return store.Subscribe(func(snapshot Snapshot[T]) {
		next := selectFn(snapshot.Value)
		selectMu.Lock()
		if next == selected {
			selectMu.Unlock()
			return
		}
		selected = next
		selectMu.Unlock()
		fn(next)
	})
}

// Watch returns a best-effort stream of snapshots until ctx is canceled. The
// channel is buffered; slow consumers may skip intermediate snapshots and will
// always receive a later state.
func (s *Store[T]) Watch(ctx context.Context, buffer int) <-chan Snapshot[T] {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Snapshot[T], buffer)
	var mu sync.Mutex
	closed := false
	unsubscribe := s.Subscribe(func(snapshot Snapshot[T]) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		select {
		case ch <- snapshot:
		default:
		}
	})
	go func() {
		<-ctx.Done()
		unsubscribe()
		mu.Lock()
		closed = true
		close(ch)
		mu.Unlock()
	}()
	return ch
}
