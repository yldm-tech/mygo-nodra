// Package devtools provides an opt-in local inspector for Nodra stores. It is
// separate from the core so production applications need not import its server
// or browser UI. Attach only the stores appropriate for a development session.
package devtools

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yldm-tech/nodra"
)

var (
	ErrClosed      = errors.New("nodra devtools: inspector closed")
	ErrNotFound    = errors.New("nodra devtools: store or snapshot not found")
	ErrReadOnly    = errors.New("nodra devtools: restoration disabled")
	ErrStoreExists = errors.New("nodra devtools: store ID already attached")
	ErrInvalid     = errors.New("nodra devtools: invalid store or state")
)

// Options configures a development inspector. Restore is opt-in. Redact runs before state enters the inspector; it must be concurrency-safe and return valid JSON. Redacted inspectors are always read-only to avoid restoring masked data. The default event limit is 200; state and event histories stay in memory.
type Options struct {
	// TaskProgressInterval coalesces intermediate running progress per task with a trailing update. Zero captures every trace. Lifecycle changes and errors are immediate.
	TaskProgressInterval time.Duration
	// SampleInterval skips mutation encoding within this interval per store. Zero captures every mutation; action completions remain observable.
	SampleInterval time.Duration
	Limit          int
	// MaxStateBytes limits each encoded state after redaction (default 1 MiB).
	MaxStateBytes int
	// MaxHistoryBytes budgets retained state/text plus event metadata (16 MiB).
	MaxHistoryBytes int
	AllowRestore    bool
	Redact          func(storeID string, state json.RawMessage) (json.RawMessage, error)
	// RedactMetadata filters names, task keys and error text before retention, including snapshots and pending progress. It must be concurrency-safe. Failure or panic discards all original metadata and records a fixed diagnostic. Store IDs are application-supplied public labels and are not filtered.
	RedactMetadata func(storeID string, metadata Metadata) (Metadata, error)
}

// Metadata contains diagnostic text that may include application secrets. Numeric correlation IDs and lifecycle fields are deliberately excluded.
type Metadata struct {
	Name  string
	Key   string
	Error string
}

type Store struct {
	TaskProgressCoalesced uint64          `json:"taskProgressCoalesced,omitempty"`
	Closed                bool            `json:"closed,omitempty"`
	TasksDropped          uint64          `json:"tasksDropped,omitempty"`
	ID                    string          `json:"id"`
	Version               uint64          `json:"version"`
	Generation            uint64          `json:"generation"`
	State                 json.RawMessage `json:"state,omitempty"`
	Error                 string          `json:"error,omitempty"`
	Restorable            bool            `json:"restorable"`
}

// Event sequences describe capture order, not an ordering guarantee between
// concurrent writers. Version and At preserve the source mutation's metadata.
type Event struct {
	Metrics       nodra.TaskMetrics  `json:"metrics"`
	RunID         uint64             `json:"runId,omitempty"`
	Key           string             `json:"key,omitempty"`
	Phase         nodra.TaskPhase    `json:"phase,omitempty"`
	Progress      float64            `json:"progress,omitempty"`
	TaskVersion   uint64             `json:"taskVersion,omitempty"`
	TraceRevision uint64             `json:"traceRevision,omitempty"`
	Committed     bool               `json:"committed,omitempty"`
	ActionID      uint64             `json:"actionId,omitempty"`
	Sequence      uint64             `json:"sequence"`
	StoreID       string             `json:"storeId"`
	Kind          string             `json:"kind"`
	Mutation      nodra.MutationType `json:"mutation,omitempty"`
	Name          string             `json:"name,omitempty"`
	Version       uint64             `json:"version"`
	Generation    uint64             `json:"generation"`
	At            time.Time          `json:"at"`
	Duration      time.Duration      `json:"duration,omitempty"`
	Previous      json.RawMessage    `json:"previous,omitempty"`
	State         json.RawMessage    `json:"state,omitempty"`
	Error         string             `json:"error,omitempty"`
}

