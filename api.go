package nodra

import (
	"errors"
	"reflect"
	"sync"
	"time"
)

// Create is the Pinia/Zustand-style constructor name for a typed store.
func Create[T any](initial T) *Store[T] { return New(initial) }

// SelectorOptions configures a selector subscription. With no Equals function, comparable values use == (including pointer identity); other values use reflect.DeepEqual. A custom comparator can use slices.Equal or maps.Equal.
type SelectorOptions[V any] struct {
	Immediate bool
	Equals    func(previous, next V) bool
}

// SubscribeSelectorWith calls fn with current and previous selected values.
// It catches up after registration and ignores obsolete computations. Callbacks
// run on writer goroutines and may overlap, as with Store.Subscribe.
func SubscribeSelectorWith[T any, V any](store *Store[T], selector func(T) V, fn func(current, previous V), options SelectorOptions[V]) func() {
	if store == nil || selector == nil || fn == nil {
		return func() {}
	}
	compare := options.Equals
	if compare == nil {
		compare = selectorEqual[V]
	}
	var mu sync.Mutex
	initial := store.Snapshot()
	previous := selector(initial.Value)
	version := initial.Version
	if options.Immediate {
		var zero V
		fn(previous, zero)
	}
	changed := func(snapshot Snapshot[T]) {
		mu.Lock()
		stale := snapshot.Version <= version
		mu.Unlock()
		if stale {
			return
		}
		current := selector(snapshot.Value)
		for {
			mu.Lock()
			if snapshot.Version <= version {
				mu.Unlock()
				return
			}
			old, comparedVersion := previous, version
			mu.Unlock()
			// Comparators may panic or read the source without holding our lock.
			equal := compare(old, current)
			mu.Lock()
			if snapshot.Version <= version {
				mu.Unlock()
				return
			}
			if version != comparedVersion {
				mu.Unlock()
				continue // Compare again against the newer baseline.
			}
			previous, version = current, snapshot.Version
			mu.Unlock()
			if !equal {
				fn(current, old)
			}
			return
		}
	}
	stop := store.Subscribe(changed)
	initialized := false
	defer func() {
		if !initialized {
			stop()
		}
	}()
	// Catch changes between reading the initial value and registering. The
	// version check also avoids recomputing when a callback already caught up.
	changed(store.Snapshot())
	initialized = true
	return stop
}

// selectorEqual preserves == semantics when dynamic values are comparable too. Interfaces and structs containing interfaces can hold slices/maps at runtime.
func selectorEqual[V any](previous, next V) bool {
	a, b := reflect.ValueOf(previous), reflect.ValueOf(next)
	if (!a.IsValid() || a.Comparable()) && (!b.IsValid() || b.Comparable()) {
		return any(previous) == any(next)
	}
	return reflect.DeepEqual(previous, next)
}

// Derived is a memoized getter derived from a store. It recomputes when the
// source publishes and only notifies listeners when the selected value changes.
type Derived[T any, V any] struct {
	source    *Store[T]
	selector  func(T) V
	equals    func(V, V) bool
	mu        sync.RWMutex
	value     V
	version   uint64
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
	initial := store.Snapshot()
	d.value = selector(initial.Value)
	d.version = initial.Version
	changed := func(snapshot Snapshot[T]) {
		d.mu.RLock()
		stale := d.closed || snapshot.Version <= d.version
		d.mu.RUnlock()
		if stale {
			return
		}
		next := selector(snapshot.Value)
		for {
			d.mu.RLock()
			if d.closed || snapshot.Version <= d.version {
				d.mu.RUnlock()
				return
			}
			old, comparedVersion := d.value, d.version
			d.mu.RUnlock()
			equal := d.equals != nil && d.equals(old, next)
			d.mu.Lock()
			if d.closed || snapshot.Version <= d.version {
				d.mu.Unlock()
				return
			}
			if d.version != comparedVersion {
				d.mu.Unlock()
				continue
			}
			// Advance even when equal, but only after comparison succeeds.
			d.version = snapshot.Version
			if equal {
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
			return
		}
	}
	d.stop = store.Subscribe(changed)
	initialized := false
	defer func() {
		if !initialized {
			d.stop()
		}
	}()
	changed(store.Snapshot())
	initialized = true
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
	StoreID  string
	Name     string
	Started  time.Time
	Finished time.Time
	Duration time.Duration
	Err      error
}

// ActionHooks observes action lifecycle events. Before runs before the
// mutation; After runs only on success; Error runs only on failure.
type ActionHooks struct {
	Before func(ActionEvent)
	After  func(ActionEvent)
	Error  func(ActionEvent)
}

// SubscribeActionHooks registers lifecycle hooks and returns an unsubscribe function.
func (s *Store[T]) SubscribeActionHooks(hooks ActionHooks) func() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return func() {}
	}
	s.nextID++
	id := s.nextID
	s.actionHooks[id] = hooks
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); delete(s.actionHooks, id); s.mu.Unlock() }) }
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
	event := ActionEvent{StoreID: s.ID(), Name: name, Started: started}
	s.mu.RLock()
	hooks := make([]ActionHooks, 0, len(s.actionHooks))
	for _, hook := range s.actionHooks {
		hooks = append(hooks, hook)
	}
	s.mu.RUnlock()
	for _, hook := range hooks {
		if hook.Before != nil {
			hook.Before(event)
		}
	}
	err := s.updateNamed(name, MutationAction, fn)
	finished := time.Now()
	event.Finished = finished
	event.Duration = finished.Sub(started)
	event.Err = err
	s.mu.RLock()
	listeners := make([]func(ActionEvent), 0, len(s.actionListeners))
	for _, listener := range s.actionListeners {
		listeners = append(listeners, listener)
	}
	s.mu.RUnlock()
	for _, listener := range listeners {
		listener(event)
	}
	for _, hook := range hooks {
		if err == nil {
			if hook.After != nil {
				hook.After(event)
			}
		} else if hook.Error != nil {
			hook.Error(event)
		}
	}
	return err
}

// Action returns a reusable named action function, matching Pinia action
// definitions while keeping Go's explicit function types.
func (s *Store[T]) Action(name string, fn func(*T) error) func() error {
	return func() error { return s.Do(name, fn) }
}
