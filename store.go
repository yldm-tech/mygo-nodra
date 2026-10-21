// Package nodra provides concurrency-safe reactive state stores for Go UI
// applications. The core is framework agnostic; UI adapters subscribe and
// request their own redraw.
package nodra

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrClosed = errors.New("nodra: store is closed")

type Snapshot[T any] struct {
	Value   T
	Version uint64
}
type listener[T any] func(Snapshot[T])

type SubscribeOptions[T any] struct {
	Immediate bool
	Equals    func(previous, next T) bool
}

type Store[T any] struct {
	mu                sync.RWMutex
	value, initial    T
	version, nextID   uint64
	listeners         map[uint64]listener[T]
	mutationListeners map[uint64]func(Mutation[T])
	actionListeners   map[uint64]func(ActionEvent)
	actionHooks       map[uint64]ActionHooks
	id                string
	batch             int
	dirty             bool
	batchBefore       T
	batchType         MutationType
	batchName         string
	closed            bool
	done              chan struct{}
}

func New[T any](initial T) *Store[T] { return newStore("", initial) }
func newStore[T any](id string, initial T) *Store[T] {
	return &Store[T]{id: id, value: initial, initial: initial, listeners: map[uint64]listener[T]{}, mutationListeners: map[uint64]func(Mutation[T]){}, actionListeners: map[uint64]func(ActionEvent){}, actionHooks: map[uint64]ActionHooks{}, done: make(chan struct{})}
}
func (s *Store[T]) Snapshot() Snapshot[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot[T]{s.value, s.version}
}
func (s *Store[T]) Get() T          { return s.Snapshot().Value }
func (s *Store[T]) State() T        { return s.Get() }
func (s *Store[T]) Version() uint64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.version }

func (s *Store[T]) Set(value T) error {
	return s.mutate(func() { s.value = value }, MutationDirect, "")
}
func (s *Store[T]) Update(fn func(*T)) error {
	return s.UpdateErr(func(v *T) error {
		if fn != nil {
			fn(v)
		}
		return nil
	})
}
func (s *Store[T]) UpdateErr(fn func(*T) error) error   { return s.updateNamed("", MutationPatch, fn) }
func (s *Store[T]) Transaction(fn func(*T) error) error { return s.UpdateErr(fn) }
func (s *Store[T]) Reset() error                        { return s.mutate(func() { s.value = s.initial }, MutationReset, "") }
func (s *Store[T]) Patch(fn func(*T)) error             { return s.Batch(func() { _ = s.Update(fn) }) }

func (s *Store[T]) updateNamed(name string, kind MutationType, fn func(*T) error) error {
	if fn == nil {
		return errors.New("nodra: nil update")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	before := s.value
	var err error
	var panicValue any
	func() { defer func() { panicValue = recover() }(); err = fn(&s.value) }()
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
		if s.batchType == "" {
			s.batchType = kind
			s.batchName = name
		}
		s.mu.Unlock()
		return nil
	}
	s.version++
	snapshot, listeners, mutations := s.publishLocked(before, kind, name)
	s.mu.Unlock()
	notify(listeners, snapshot)
	notifyMutation(mutations, Mutation[T]{Type: kind, Name: name, Previous: before, Value: snapshot.Value, Version: snapshot.Version, At: time.Now()})
	return nil
}

func (s *Store[T]) mutate(mutator func(), kind MutationType, name string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	before := s.value
	mutator()
	if s.batch > 0 {
		s.dirty = true
		if s.batchType == "" {
			s.batchType = kind
			s.batchName = name
		}
		s.mu.Unlock()
		return nil
	}
	s.version++
	snapshot, listeners, mutations := s.publishLocked(before, kind, name)
	s.mu.Unlock()
	notify(listeners, snapshot)
	notifyMutation(mutations, Mutation[T]{Type: kind, Name: name, Previous: before, Value: snapshot.Value, Version: snapshot.Version, At: time.Now()})
	return nil
}

func (s *Store[T]) publishLocked(before T, kind MutationType, name string) (Snapshot[T], []listener[T], []func(Mutation[T])) {
	return Snapshot[T]{s.value, s.version}, s.listenersCopyLocked(), s.mutationListenersCopyLocked()
}

func (s *Store[T]) Batch(fn func()) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.batch == 0 {
		s.batchBefore = s.value
		s.batchType = ""
		s.batchName = ""
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
		before, kind, name := s.batchBefore, s.batchType, s.batchName
		s.dirty = false
		s.batchType = ""
		s.batchName = ""
		s.version++
		snapshot, listeners, mutations := s.publishLocked(before, kind, name)
		s.mu.Unlock()
		notify(listeners, snapshot)
		notifyMutation(mutations, Mutation[T]{Type: kind, Name: name, Previous: before, Value: snapshot.Value, Version: snapshot.Version, At: time.Now()})
	}()
	fn()
	return nil
}

func (s *Store[T]) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.closed = true
	close(s.done)
	s.listeners = nil
	s.mutationListeners = nil
	s.actionListeners = nil
	s.actionHooks = nil
	s.actionHooks = nil
	return nil
}
func (s *Store[T]) Subscribe(fn func(Snapshot[T])) func() {
	return s.SubscribeWith(SubscribeOptions[T]{}, fn)
}
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
	initial := Snapshot[T]{s.value, s.version}
	s.mu.Unlock()
	if options.Immediate {
		wrapped(initial)
	}
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); delete(s.listeners, id); s.mu.Unlock() }) }
}
func (s *Store[T]) SubscribeMutations(fn func(Mutation[T])) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return func() {}
	}
	s.nextID++
	id := s.nextID
	s.mutationListeners[id] = fn
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); delete(s.mutationListeners, id); s.mu.Unlock() }) }
}
func (s *Store[T]) listenersCopyLocked() []listener[T] {
	out := make([]listener[T], 0, len(s.listeners))
	for _, fn := range s.listeners {
		out = append(out, fn)
	}
	return out
}
func (s *Store[T]) mutationListenersCopyLocked() []func(Mutation[T]) {
	out := make([]func(Mutation[T]), 0, len(s.mutationListeners))
	for _, fn := range s.mutationListeners {
		out = append(out, fn)
	}
	return out
}
func notify[T any](listeners []listener[T], snapshot Snapshot[T]) {
	for _, fn := range listeners {
		fn(snapshot)
	}
}
func notifyMutation[T any](listeners []func(Mutation[T]), mutation Mutation[T]) {
	for _, fn := range listeners {
		fn(mutation)
	}
}

func SubscribeSelector[T any, S comparable](store *Store[T], selector func(T) S, fn func(S)) func() {
	return SubscribeSelectorWith(store, selector, func(current, _ S) { fn(current) }, SelectorOptions[S]{})
}

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
				{
				}
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
