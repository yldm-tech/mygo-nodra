// Package nodra provides small, concurrency-safe reactive stores for Go UI
// applications. Nodra is framework agnostic; a UI can subscribe and request
// its own redraw when a snapshot changes.
package nodra

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned when an operation targets a closed store.
var ErrClosed = errors.New("nodra: store is closed")

// Snapshot is an immutable view of a store at one version. Value is a copy of
// the value held by the store; reference fields still follow normal Go aliasing
// rules, so callers should use value types or copy reference fields in updates.
type Snapshot[T any] struct {
	Value   T
	Version uint64
}
type listener[T any] func(Snapshot[T])

// SubscribeOptions controls one subscription.
type SubscribeOptions[T any] struct {
	// Immediate calls the listener once with the current snapshot.
	Immediate bool
	// Equals suppresses notifications when the selected values are equivalent.
	// It is applied per subscription, not to the store's version counter.
	Equals func(previous, next T) bool
}

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
	closed    bool
	done      chan struct{}
}

// New creates a store containing initial.
func New[T any](initial T) *Store[T] {
	return &Store[T]{value: initial, listeners: make(map[uint64]listener[T]), done: make(chan struct{})}
}

// Snapshot returns the current state and version.
func (s *Store[T]) Snapshot() Snapshot[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot[T]{Value: s.value, Version: s.version}
}
func (s *Store[T]) Get() T          { return s.Snapshot().Value }
func (s *Store[T]) Version() uint64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.version }

// Set replaces the state and publishes one update, unless inside Batch.
func (s *Store[T]) Set(value T) error { return s.mutate(func() { s.value = value }) }

// Update mutates the state and publishes one update, unless inside Batch.
func (s *Store[T]) Update(fn func(*T)) error {
	return s.UpdateErr(func(value *T) error {
		if fn != nil {
			fn(value)
		}
		return nil
	})
}

// UpdateErr rolls back the value when fn returns an error. Rollback is a value
// assignment; reference fields should be copied by callers when necessary.
func (s *Store[T]) UpdateErr(fn func(*T) error) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	before := s.value
	var err error
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		err = fn(&s.value)
	}()
	if panicValue != nil {
		s.value = before
		s.mu.Unlock()
		panic(panicValue)
	}
	if err != nil {
		s.value = before
		s.mu.Unlock()
		return err
	}
	if s.batch > 0 {
		s.dirty = true
		s.mu.Unlock()
		return nil
	}
	s.version++
	snapshot := Snapshot[T]{Value: s.value, Version: s.version}
	listeners := s.listenersCopyLocked()
	s.mu.Unlock()
	notify(listeners, snapshot)
	return nil
}

func (s *Store[T]) mutate(mutator func()) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	mutator()
	if s.batch > 0 {
		s.dirty = true
		s.mu.Unlock()
		return nil
	}
	s.version++
	snapshot := Snapshot[T]{Value: s.value, Version: s.version}
	listeners := s.listenersCopyLocked()
	s.mu.Unlock()
	notify(listeners, snapshot)
	return nil
}

// Transaction serializes one fallible mutation with all other writes and
// publishes one snapshot on success. It has the same shallow rollback rule as
// UpdateErr: copy reference fields before mutating them.
func (s *Store[T]) Transaction(fn func(*T) error) error { return s.UpdateErr(fn) }

// Batch groups nested Set and Update calls into one notification.
func (s *Store[T]) Batch(fn func()) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.batch++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.batch--
		if s.batch != 0 || !s.dirty || s.closed {
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
	return nil
}

// Close permanently stops the store and removes its listeners.
func (s *Store[T]) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.closed = true
	close(s.done)
	s.listeners = nil
	return nil
}

// Subscribe registers fn and returns an idempotent unsubscribe function.
func (s *Store[T]) Subscribe(fn func(Snapshot[T])) func() {
	return s.SubscribeWith(SubscribeOptions[T]{}, fn)
}

// SubscribeWith registers a configured subscription.
func (s *Store[T]) SubscribeWith(options SubscribeOptions[T], fn func(Snapshot[T])) func() {
	if fn == nil {
		return func() {}
	}
	var subMu sync.Mutex
	var previous T
	hasPrevious := false
	wrapped := func(snapshot Snapshot[T]) {
		subMu.Lock()
		if hasPrevious && options.Equals != nil && options.Equals(previous, snapshot.Value) {
			subMu.Unlock()
			return
		}
		previous, hasPrevious = snapshot.Value, true
		subMu.Unlock()
		fn(snapshot)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return func() {}
	}
	s.nextID++
	id := s.nextID
	s.listeners[id] = wrapped
	initial := Snapshot[T]{Value: s.value, Version: s.version}
	s.mu.Unlock()
	if options.Immediate {
		wrapped(initial)
	}
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
// the selected comparable value changes.
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

// Watch returns a best effort stream of snapshots until ctx is canceled.
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
			select {
			case <-ch:
			default:
			}
			ch <- snapshot
		}
	})
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-s.done:
		}
		unsubscribe()
		mu.Lock()
		if !closed {
			closed = true
			close(ch)
		}
		mu.Unlock()
	}()
	return ch
}
