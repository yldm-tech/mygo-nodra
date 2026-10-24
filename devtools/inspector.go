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

// Options configures a development inspector. Restore is opt-in. Redact runs
// before state enters the inspector; it must be concurrency-safe and return
// valid JSON. Redacted inspectors are always read-only to avoid restoring masked
// data. The default event limit is 200; state and event histories stay in memory.
type Options struct {
	Limit        int
	AllowRestore bool
	Redact       func(storeID string, state json.RawMessage) (json.RawMessage, error)
}

type Store struct {
	ID         string          `json:"id"`
	Version    uint64          `json:"version"`
	Generation uint64          `json:"generation"`
	State      json.RawMessage `json:"state,omitempty"`
	Error      string          `json:"error,omitempty"`
	Restorable bool            `json:"restorable"`
}

// Event sequences describe capture order, not an ordering guarantee between
// concurrent writers. Version and At preserve the source mutation's metadata.
type Event struct {
	Sequence   uint64             `json:"sequence"`
	StoreID    string             `json:"storeId"`
	Kind       string             `json:"kind"`
	Mutation   nodra.MutationType `json:"mutation,omitempty"`
	Name       string             `json:"name,omitempty"`
	Version    uint64             `json:"version"`
	Generation uint64             `json:"generation"`
	At         time.Time          `json:"at"`
	Duration   time.Duration      `json:"duration,omitempty"`
	Previous   json.RawMessage    `json:"previous,omitempty"`
	State      json.RawMessage    `json:"state,omitempty"`
	Error      string             `json:"error,omitempty"`
}

type Snapshot struct {
	Format     int       `json:"format"`
	CapturedAt time.Time `json:"capturedAt"`
	Stores     []Store   `json:"stores"`
	Events     []Event   `json:"events"`
}

type attached struct {
	view    Store
	stop    func()
	restore func(json.RawMessage) error
}

// Inspector captures JSON state and events without changing Store's execution
// model. Encoding occurs on the notifying goroutine; use it for development,
// with immutable state or NewWithClone. Close releases subscriptions and server.
type Inspector struct {
	mu      sync.Mutex
	options Options
	stores  map[string]*attached
	events  []Event
	next    uint64
	closed  bool
	token   string
	server  *http.Server
	done    chan struct{}
}

func New(options Options) *Inspector {
	if options.Limit < 1 {
		options.Limit = 200
	}
	return &Inspector{options: options, stores: make(map[string]*attached), token: rand.Text()}
}

// Attach observes one typed store. IDs are explicit and unique, so unnamed and
// heterogeneous stores can share an inspector. Unsupported JSON values appear
// as inspection errors. The returned function detaches this store idempotently.
func Attach[T any](i *Inspector, id string, source *nodra.Store[T]) (func(), error) {
	if i == nil || source == nil || strings.TrimSpace(id) == "" {
		return nil, ErrInvalid
	}
	initial := source.Snapshot()
	state, stateErr := i.encode(id, initial.Value)
	node := &attached{view: Store{ID: id, Version: initial.Version, State: state, Error: stateErr,
		Restorable: i.options.AllowRestore && i.options.Redact == nil && stateErr == ""}}
	node.restore = func(raw json.RawMessage) error {
		var value T
		if err := decode(raw, &value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return source.Set(value)
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
	i.appendLocked(Event{StoreID: id, Generation: node.view.Generation, Kind: "snapshot", Version: initial.Version, At: time.Now().UTC(), State: state, Error: stateErr})
	i.mu.Unlock()
	stopMutation := source.SubscribeMutations(func(m nodra.Mutation[T]) {
		state, stateErr := i.encode(id, m.Value)
		previous, previousErr := i.encode(id, m.Previous)
		i.record(node, Event{StoreID: id, Kind: "mutation", Mutation: m.Type, Name: m.Name,
			Version: m.Version, At: m.At, Previous: previous, State: state, Error: joinMessages(stateErr, previousErr)}, stateErr, true)
	})
	stopAction := source.SubscribeActions(func(a nodra.ActionEvent) {
		message := ""
		if a.Err != nil {
			message = a.Err.Error()
		}
		i.record(node, Event{StoreID: id, Kind: "action", Name: a.Name, At: a.Finished,
			Version: source.Version(), Duration: a.Duration, Error: message}, "", false)
	})
	stop := func() { stopMutation(); stopAction() }
	i.mu.Lock()
	if i.closed || i.stores[id] != node {
		i.mu.Unlock()
		stop()
		return nil, ErrClosed
	}
	node.stop = stop
	i.mu.Unlock()
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
	return bytes.Clone(raw), ""
}

func (i *Inspector) appendLocked(event Event) {
	i.next++
	event.Sequence = i.next
	if len(i.events) == i.options.Limit {
		copy(i.events, i.events[1:])
		i.events[len(i.events)-1] = event
	} else {
		i.events = append(i.events, event)
	}
}

func (i *Inspector) record(node *attached, event Event, stateErr string, update bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.stores[node.view.ID] != node {
		return
	}
	if update && event.Version >= node.view.Version {
		node.view.Version, node.view.State, node.view.Error = event.Version, event.State, stateErr
		node.view.Restorable = i.options.AllowRestore && i.options.Redact == nil && stateErr == ""
	}
	event.Generation = node.view.Generation
	i.appendLocked(event)
}

// Snapshot returns an independent export, including copies of JSON buffers.
func (i *Inspector) Snapshot() Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	result := Snapshot{Format: 1, CapturedAt: time.Now().UTC(), Stores: make([]Store, 0, len(i.stores)), Events: make([]Event, len(i.events))}
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
	return i.Apply(id, state)
}

// Apply strictly decodes one store's JSON before Set. It is disabled unless
// AllowRestore is true and no redactor is configured. No actions are replayed.
func (i *Inspector) Apply(id string, state json.RawMessage) error {
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
	if node == nil {
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
