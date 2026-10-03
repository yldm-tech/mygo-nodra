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

// History records successful store snapshots and provides bounded undo/redo.
type History[T any] struct {
	store    *Store[T]
	mu       sync.Mutex
	undo     []T
	redo     []T
	current  T
	limit    int
	applying bool
	stop     func()
	closed   bool
}

// Track starts recording changes to store. limit is the maximum number of
// undo entries; values below one disable history recording.
func Track[T any](store *Store[T], limit int) *History[T] {
	h := &History[T]{store: store, limit: limit}
	if store == nil || limit < 1 {
		h.closed = true
		return h
	}
	h.current = store.Get()
	h.stop = store.Subscribe(func(snapshot Snapshot[T]) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed {
			return
		}
		if h.applying {
			h.current = snapshot.Value
			return
		}
		h.undo = append(h.undo, h.current)
		if len(h.undo) > h.limit {
			h.undo = h.undo[len(h.undo)-h.limit:]
		}
		h.current = snapshot.Value
		h.redo = nil
	})
	return h
}

func (h *History[T]) CanUndo() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.undo) > 0 }
func (h *History[T]) CanRedo() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.redo) > 0 }

// Undo restores the previous state.
func (h *History[T]) Undo() error { return h.travel(true) }

// Redo restores the next state.
func (h *History[T]) Redo() error { return h.travel(false) }

func (h *History[T]) travel(undo bool) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	stack := &h.undo
	other := &h.redo
	if !undo {
		stack, other = other, stack
	}
	if len(*stack) == 0 {
		h.mu.Unlock()
		if undo {
			return ErrNoUndo
		}
		return ErrNoRedo
	}
	target := (*stack)[len(*stack)-1]
	*stack = (*stack)[:len(*stack)-1]
	*other = append(*other, h.current)
	h.applying = true
	h.mu.Unlock()
	err := h.store.Set(target)
	h.mu.Lock()
	h.applying = false
	h.current = h.store.Get()
	h.mu.Unlock()
	return err
}

// Close stops recording future changes. It does not close the underlying store.
func (h *History[T]) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	h.closed = true
	if h.stop != nil {
		h.stop()
	}
	h.undo = nil
	h.redo = nil
	return nil
}