type Snapshot struct {
	Tasks      []Event   `json:"tasks"`
	Revision   uint64    `json:"revision"`
	Cursor     uint64    `json:"cursor"`
	Capture    Capture   `json:"capture"`
	Format     int       `json:"format"`
	CapturedAt time.Time `json:"capturedAt"`
	Stores     []Store   `json:"stores"`
	Events     []Event   `json:"events"`
	Retention  Retention `json:"retention"`
}

// Retention describes the bounded timeline, not total process memory. Bytes counts state/text bytes and a fixed allowance for each event's metadata.
type Retention struct {
	Bytes     int    `json:"bytes"`
	MaxBytes  int    `json:"maxBytes"`
	MaxEvents int    `json:"maxEvents"`
	Dropped   uint64 `json:"dropped"`
}

type attached struct {
	progress    map[uint64]*taskProgress
	tasks       []Event
	taskBytes   int
	taskFloor   uint64
	revision    uint64
	lastCapture time.Time
	capture     func()
	view        Store
	stop        func()
	restore     func(json.RawMessage) error
}

// Inspector captures JSON state and events without changing Store's execution
// model. Encoding occurs on the notifying goroutine; use it for development,
// with immutable state or NewWithClone. Close releases subscriptions and server.
type Inspector struct {
	revision     uint64
	cleared      uint64
	paused       bool
	skipped      uint64
	mu           sync.Mutex
	options      Options
	stores       map[string]*attached
	events       []Event
	historyBytes int
	dropped      uint64
	next         uint64
	closed       bool
	token        string
	server       *http.Server
	done         chan struct{}
}

func New(options Options) *Inspector {
	if options.TaskProgressInterval < 0 {
		options.TaskProgressInterval = 0
	}
	if options.SampleInterval < 0 {
		options.SampleInterval = 0
	}
	if options.Limit < 1 {
		options.Limit = 200
	}
	if options.MaxStateBytes < 1 {
		options.MaxStateBytes = 1 << 20
	}
	if options.MaxHistoryBytes < 1 {
		options.MaxHistoryBytes = 16 << 20
	}
	return &Inspector{options: options, stores: make(map[string]*attached), token: rand.Text()}
}

