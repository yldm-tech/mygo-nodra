package nodra

import (
	"sync"

	"github.com/egoist/mygo"
)

// InvalidateProjection requests frames only when an owned projection changes, without cloning the entire source. The projector and callbacks follow SubscribeProjection's ownership rules. The window owns the subscription lifetime.
func InvalidateProjection[T, V any](source Reader[T], window *mygo.Window, project func(T) V) func() {
	return InvalidateProjectionWith(source, window, project, SelectorOptions[V]{})
}

func InvalidateProjectionWith[T, V any](source Reader[T], window *mygo.Window, project func(T) V, options SelectorOptions[V]) func() {
	store := sourceStore(source)
	if store == nil || window == nil || project == nil {
		return func() {}
	}
	return bindInvalidation(window, func(redraw func()) func() {
		return SubscribeProjectionWith(store, project, func(_, _ V) { redraw() }, options)
	})
}

// Invalidate requests a new frame of window's native Content whenever store publishes a snapshot. The subscription stops when the window closes; the returned function stops it earlier and is safe to call repeatedly. Nil inputs and destroyed windows subscribe to nothing. A callback already running when stopped may still request a frame.
func Invalidate[T any](source Reader[T], window *mygo.Window) (stop func()) {
	store := sourceStore(source)
	if store == nil || window == nil {
		return func() {}
	}
	return bindInvalidation(window, func(redraw func()) func() {
		return store.Subscribe(func(Snapshot[T]) { redraw() })
	})
}

// InvalidateSelector requests a frame only when the selected value changes, using SubscribeSelector's equality rules. The subscription stops when the window closes or the returned function is called. Selectors run on writer goroutines and must be concurrency-safe; selected reference values follow the store's ownership policy.
func InvalidateSelector[T any, V any](source Reader[T], window *mygo.Window, selector func(T) V) (stop func()) {
	return InvalidateSelectorWith(source, window, selector, SelectorOptions[V]{})
}

// InvalidateSelectorWith is InvalidateSelector with custom equality and optional immediate invalidation. Nil inputs and destroyed windows subscribe to nothing. As with Invalidate, stopping does not wait for callbacks already running.
func InvalidateSelectorWith[T any, V any](source Reader[T], window *mygo.Window, selector func(T) V, options SelectorOptions[V]) (stop func()) {
	store := sourceStore(source)
	if store == nil || window == nil || selector == nil {
		return func() {}
	}
	return bindInvalidation(window, func(redraw func()) func() {
		return SubscribeSelectorWith(store, selector, func(_, _ V) { redraw() }, options)
	})
}

// invalidationWindow is the native lifecycle boundary; state subscriptions need no running UI event loop.
type invalidationWindow interface {
	Invalidate()
	IsDestroyed() bool
	OnClosed(func()) func()
}

func bindInvalidation(window invalidationWindow, subscribe func(redraw func()) func()) func() {
	if window.IsDestroyed() {
		return func() {}
	}
	var mu sync.Mutex
	var stopped bool
	var cleanups []func()
	stop := func() {
		mu.Lock()
		if stopped {
			mu.Unlock()
			return
		}
		stopped = true
		pending := cleanups
		cleanups = nil
		mu.Unlock()
		for _, cleanup := range pending {
			cleanup()
		}
	}
	// Closing may race with either registration, including selector initialization. A registration completed after stop must be removed immediately.
	addCleanup := func(cleanup func()) {
		mu.Lock()
		if !stopped {
			cleanups = append(cleanups, cleanup)
			mu.Unlock()
			return
		}
		mu.Unlock()
		cleanup()
	}
	attached := false
	defer func() {
		if !attached {
			stop()
		}
	}()
	addCleanup(window.OnClosed(stop))
	// OnClosed does not replay a close that happened before registration.
	if window.IsDestroyed() {
		stop()
		return stop
	}
	addCleanup(subscribe(func() {
		mu.Lock()
		active := !stopped
		mu.Unlock()
		if active {
			window.Invalidate()
		}
	}))
	attached = true
	return stop
}
