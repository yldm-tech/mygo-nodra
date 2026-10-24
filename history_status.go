package nodra

import "errors"

type historyEntry[T any] struct {
	value T
	bytes uint64
}

// HistoryState describes history availability and estimated retained payloads. Revision orders status publications independently of source versions. Bytes excludes the current value and is an application estimate, not process heap usage. CanUndo/CanRedo are false while grouped or closed.
type HistoryState struct {
	Revision    uint64
	UndoEntries int
	RedoEntries int
	Bytes       int64
	MaxBytes    int64
	Dropped     uint64
	Grouped     bool
	Closed      bool
	CanUndo     bool
	CanRedo     bool
}

func (h *History[T]) Status() ReadOnly[HistoryState] { return h.state.ReadOnly() }

func (h *History[T]) estimate(value T) uint64 {
	if h.options.MaxBytes == 0 {
		return 0
	}
	size := h.options.Size(value)
	if size < 0 {
		panic(ErrInvalidHistory)
	}
	return uint64(size)
}

func (h *History[T]) trimLocked() {
	for len(h.undo) > h.limit || (h.options.MaxBytes > 0 && h.bytes > uint64(h.options.MaxBytes)) {
		stack := &h.undo
		if len(*stack) == 0 {
			stack = &h.redo
		}
		if len(*stack) == 0 {
			break
		}
		h.bytes -= (*stack)[0].bytes
		var zero historyEntry[T]
		(*stack)[0] = zero
		*stack = (*stack)[1:]
		h.dropped++
	}
}

var errHistoryStatusUnchanged = errors.New("nodra: history status unchanged")

// Status subscribers run after releasing both the history lock and source write lock. A delayed publisher cannot replace a newer status or revive a closed status Store.
func (h *History[T]) publishStatus() {
	h.mu.Lock()
	state := HistoryState{Revision: h.revision, UndoEntries: len(h.undo), RedoEntries: len(h.redo), Bytes: int64(h.bytes), MaxBytes: h.options.MaxBytes, Dropped: h.dropped, Grouped: h.groups > 0, Closed: h.closed, CanUndo: !h.closed && h.groups == 0 && len(h.undo) > 0, CanRedo: !h.closed && h.groups == 0 && len(h.redo) > 0}
	h.mu.Unlock()
	_ = h.state.UpdateErr(func(current *HistoryState) error {
		if current.Revision >= state.Revision {
			return errHistoryStatusUnchanged
		}
		*current = state
		return nil
	})
}