// Attach observes one typed store. IDs are explicit and unique, so unnamed and
// heterogeneous stores can share an inspector. Unsupported JSON values appear
// as inspection errors. The returned function detaches this store idempotently.
func Attach[T any](i *Inspector, id string, source nodra.Reader[T]) (func(), error) {
	if view, ok := source.(*nodra.ReadOnly[T]); ok && view == nil {
		return nil, ErrInvalid
	}
	if i == nil || source == nil || source.Done() == nil || strings.TrimSpace(id) == "" {
		return nil, ErrInvalid
	}
	initial := source.Snapshot()
	state, stateErr := i.encode(id, initial.Value)
	initialEvent := i.redactEvent(Event{StoreID: id, Kind: "snapshot", Version: initial.Version, At: time.Now().UTC(), State: state, Error: stateErr})
	node := &attached{view: Store{ID: id, Version: initial.Version, State: state, Error: stateErr,
		Restorable: false}}
	node.view.Error = initialEvent.Error
	node.capture = func() {
		latest := source.Snapshot()
		raw, message := i.encode(id, latest.Value)
		i.record(node, Event{StoreID: id, Kind: "snapshot", Version: latest.Version, At: time.Now().UTC(), State: raw, Error: message}, message, true)
		i.refreshTasks(node, source.TaskSnapshot())
	}
	if writable, ok := source.(*nodra.Store[T]); ok {
		node.restore = func(raw json.RawMessage) error {
			var value T
			if err := decode(raw, &value); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			return writable.Set(value)
		}
		node.view.Restorable = node.restore != nil && i.options.AllowRestore && i.options.Redact == nil && stateErr == ""
	}
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil, ErrClosed
	}
	if _, exists := i.stores[id]; exists {
		i.mu.Unlock()
		return nil, ErrStoreExists
	}
	i.stores[id] = node
	node.view.Generation = i.next + 1
	initialEvent.Generation = node.view.Generation
	i.appendLocked(initialEvent)
	node.revision = i.revision
	i.mu.Unlock()
	stopMutation := source.SubscribeMutations(func(m nodra.Mutation[T]) {
		if !i.allowCapture(node, true) {
			return
		}
		state, stateErr := i.encode(id, m.Value)
		previous, previousErr := i.encode(id, m.Previous)
		i.record(node, Event{StoreID: id, Kind: "mutation", Mutation: m.Type, Name: m.Name,
			ActionID: m.ActionID, Version: m.Version, At: m.At, Previous: previous, State: state, Error: joinMessages(stateErr, previousErr)}, stateErr, true)
	})
	stopAction := source.SubscribeActions(func(a nodra.ActionEvent) {
		if !i.allowCapture(node, false) {
			return
		}
		message := ""
		if a.Err != nil {
			message = a.Err.Error()
		}
		i.record(node, Event{StoreID: id, Kind: "action", ActionID: a.ID, Name: a.Name, At: a.Finished,
			RunID: a.RunID, Key: a.Key, Committed: a.Committed, Version: a.Version, Duration: a.Duration, Metrics: a.Metrics, Error: message}, "", false)
	})
	stopTrace := source.SubscribeActionTrace(func(trace nodra.ActionTrace) { i.recordTrace(node, trace) })
	watcherStop := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { stopMutation(); stopAction(); stopTrace(); close(watcherStop) }) }
	i.mu.Lock()
	if i.closed || i.stores[id] != node {
		i.mu.Unlock()
		stop()
		return nil, ErrClosed
	}
	node.stop = stop
	i.mu.Unlock()
	i.refreshTasks(node, source.TaskSnapshot())
	go func() {
		select {
		case <-source.Done():
			i.refreshTasks(node, source.TaskSnapshot())
			i.mu.Lock()
			if !i.closed && i.stores[id] == node {
				node.view.Closed = true
				node.view.Restorable = false
				i.revision++
				node.revision = i.revision
			}
			i.mu.Unlock()
		case <-watcherStop:
		}
	}()
	// Catch an update during registration. Intermediate changes in this gap
	// may coalesce, but the current state must not remain behind the source.
	latest := source.Snapshot()
	if latest.Version > initial.Version {
		state, stateErr := i.encode(id, latest.Value)
		i.record(node, Event{StoreID: id, Kind: "snapshot", Version: latest.Version, At: time.Now().UTC(), State: state, Error: stateErr}, stateErr, true)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			i.mu.Lock()
			if i.stores[id] == node {
				delete(i.stores, id)
				i.cancelProgressLocked(node)
				i.revision++
			}
			i.mu.Unlock()
			stop()
		})
	}, nil
}

func joinMessages(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || a == b {
		return a
	}
	return a + "; " + b
}

func (i *Inspector) encode(id string, value any) (json.RawMessage, string) {
	raw, err := json.Marshal(value)
	if err == nil && i.options.Redact != nil {
		raw, err = i.options.Redact(id, raw)
	}
	if err != nil {
		return nil, err.Error()
	}
	if !json.Valid(raw) {
		return nil, "redactor returned invalid JSON"
	}
	if len(raw) > i.options.MaxStateBytes {
		return nil, fmt.Sprintf("state exceeds inspection limit (%d bytes; maximum %d)", len(raw), i.options.MaxStateBytes)
	}
	return bytes.Clone(raw), ""
}

func (i *Inspector) appendLocked(event Event) {
	i.revision++
	i.next++
	event.Sequence = i.next
	size := eventBytes(event)
	if size > i.options.MaxHistoryBytes {
		i.dropped++
		return
	}
	for len(i.events) > 0 && (len(i.events) >= i.options.Limit || i.historyBytes+size > i.options.MaxHistoryBytes) {
		i.historyBytes -= eventBytes(i.events[0])
		i.events[0] = Event{} // Release evicted buffers, including sliced capacity.
		i.events = i.events[1:]
		i.dropped++
	}
	i.events = append(i.events, event)
	i.historyBytes += size
}

func eventBytes(event Event) int {
	return 256 + len(event.StoreID) + len(event.Kind) + len(event.Mutation) + len(event.Name) + len(event.Key) + len(event.Phase) + len(event.Previous) + len(event.State) + len(event.Error)
}

