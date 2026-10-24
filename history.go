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
var ErrInvalidHistory = errors.New("nodra: invalid history size configuration")

// HistoryOptions configures recording. Equals excludes changes to irrelevant fields. Restore merges recorded fields into current state so ignored fields survive travel. Both functions must be pure, must not mutate arguments, and must not reenter the Store or History; they can run while those locks are held. With nil callbacks, every publication records and travel replaces the whole value.
type HistoryOptions[T any] struct {
	Limit int
	// MaxBytes budgets estimated undo and redo payloads together; zero disables sizing. Positive budgets require Size. The current value, Store state, shared allocations and metadata are outside this estimate.
	MaxBytes int64
	// Size estimates one retained value. It must return a nonnegative value and follow Equals/Restore purity and lock rules. Invalid configurations or negative estimates panic with ErrInvalidHistory.
	Size    func(T) int64
	Equals  func(previous, next T) bool
	Restore func(current, recorded T) T
}

// History records successful store snapshots and provides bounded undo/redo.
type History[T any] struct {
	store        *Store[T]
	mu           sync.Mutex
	undo         []historyEntry[T]
	redo         []historyEntry[T]
	current      T
	limit        int
	version      uint64
	stop         func()
	closed       bool
	options      HistoryOptions[T]
	groups       int
	grouped      bool
	bytes        uint64
	currentBytes uint64
	dropped      uint64
	revision     uint64
	state        *Store[HistoryState]
}

// Track starts recording changes to store. limit is the maximum number of undo entries; values below one disable history recording.
func Track[T any](store *Store[T], limit int) *History[T] {
	return TrackWith(store, HistoryOptions[T]{Limit: limit})
}

func TrackWith[T any](store *Store[T], options HistoryOptions[T]) *History[T] {
	if options.MaxBytes < 0 || (options.MaxBytes > 0 && options.Size == nil) {
		panic(ErrInvalidHistory)
	}
	h := &History[T]{store: store, limit: options.Limit, options: options, state: New(HistoryState{MaxBytes: options.MaxBytes})}
	if store == nil || options.Limit < 1 {
		h.closed = true
		h.revision++
		h.publishStatus()
		_ = h.state.Close()
		return h
	}
	initial := store.Snapshot()
	h.current, h.version = initial.Value, initial.Version
	h.currentBytes = h.estimate(initial.Value)
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
	func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.observeLocked(snapshot)
	}()
	h.publishStatus()
}

func (h *History[T]) observeLocked(snapshot Snapshot[T]) {
	if h.closed || snapshot.Version <= h.version {
		return
	}
	size := h.estimate(snapshot.Value)
	if h.options.Equals == nil || !h.options.Equals(h.current, snapshot.Value) {
		if h.groups == 0 || !h.grouped {
			h.undo = append(h.undo, historyEntry[T]{h.current, h.currentBytes})
			h.bytes += h.currentBytes
			h.grouped = h.groups > 0
		}
		for _, entry := range h.redo {
			h.bytes -= entry.bytes
		}
		h.redo = nil
		h.trimLocked()
	}
	h.current, h.version, h.currentBytes = snapshot.Value, snapshot.Version, size
	h.revision++
}

// BeginGroup merges edits across frames into one undo step while still publishing every update. Groups nest; the returned end function is idempotent. Concurrent writers join the group. Undo and Redo are rejected until all groups end. Filtering still applies, so a group containing only ignored changes adds no step.
func (h *History[T]) BeginGroup() (func(), error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if h.groups == 0 {
		h.grouped = false
	}
	h.groups++
	h.revision++
	h.mu.Unlock()
	h.publishStatus()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.groups--
			if h.groups == 0 {
				h.grouped = false
			}
			h.revision++
			h.mu.Unlock()
			h.publishStatus()
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
	defer h.publishStatus()
	var target T
	var targetBytes uint64
	// Choose and commit the history step under the source write lock. Stack changes happen only after the store's commit clone has succeeded, before any notifications can reenter history or a concurrent writer can commit.
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
		target = stack[len(stack)-1].value
		if h.options.Restore != nil {
			target = h.options.Restore(*value, target)
		}
		targetBytes = h.estimate(target)
		*value = target
		return nil
	}, func(version uint64) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed {
			return
		}
		var zero historyEntry[T]
		if undo {
			h.bytes -= h.undo[len(h.undo)-1].bytes
			h.undo[len(h.undo)-1] = zero
			h.undo = h.undo[:len(h.undo)-1]
			h.redo = append(h.redo, historyEntry[T]{h.current, h.currentBytes})
		} else {
			h.bytes -= h.redo[len(h.redo)-1].bytes
			h.redo[len(h.redo)-1] = zero
			h.redo = h.redo[:len(h.redo)-1]
			h.undo = append(h.undo, historyEntry[T]{h.current, h.currentBytes})
		}
		h.bytes += h.currentBytes
		h.trimLocked()
		h.current, h.version, h.currentBytes = target, version, targetBytes
		h.revision++
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
	h.bytes = 0
	h.revision++
	h.mu.Unlock()
	if h.stop != nil {
		h.stop()
	}
	if h.state != nil {
		defer func() { _ = h.state.Close() }()
	}
	h.publishStatus()
	return nil
}
