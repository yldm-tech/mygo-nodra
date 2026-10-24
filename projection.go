package nodra

import "sync"

// Select reads an owned projection and source version without cloning the whole Store. project borrows read-only state under the read lock: it must not mutate or retain source references, reenter the Store, or block. Return scalars or copy all mutable references in the projection (for example with slices.Clone). Existing Get and SubscribeSelector keep their full-state isolation policy.
func Select[T, V any](store *Store[T], project func(T) V) Snapshot[V] {
	if store == nil || project == nil {
		return Snapshot[V]{}
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return Snapshot[V]{Value: project(store.value), Version: store.version}
}

// SubscribeProjectionWith is an opt-in selector that borrows immutable source snapshots instead of cloning the entire Store per notification. project must be pure and concurrency-safe, must not retain or mutate source references, and must return independently owned projections. Callbacks must treat the current/previous projections as immutable. Equality, initialization and callback ordering match SubscribeSelectorWith. On default stores, source reference values must remain immutable as with any retained snapshot.
func SubscribeProjectionWith[T, V any](store *Store[T], project func(T) V, fn func(current, previous V), options SelectorOptions[V]) func() {
	if store == nil || project == nil || fn == nil {
		return func() {}
	}
	read := func() Snapshot[T] {
		store.mu.RLock()
		defer store.mu.RUnlock()
		return Snapshot[T]{Value: store.value, Version: store.version}
	}
	subscribe := func(fn func(Snapshot[T])) func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.closed {
			return func() {}
		}
		store.nextID++
		id := store.nextID
		store.listeners[id] = listener[T]{fn: fn, borrowed: true}
		var once sync.Once
		return func() { once.Do(func() { store.mu.Lock(); delete(store.listeners, id); store.mu.Unlock() }) }
	}
	return subscribeSelection(read, subscribe, project, fn, options)
}

func SubscribeProjection[T, V any](store *Store[T], project func(T) V, fn func(V)) func() {
	if fn == nil {
		return func() {}
	}
	return SubscribeProjectionWith(store, project, func(current, _ V) { fn(current) }, SelectorOptions[V]{})
}
