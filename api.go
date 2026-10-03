package nodra

import (
	"errors"
	"sync"
	"time"
)

// Create is the Pinia/Zustand-style constructor name for a typed store.
func Create[T any](initial T) *Store[T] { return New(initial) }

// SelectorOptions configures a selector subscription.
type SelectorOptions[V comparable] struct {
	Immediate bool
	Equals    func(previous, next V) bool
}

// SubscribeSelectorWith calls fn with current and previous selected values.
func SubscribeSelectorWith[T any, V comparable](store *Store[T], selector func(T) V, fn func(current, previous V), options SelectorOptions[V]) func() {
	if store == nil || selector == nil || fn == nil {
		return func() {}
	}
	var mu sync.Mutex
	previous := selector(store.Get())
	first := true
	if options.Immediate {
		var zero V
		fn(previous, zero)
		first = false
	}
	return store.Subscribe(func(snapshot Snapshot[T]) {
		current := selector(snapshot.Value)
		mu.Lock()
		old := previous
		equal := old == current
		if options.Equals != nil {
			equal = options.Equals(old, current)
		}
		previous = current
		skip := first && !options.Immediate
		first = false
		mu.Unlock()
		if !equal && !skip {
			fn(current, old)
		}
	})
}

// Derived is a memoized getter derived from a store. It recomputes when the
// source publishes and only notifies listeners when the selected value changes.
type Derived[T any, V any] struct {
	source    *Store[T]
	selector  func(T) V
	equals    func(V, V) bool
	mu        sync.RWMutex
	value     V
	listeners map[uint64]func(V)
	nextID    uint64
	stop      func()
	closed    bool
}

func Derive[T any, V any](store *Store[T], selector func(T) V, equals func(V, V) bool) *Derived[T, V] {
	d := &Derived[T, V]{source: store, selector: selector, equals: equals, listeners: make(map[uint64]func(V))}
	if store == nil || selector == nil {
		d.closed = true
		return d
	}
	d.value = selector(store.Get())
	d.stop = store.Subscribe(func(snapshot Snapshot[T]) {
		next := selector(snapshot.Value)
		d.mu.Lock()
		if d.closed || (d.equals != nil && d.equals(d.value, next)) {
			d.mu.Unlock()
			return
		}
		d.value = next
		listeners := make([]func(V), 0, len(d.listeners))
		for _, fn := range d.listeners {
			listeners = append(listeners, fn)
		}
		d.mu.Unlock()
		for _, fn := range listeners {
			fn(next)
		}
	})
	return d
}
func (d *Derived[T, V]) Get() V { d.mu.RLock(); defer d.mu.RUnlock(); return d.value }
func (d *Derived[T, V]) Subscribe(fn func(V)) func() {
	if d == nil || fn == nil {
		return func() {}
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return func() {}
	}
	d.nextID++
	id := d.nextID
	d.listeners[id] = fn
	d.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { d.mu.Lock(); delete(d.listeners, id); d.mu.Unlock() }) }
}
func (d *Derived[T, V]) Close() error {
	if d == nil {
		return ErrClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	d.closed = true
	if d.stop != nil {
		d.stop()
	}
	d.listeners = nil
	return nil
}

// MutationType describes why a store published a snapshot.
type MutationType string

const (
	MutationDirect MutationType = "direct"
	MutationPatch  MutationType = "patch"
	MutationAction MutationType = "action"
	MutationReset  MutationType = "reset"
)

// Mutation contains Pinia-like subscription metadata.
type Mutation[T any] struct {
	Type     MutationType
	Name     string
	Previous T
	Value    T
	Version  uint64
	At       time.Time
}

// ActionEvent describes one completed action.
type ActionEvent struct {
	Name     string
	Started  time.Time
	Finished time.Time
	Duration time.Duration
	Err      error
}

func (s *Store[T]) SubscribeActions(fn func(ActionEvent)) func() {
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
	s.actionListeners[id] = fn
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); delete(s.actionListeners, id); s.mu.Unlock() }) }
}

// Do runs a named mutation and emits an action event after it completes.
func (s *Store[T]) Do(name string, fn func(*T) error) error {
	if fn == nil {
		return errors.New("nodra: nil action")
	}
	started := time.Now()
	err := s.updateNamed(name, MutationAction, fn)
	finished := time.Now()
	s.mu.RLock()
	listeners := make([]func(ActionEvent), 0, len(s.actionListeners))
	for _, listener := range s.actionListeners {
		listeners = append(listeners, listener)
	}
	s.mu.RUnlock()
	event := ActionEvent{Name: name, Started: started, Finished: finished, Duration: finished.Sub(started), Err: err}
	for _, listener := range listeners {
		listener(event)
	}
	return err
}

// Action returns a reusable named action function, matching Pinia's action
// definitions while keeping Go's explicit function types.
func (s *Store[T]) Action(name string, fn func(*T) error) func() error {
	return func() error { return s.Do(name, fn) }
}
