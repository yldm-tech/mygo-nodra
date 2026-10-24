package nodra

import (
	"reflect"
	"sync"
)

// Observable is a versioned value source shared by stores, read-only views and derived values. Callbacks may overlap and must treat reference values as read-only. Implementations must use monotonically increasing versions and subscribe without replay; constructors catch up after subscribing.
type Observable[T any] interface {
	Snapshot() Snapshot[T]
	SubscribeSnapshot(func(Snapshot[T])) func()
}

func validObservable[T any](source Observable[T]) bool {
	if source == nil {
		return false
	}
	v := reflect.ValueOf(source)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return false
	}
	if reader, ok := source.(Reader[T]); ok {
		return sourceStore(reader) != nil
	}
	return true
}

func (s *Store[T]) SubscribeSnapshot(fn func(Snapshot[T])) func()   { return s.Subscribe(fn) }
func (r ReadOnly[T]) SubscribeSnapshot(fn func(Snapshot[T])) func() { return r.Subscribe(fn) }

// Snapshot returns the memoized value and its accepted source version. Derived reference values are read-only; equality-suppressed changes still advance the version without notifying.
func (d *Derived[T, V]) Snapshot() Snapshot[V] {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return Snapshot[V]{Value: d.value, Version: d.version}
}

func (d *Derived[T, V]) SubscribeSnapshot(fn func(Snapshot[V])) func() {
	if d == nil || fn == nil {
		return func() {}
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return func() {}
	}
	if d.snapshotListeners == nil {
		d.snapshotListeners = make(map[uint64]func(Snapshot[V]))
	}
	d.nextID++
	id := d.nextID
	d.snapshotListeners[id] = fn
	d.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { d.mu.Lock(); delete(d.snapshotListeners, id); d.mu.Unlock() }) }
}

// Pair contains the latest observed values of two independent sources.
type Pair[A, B any] struct {
	First  A
	Second B
}

// Combine derives a value from two stores or derived values. Inputs are eventually consistent, not an atomic cross-store snapshot; diamond graphs can publish intermediate values. Old source versions and obsolete computations cannot replace newer ones. Close the returned derived value to detach both sources. Projectors/comparators must be pure, concurrency-safe and keep source references immutable.
func Combine[A, B, V any](first Observable[A], second Observable[B], project func(A, B) V, equals func(V, V) bool) *Derived[Pair[A, B], V] {
	if !validObservable(first) || !validObservable(second) || project == nil {
		return &Derived[Pair[A, B], V]{closed: true}
	}
	a, b := first.Snapshot(), second.Snapshot()
	var mu sync.Mutex
	current := Snapshot[Pair[A, B]]{Value: Pair[A, B]{a.Value, b.Value}}
	snapshot := func() Snapshot[Pair[A, B]] {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	subscribe := func(fn func(Snapshot[Pair[A, B]])) (stop func()) {
		var stops []func()
		var once sync.Once
		stop = func() {
			once.Do(func() {
				for _, cancel := range stops {
					cancel()
				}
			})
		}
		ready := false
		defer func() {
			if !ready {
				stop()
			}
		}()
		acceptA := func(next Snapshot[A]) {
			mu.Lock()
			if next.Version <= a.Version {
				mu.Unlock()
				return
			}
			a = next
			current.Value.First, current.Version = next.Value, current.Version+1
			value := current
			mu.Unlock()
			fn(value)
		}
		acceptB := func(next Snapshot[B]) {
			mu.Lock()
			if next.Version <= b.Version {
				mu.Unlock()
				return
			}
			b = next
			current.Value.Second, current.Version = next.Value, current.Version+1
			value := current
			mu.Unlock()
			fn(value)
		}
		stops = append(stops, first.SubscribeSnapshot(acceptA))
		stops = append(stops, second.SubscribeSnapshot(acceptB))
		acceptA(first.Snapshot())
		acceptB(second.Snapshot())
		ready = true
		return stop
	}
	return derive(nil, snapshot, subscribe, func(pair Pair[A, B]) V { return project(pair.First, pair.Second) }, equals)
}
