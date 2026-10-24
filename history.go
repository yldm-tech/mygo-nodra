package nodra

import (
	"errors"
	"sync"
)

// ErrNoUndo and ErrNoRedo report an empty history stack.
var (
	ErrNoUndo = errors.New("nodra: nothing to undo")
	ErrNoRedo = errors.New("nodra: nothing to redo")
)

// ErrHistoryBatch reports travel without an independent publication boundary.
var ErrHistoryBatch = errors.New("nodra: cannot undo or redo inside an open batch")

// History records successful store snapshots and provides bounded undo/redo.
type History[T any] struct {
	store   *Store[T]
	mu      sync.Mutex
	undo    []T
	redo    []T
	current T
	limit   int
	version uint64
	stop    func()
	closed  bool
}

// Track starts recording changes to store. limit is the maximum number of
// undo entries; values below one disable history recording.
func Track[T any](store *Store[T], limit int) *History[T] {
	h := &History[T]{store: store, limit: limit}
	if store == nil || limit < 1 {
		h.closed = true
		return h
	}
	initial := store.Snapshot()
	h.current, h.version = initial.Value, initial.Version
	h.stop = store.Subscribe(h.observe)
	initialized := false
	defer func() {
		if !initialized {
			h.stop()
		}
	}()
	// Changes during registration may coalesce into one entry.
	h.observe(store.Snapshot())
	initialized = true
	return h
}

func (h *History[T]) observe(snapshot Snapshot[T]) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observeLocked(snapshot)
}

func (h *History[T]) observeLocked(snapshot Snapshot[T]) {
	if h.closed || snapshot.Version <= h.version {
		return
	}
	h.undo = append(h.undo, h.current)
	if len(h.undo) > h.limit {
		h.undo = h.undo[len(h.undo)-h.limit:]
	}
	h.current, h.version = snapshot.Value, snapshot.Version
	h.redo = nil
}

func (h *History[T]) CanUndo() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.undo) > 0 }
func (h *History[T]) CanRedo() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.redo) > 0 }

// Undo restores the previous state.
func (h *History[T]) Undo() error { return h.travel(true) }

// Redo restores the next state.
func (h *History[T]) Redo() error { return h.travel(false) }

func (h *History[T]) travel(undo bool) error {
	if h.store == nil {
		return ErrClosed
	}
	var target T
	// Choose and commit the history step under the source write lock. Stack
	// changes happen only after the store's commit clone has succeeded, before
	// any notifications can reenter history or a concurrent writer can commit.
	return h.store.updateCommitted("", MutationDirect, func(value *T) error {
		current := Snapshot[T]{*value, h.store.version}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed {
			return ErrClosed
		}
		if h.store.batch > 0 {
			return ErrHistoryBatch
		}
		h.observeLocked(current)
		stack := h.undo
		if !undo {
			stack = h.redo
		}
		if len(stack) == 0 {
			if undo {
				return ErrNoUndo
			}
			return ErrNoRedo
		}
		target = stack[len(stack)-1]
		*value = target
		return nil
	}, func(version uint64) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed {
			return
		}
		if undo {
			h.undo = h.undo[:len(h.undo)-1]
			h.redo = append(h.redo, h.current)
		} else {
			h.redo = h.redo[:len(h.redo)-1]
			h.undo = append(h.undo, h.current)
		}
		h.current, h.version = target, version
	})
}

// Close stops recording future changes. It does not close the underlying store.
func (h *History[T]) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	h.closed = true
	h.undo = nil
	h.redo = nil
	h.mu.Unlock()
	if h.stop != nil {
		h.stop()
	}
	return nil
}
