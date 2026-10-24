package devtools

import (
	"bytes"
	"sort"
	"time"
)

// Capture describes diagnostic collection, independently of application state. Skipped counts notifications omitted while paused or sampled. Sampled/paused current states may be behind the source; resuming refreshes attached stores.
type Capture struct {
	Paused           bool   `json:"paused"`
	SampleIntervalMS int64  `json:"sampleIntervalMS"`
	Skipped          uint64 `json:"skipped"`
}

func (i *Inspector) captureLocked() Capture {
	return Capture{Paused: i.paused, SampleIntervalMS: i.options.SampleInterval.Milliseconds(), Skipped: i.skipped}
}

func (i *Inspector) allowCapture(node *attached, sample bool) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.stores[node.view.ID] != node {
		return false
	}
	now := time.Now()
	if i.paused || (sample && i.options.SampleInterval > 0 && now.Sub(node.lastCapture) < i.options.SampleInterval) {
		i.skipped++
		i.revision++
		return false
	}
	if sample {
		node.lastCapture = now
	}
	return true
}

// SetCapture changes capture policy before state encoding. Pausing does not stop application updates or alter retained history. Resuming (or disabling sampling) captures each current Store once. Notifications already encoding may finish, but record rechecks the pause flag.
func (i *Inspector) SetCapture(paused bool, interval time.Duration) error {
	if interval < 0 {
		return ErrInvalid
	}
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return ErrClosed
	}
	refresh := !paused && (i.paused || (i.options.SampleInterval > 0 && interval == 0))
	if paused {
		for _, node := range i.stores {
			i.cancelProgressLocked(node)
		}
	}
	i.paused = paused
	i.options.SampleInterval = interval
	i.revision++
	var captures []func()
	if refresh {
		for _, node := range i.stores {
			captures = append(captures, node.capture)
		}
	}
	i.mu.Unlock()
	for _, capture := range captures {
		capture()
	}
	return nil
}

// Changes is a bounded incremental poll. StoreIDs is authoritative for detachments; Stores contains only states changed since revision. Reset discards the client's event cache after a clear or initial connection. Oldest trims evicted events, Cursor advances through paginated new events, and More requests another page. Initial/reset polls start with the latest page; EventsBefore retrieves older retained history. Returned JSON buffers are independent.
type Changes struct {
	Tasks        []Event   `json:"tasks"`
	TasksChanged bool      `json:"tasksChanged"`
	Epoch        uint64    `json:"epoch"`
	Revision     uint64    `json:"revision"`
	Cursor       uint64    `json:"cursor"`
	Oldest       uint64    `json:"oldest"`
	Reset        bool      `json:"reset"`
	More         bool      `json:"more"`
	StoreIDs     []string  `json:"storeIds"`
	Stores       []Store   `json:"stores"`
	Events       []Event   `json:"events"`
	Retention    Retention `json:"retention"`
	Capture      Capture   `json:"capture"`
}

func pageLimit(limit int) int {
	if limit < 1 {
		return 50
	}
	return min(limit, 200)
}
func copyEvent(event Event) Event {
	event.State = bytes.Clone(event.State)
	event.Previous = bytes.Clone(event.Previous)
	return event
}

func (i *Inspector) Changes(revision, cursor uint64, limit int) Changes {
	i.mu.Lock()
	defer i.mu.Unlock()
	limit = pageLimit(limit)
	reset := revision == 0 || revision < i.cleared || revision > i.revision || cursor > i.next
	result := Changes{Epoch: i.cleared, Revision: i.revision, Cursor: i.next, Oldest: i.next + 1, Reset: reset, StoreIDs: make([]string, 0, len(i.stores)), Stores: []Store{}, Events: []Event{}, Capture: i.captureLocked(), Retention: Retention{Bytes: i.historyBytes, MaxBytes: i.options.MaxHistoryBytes, MaxEvents: i.options.Limit, Dropped: i.dropped}}
	result.TasksChanged = reset || revision != i.revision
	if result.TasksChanged {
		result.Tasks = i.tasksLocked()
	}
	for id, node := range i.stores {
		result.StoreIDs = append(result.StoreIDs, id)
		if reset || node.revision > revision {
			view := node.view
			view.State = bytes.Clone(view.State)
			result.Stores = append(result.Stores, view)
		}
	}
	sort.Strings(result.StoreIDs)
	sort.Slice(result.Stores, func(a, b int) bool { return result.Stores[a].ID < result.Stores[b].ID })
	if len(i.events) > 0 {
		result.Oldest = i.events[0].Sequence
	}
	start := 0
	if reset {
		start = max(0, len(i.events)-limit)
	} else {
		for start < len(i.events) && i.events[start].Sequence <= cursor {
			start++
		}
	}
	end := min(len(i.events), start+limit)
	for _, event := range i.events[start:end] {
		result.Events = append(result.Events, copyEvent(event))
	}
	if end < len(i.events) {
		result.More = true
		result.Cursor = i.events[end-1].Sequence
	}
	return result
}

type EventPage struct {
	Epoch  uint64  `json:"epoch"`
	Events []Event `json:"events"`
	More   bool    `json:"more"`
}

// EventsBefore returns the newest retained page strictly before sequence (zero means newest). It never includes current store payloads.
func (i *Inspector) EventsBefore(sequence uint64, limit int) EventPage {
	i.mu.Lock()
	defer i.mu.Unlock()
	end := len(i.events)
	if sequence > 0 {
		for end > 0 && i.events[end-1].Sequence >= sequence {
			end--
		}
	}
	start := max(0, end-pageLimit(limit))
	page := EventPage{Epoch: i.cleared, Events: make([]Event, 0, end-start), More: start > 0}
	for _, event := range i.events[start:end] {
		page.Events = append(page.Events, copyEvent(event))
	}
	return page
}
