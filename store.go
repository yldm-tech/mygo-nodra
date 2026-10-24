// Package nodra is a state library for MyGo apps: concurrency-safe reactive stores whose snapshots drive native MyGo views. Invalidate redraws a window when its store publishes; the Store itself does not depend on a running MyGo app, so services and tests can use it as well.
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
type listener[T any] struct {
	fn       func(Snapshot[T])
	borrowed bool
}

type SubscribeOptions[T any] struct {
	Immediate bool
	Equals    func(previous, next T) bool
}

type Store[T any] struct {
	mu                sync.RWMutex
	value, initial    T
	clone             func(T) T
	version, nextID   uint64
	listeners         map[uint64]listener[T]
	mutationListeners map[uint64]func(Mutation[T])
	actionListeners   map[uint64]func(ActionEvent)
	actionHooks       map[uint64]ActionHooks
	id                string
	nextAction        uint64
	batch             int
	dirty             bool
	batchBefore       T
	batchType         MutationType
	batchName         string
	closed            bool
	done              chan struct{}
}

func New[T any](initial T) *Store[T] { return newStore("", initial) }

// NewWithClone isolates owned state, snapshots, subscriber values and rollback
// with clone. Clone must copy all mutable references, be safe for concurrent
// calls, and must not reenter the store. A nil clone preserves New's behavior.
func NewWithClone[T any](initial T, clone func(T) T) *Store[T] {
	return newStoreWithClone("", initial, clone)
}

func newStore[T any](id string, initial T) *Store[T] {
	return newStoreWithClone(id, initial, nil)
}
func newStoreWithClone[T any](id string, initial T, clone func(T) T) *Store[T] {
	s := &Store[T]{id: id, clone: clone, listeners: map[uint64]listener[T]{}, mutationListeners: map[uint64]func(Mutation[T]){}, actionListeners: map[uint64]func(ActionEvent){}, actionHooks: map[uint64]ActionHooks{}, done: make(chan struct{})}
	s.value, s.initial = s.copy(initial), s.copy(initial)
	return s
}
func (s *Store[T]) copy(value T) T {
	if s.clone != nil {
		return s.clone(value)
	}
	return value
}
func (s *Store[T]) Snapshot() Snapshot[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot[T]{s.copy(s.value), s.version}
}
func (s *Store[T]) Get() T          { return s.Snapshot().Value }
func (s *Store[T]) State() T        { return s.Get() }
func (s *Store[T]) Version() uint64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.version }

func (s *Store[T]) Set(value T) error {
	return s.updateNamed("", MutationDirect, func(v *T) error { *v = value; return nil })
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
func (s *Store[T]) Reset() error {
	return s.updateNamed("", MutationReset, func(v *T) error { *v = s.initial; return nil })
}
func (s *Store[T]) Patch(fn func(*T)) error { return s.Batch(func() { _ = s.Update(fn) }) }

func (s *Store[T]) updateNamed(name string, kind MutationType, fn func(*T) error) error {
	return s.updateCommitted(name, kind, fn, nil)
}

// updateCommitted keeps internal bookkeeping in the same commit as the source
// version. committed runs under the store lock and must not invoke user code.
func (s *Store[T]) updateCommitted(name string, kind MutationType, fn func(*T) error, committed func(uint64)) error {
	if fn == nil {
		return errors.New("nodra: nil update")
	}
	var before T
	var snapshot Snapshot[T]
	var listeners []listener[T]
	var mutations []func(Mutation[T])
	published := false
	var err error
	s.mu.Lock()
	func() {
		// Release the lock on update or clone panics without publishing a
		// partially committed value. The candidate is not the owned state.
		defer s.mu.Unlock()
		if s.closed {
			err = ErrClosed
			return
		}
		before = s.value
		next := s.copy(before)
		if err = fn(&next); err != nil {
			return
		}
		// Copy on commit too: a callback may retain its candidate or a caller
		// may retain references passed to Set.
		s.value = s.copy(next)
		if s.batch > 0 {
			s.dirty = true
			if s.batchType == "" {
				s.batchType = kind
				s.batchName = name
			}
			return
		}
		s.version++
		if committed != nil {
			committed(s.version)
		}
		snapshot, listeners, mutations = s.publishLocked(before, kind, name)
		published = true
	}()
	if published {
		s.notify(listeners, snapshot)
		s.notifyMutation(mutations, Mutation[T]{Type: kind, Name: name, Previous: before, Value: snapshot.Value, Version: snapshot.Version, At: time.Now()})
	}
	return err
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
		s.notify(listeners, snapshot)
		s.notifyMutation(mutations, Mutation[T]{Type: kind, Name: name, Previous: before, Value: snapshot.Value, Version: snapshot.Version, At: time.Now()})
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
	var version uint64
	hasVersion := false
	wrapped := func(snapshot Snapshot[T]) {
		for {
			subMu.Lock()
			if hasVersion && snapshot.Version <= version {
				subMu.Unlock()
				return
			}
			old, hadPrevious, comparedVersion := previous, hasPrevious, version
			subMu.Unlock()
			equal := hadPrevious && options.Equals != nil && options.Equals(old, snapshot.Value)
			retry, deliver := func() (bool, bool) {
				subMu.Lock()
				defer subMu.Unlock()
				if hasVersion && snapshot.Version <= version {
					return false, false
				}
				if version != comparedVersion || hasPrevious != hadPrevious {
					return true, false
				}
				if !equal && options.Equals != nil {
					previous, hasPrevious = s.copy(snapshot.Value), true
				}
				version, hasVersion = snapshot.Version, true
				return false, !equal
			}()
			if retry {
				continue
			}
			if deliver {
				fn(snapshot)
			}
			return
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return func() {}
	}
	s.nextID++
	id := s.nextID
	s.listeners[id] = listener[T]{fn: wrapped}
	initial := Snapshot[T]{s.value, s.version}
	s.mu.Unlock()
	var once sync.Once
	stop := func() { once.Do(func() { s.mu.Lock(); delete(s.listeners, id); s.mu.Unlock() }) }
	if options.Immediate {
		initialized := false
		defer func() {
			if !initialized {
				stop()
			}
		}()
		wrapped(Snapshot[T]{s.copy(initial.Value), initial.Version})
		initialized = true
	}
	return stop
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
func (s *Store[T]) notify(listeners []listener[T], snapshot Snapshot[T]) {
	for _, sub := range listeners {
		value := snapshot.Value
		if !sub.borrowed {
			value = s.copy(value)
		}
		sub.fn(Snapshot[T]{value, snapshot.Version})
	}
}
func (s *Store[T]) notifyMutation(listeners []func(Mutation[T]), mutation Mutation[T]) {
	for _, fn := range listeners {
		event := mutation
		event.Previous, event.Value = s.copy(mutation.Previous), s.copy(mutation.Value)
		fn(event)
	}
}

func SubscribeSelector[T any, S any](store *Store[T], selector func(T) S, fn func(S)) func() {
	if fn == nil {
		return func() {}
	}
	return SubscribeSelectorWith(store, selector, func(current, _ S) { fn(current) }, SelectorOptions[S]{})
}

func (s *Store[T]) Watch(ctx context.Context, buffer int) <-chan Snapshot[T] {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Snapshot[T], buffer)
	var mu sync.Mutex
	closed := false
	var version uint64
	hasVersion := false
	unsubscribe := s.Subscribe(func(snapshot Snapshot[T]) {
		mu.Lock()
		defer mu.Unlock()
		if closed || (hasVersion && snapshot.Version <= version) {
			return
		}
		version, hasVersion = snapshot.Version, true
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