func (i *Inspector) record(node *attached, event Event, stateErr string, update bool) {
	stateValid := stateErr == ""
	event = i.redactEvent(event)
	if update {
		stateErr = i.redactEvent(Event{StoreID: event.StoreID, Error: stateErr}).Error
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.stores[node.view.ID] != node {
		return
	}
	if i.paused {
		i.skipped++
		i.revision++
		return
	}
	if update && event.Version >= node.view.Version {
		node.view.Version, node.view.State, node.view.Error = event.Version, event.State, stateErr
		node.view.Restorable = !node.view.Closed && node.restore != nil && i.options.AllowRestore && i.options.Redact == nil && stateValid
	}
	event.Generation = node.view.Generation
	i.appendLocked(event)
	if update {
		node.revision = i.revision
	}
}

// Snapshot returns an independent export, including copies of JSON buffers.
func (i *Inspector) Snapshot() Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	result := Snapshot{Format: 1, CapturedAt: time.Now().UTC(), Stores: make([]Store, 0, len(i.stores)), Events: make([]Event, len(i.events))}
	result.Revision, result.Cursor, result.Capture = i.revision, i.next, i.captureLocked()
	result.Tasks = i.tasksLocked()
	result.Retention = Retention{Bytes: i.historyBytes, MaxBytes: i.options.MaxHistoryBytes, MaxEvents: i.options.Limit, Dropped: i.dropped}
	for _, node := range i.stores {
		view := node.view
		view.State = bytes.Clone(view.State)
		result.Stores = append(result.Stores, view)
	}
	sort.Slice(result.Stores, func(a, b int) bool { return result.Stores[a].ID < result.Stores[b].ID })
	for n, event := range i.events {
		event.State, event.Previous = bytes.Clone(event.State), bytes.Clone(event.Previous)
		result.Events[n] = event
	}
	return result
}

func (i *Inspector) Clear() {
	i.mu.Lock()
	i.events = nil
	for _, node := range i.stores {
		i.cancelProgressLocked(node)
		node.view.TaskProgressCoalesced = 0
		node.tasks = nil
		node.taskBytes = 0
		node.taskFloor = 0
		node.view.TasksDropped = 0
	}
	i.historyBytes, i.dropped = 0, 0
	i.revision++
	i.cleared = i.revision
	i.mu.Unlock()
}

// Restore replaces a store with a retained event's typed JSON state. Action
// side effects are not reversed. New snapshots/events are emitted normally.
func (i *Inspector) Restore(id string, sequence uint64) error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return ErrClosed
	}
	var state json.RawMessage
	node := i.stores[id]
	for _, event := range i.events {
		if node != nil && event.Sequence == sequence && event.StoreID == id && event.Generation == node.view.Generation {
			state = bytes.Clone(event.State)
			break
		}
	}
	i.mu.Unlock()
	if state == nil {
		return ErrNotFound
	}
	return i.apply(id, state, node)
}

// Apply strictly decodes one store's JSON before Set. It is disabled unless
// AllowRestore is true and no redactor is configured. No actions are replayed.
func (i *Inspector) Apply(id string, state json.RawMessage) error {
	return i.apply(id, state, nil)
}

func (i *Inspector) apply(id string, state json.RawMessage, expected *attached) error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return ErrClosed
	}
	if !i.options.AllowRestore || i.options.Redact != nil {
		i.mu.Unlock()
		return ErrReadOnly
	}
	node := i.stores[id]
	if node == nil || (expected != nil && node != expected) {
		i.mu.Unlock()
		return ErrNotFound
	}
	if !node.view.Restorable {
		i.mu.Unlock()
		return ErrReadOnly
	}
	i.mu.Unlock()
	return node.restore(bytes.Clone(state))
}

func decode(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func (i *Inspector) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	nodes, server, done := i.stores, i.server, i.done
	for _, node := range nodes {
		i.cancelProgressLocked(node)
	}
	i.stores = make(map[string]*attached)
	i.mu.Unlock()
	for _, node := range nodes {
		if node.stop != nil {
			node.stop()
		}
	}
	if server != nil {
		err := server.Close()
		<-done
		return err
	}
	return nil
}
