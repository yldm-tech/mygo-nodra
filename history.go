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

var ErrHistoryGroup = errors.New("nodra: cannot undo or redo inside an open history group")

// HistoryOptions configures recording. Equals excludes changes to irrelevant fields. Restore merges recorded fields into current state so ignored fields survive travel. Both functions must be pure, must not mutate arguments, and must not reenter the Store or History; they can run while those locks are held. With nil callbacks, every publication records and travel replaces the whole value.
type HistoryOptions[T any] struct {
	Limit   int
	Equals  func(previous, next T) bool
	Restore func(current, recorded T) T
}

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
	options HistoryOptions[T]
	groups  int
	grouped bool
}

// Track starts recording changes to store. limit is the maximum number of
// undo entries; values below one disable history recording.
func Track[T any](store *Store[T], limit int) *History[T] {
	return TrackWith(store, HistoryOptions[T]{Limit: limit})
}

func TrackWith[T any](store *Store[T], options HistoryOptions[T]) *History[T] {
	h := &History[T]{store: store, limit: options.Limit, options: options}
	if store == nil || options.Limit < 1 {
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
	if h.options.Equals == nil || !h.options.Equals(h.current, snapshot.Value) {
		if h.groups == 0 || !h.grouped {
			h.undo = append(h.undo, h.current)
			if len(h.undo) > h.limit {
				var zero T
				h.undo[0] = zero
				h.undo = h.undo[1:]
			}
			h.grouped = h.groups > 0
		}
		h.redo = nil
	}
	h.current, h.version = snapshot.Value, snapshot.Version
}

// BeginGroup merges edits across frames into one undo step while still publishing every update. Groups nest; the returned end function is idempotent. Concurrent writers join the group. Undo and Redo are rejected until all groups end. Filtering still applies, so a group containing only ignored changes adds no step.
func (h *History[T]) BeginGroup() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if h.groups == 0 {
		h.grouped = false
	}
	h.groups++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.groups--
			if h.groups == 0 {
				h.grouped = false
			}
			h.mu.Unlock()
		})
	}, nil
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
		if h.groups > 0 {
			return ErrHistoryGroup
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
		if h.options.Restore != nil {
			target = h.options.Restore(*value, target)
		}
		*value = target
		return nil
	}, func(version uint64) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed {
			return
		}
		if undo {
			var zero T
			h.undo[len(h.undo)-1] = zero
			h.undo = h.undo[:len(h.undo)-1]
			h.redo = append(h.redo, h.current)
		} else {
			var zero T
			h.redo[len(h.redo)-1] = zero
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
