package devtools

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yldm-tech/nodra"
)

type counterState struct {
	Count int `json:"count"`
}

func mustAttach[T any](t *testing.T, i *Inspector, id string, source *nodra.Store[T]) func() {
	t.Helper()
	stop, err := Attach(i, id, source)
	if err != nil {
		t.Fatal(err)
	}
	return stop
}

func mustUpdate(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCapturesHeterogeneousStoresMutationsAndActions(t *testing.T) {
	i := New(Options{})
	defer func() { _ = i.Close() }()
	count, label := nodra.New(counterState{}), nodra.New("ready")
	mustAttach(t, i, "counter", count)
	mustAttach(t, i, "label", label)
	mustUpdate(t, count.Do("increment", func(v *counterState) error { v.Count++; return nil }))
	want := errors.New("validation failed")
	if err := count.Do("rejected", func(v *counterState) error { v.Count = 99; return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	snapshot := i.Snapshot()
	if len(snapshot.Stores) != 2 || snapshot.Stores[0].ID != "counter" || string(snapshot.Stores[0].State) != `{"count":1}` {
		t.Fatalf("stores = %+v", snapshot.Stores)
	}
	if len(snapshot.Events) != 5 {
		t.Fatalf("events = %+v", snapshot.Events)
	}
	mutation, success, failure := snapshot.Events[2], snapshot.Events[3], snapshot.Events[4]
	if mutation.Kind != "mutation" || mutation.Mutation != nodra.MutationAction || mutation.Name != "increment" || string(mutation.Previous) != `{"count":0}` || mutation.Version != 1 {
		t.Fatalf("mutation = %+v", mutation)
	}
	if success.Kind != "action" || success.Name != "increment" || success.Duration < 0 || failure.Error != want.Error() || failure.Name != "rejected" {
		t.Fatalf("action events = %+v, %+v", success, failure)
	}
}

func TestRestoreAndApplyDecodeBeforeChangingState(t *testing.T) {
	i := New(Options{AllowRestore: true})
	defer func() { _ = i.Close() }()
	store := nodra.New(counterState{})
	mustAttach(t, i, "counter", store)
	initial := i.Snapshot().Events[0].Sequence
	mustUpdate(t, store.Set(counterState{Count: 7}))
	mustUpdate(t, i.Restore("counter", initial))
	if store.Get().Count != 0 || store.Version() != 2 {
		t.Fatal("restore did not publish a new typed state")
	}
	mustUpdate(t, i.Apply("counter", json.RawMessage(`{"count":4}`)))
	for _, raw := range []string{`{"count":"bad"}`, `{"unknown":9}`, `{} {}`, `{`} {
		if err := i.Apply("counter", json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid state %s accepted: %v", raw, err)
		}
		if store.Get().Count != 4 || store.Version() != 3 {
			t.Fatal("invalid restore changed the store")
		}
	}
	if err := i.Restore("counter", 999); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestReadOnlyAndRedactionProtectCapturedState(t *testing.T) {
	for _, redact := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-only", true: "redacted"}[redact], func(t *testing.T) {
			options := Options{}
			if redact {
				options.AllowRestore = true
				options.Redact = func(_ string, raw json.RawMessage) (json.RawMessage, error) {
					var state map[string]string
					if err := json.Unmarshal(raw, &state); err != nil {
						return nil, err
					}
					state["secret"] = "[redacted]"
					return json.Marshal(state)
				}
			}
			i := New(options)
			defer func() { _ = i.Close() }()
			store := nodra.New(map[string]string{"secret": "first-secret"})
			mustAttach(t, i, "account", store)
			mustUpdate(t, store.Set(map[string]string{"secret": "second-secret"}))
			snapshot := i.Snapshot()
			if snapshot.Stores[0].Restorable {
				t.Fatal("read-only/redacted store allows restoration")
			}
			if err := i.Restore("account", snapshot.Events[0].Sequence); !errors.Is(err, ErrReadOnly) {
				t.Fatalf("restore error = %v", err)
			}
			if redact {
				raw, err := json.Marshal(snapshot)
				mustUpdate(t, err)
				if strings.Contains(string(raw), "first-secret") || strings.Contains(string(raw), "second-secret") {
					t.Fatal("secret entered export or timeline")
				}
			}
		})
	}
}

func TestBoundedExportsDetachAndAttachmentGeneration(t *testing.T) {
	i := New(Options{Limit: 3, AllowRestore: true})
	defer func() { _ = i.Close() }()
	store := nodra.New(counterState{})
	detach := mustAttach(t, i, "counter", store)
	for n := 1; n <= 8; n++ {
		mustUpdate(t, store.Set(counterState{Count: n}))
	}
	snapshot := i.Snapshot()
	if len(snapshot.Events) != 3 || snapshot.Events[0].Sequence != 7 {
		t.Fatalf("bounded history = %+v", snapshot.Events)
	}
	oldSequence := snapshot.Events[0].Sequence
	snapshot.Stores[0].State[0] = 'X'
	snapshot.Events[0].State[0] = 'X'
	snapshot.Events[0].Previous[0] = 'X'
	if _, err := json.Marshal(i.Snapshot()); err != nil {
		t.Fatalf("export buffers alias internal state: %v", err)
	}
	detach()
	detach()
	mustUpdate(t, store.Set(counterState{Count: 9}))
	if len(i.Snapshot().Stores) != 0 || len(i.Snapshot().Events) != 3 {
		t.Fatal("detached store still recorded changes")
	}
	other := nodra.New(counterState{Count: 100})
	mustAttach(t, i, "counter", other)
	// Use the last event from the old attachment, which remains retained.
	if err := i.Restore("counter", oldSequence+2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restored a different store instance: %v", err)
	}
	i.Clear()
	if len(i.Snapshot().Events) != 0 || len(i.Snapshot().Stores) != 1 {
		t.Fatal("clear removed stores or retained events")
	}
}

func TestInspectionFailuresAreVisibleWithoutBreakingStore(t *testing.T) {
	for _, reason := range []string{"unsupported", "invalid-redaction", "redaction-error"} {
		t.Run(reason, func(t *testing.T) {
			options := Options{AllowRestore: true}
			switch reason {
			case "invalid-redaction":
				options.Redact = func(string, json.RawMessage) (json.RawMessage, error) { return []byte("{"), nil }
			case "redaction-error":
				options.Redact = func(string, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("redaction failed") }
			}
			i := New(options)
			defer func() { _ = i.Close() }()
			if reason == "unsupported" {
				store := nodra.New(make(chan int))
				mustAttach(t, i, "unsupported", store)
				mustUpdate(t, store.Set(make(chan int)))
			} else {
				store := nodra.New(0)
				mustAttach(t, i, "unsupported", store)
				mustUpdate(t, store.Set(1))
			}
			snapshot := i.Snapshot()
			if snapshot.Stores[0].Error == "" || snapshot.Stores[0].Restorable || snapshot.Events[1].Error == "" {
				t.Fatalf("inspection error was hidden: %+v", snapshot)
			}
			if _, err := json.Marshal(snapshot); err != nil {
				t.Fatalf("failed inspection broke JSON export: %v", err)
			}
		})
	}
}

func TestOutOfOrderCaptureDoesNotRegressCurrentState(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	i := New(Options{Redact: func(_ string, raw json.RawMessage) (json.RawMessage, error) {
		if string(raw) == `1` && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return raw, nil
	}})
	defer func() { _ = i.Close() }()
	store := nodra.New(0)
	mustAttach(t, i, "counter", store)
	go func() {
		defer close(done)
		if err := store.Set(1); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	// Finish the newer capture while the old state encoding is still blocked.
	mustUpdate(t, store.Set(2))
	close(release)
	<-done
	if view := i.Snapshot().Stores[0]; view.Version != 2 || string(view.State) != "2" {
		t.Fatalf("current state regressed: %+v", view)
	}
}

func TestAttachCatchesRegistrationGapAndRejectsDuplicateIDs(t *testing.T) {
	store := nodra.New(0)
	var once sync.Once
	i := New(Options{Redact: func(_ string, raw json.RawMessage) (json.RawMessage, error) {
		once.Do(func() { mustUpdate(t, store.Set(1)) })
		return raw, nil
	}})
	defer func() { _ = i.Close() }()
	mustAttach(t, i, "counter", store)
	if view := i.Snapshot().Stores[0]; view.Version != 1 || string(view.State) != "1" {
		t.Fatalf("initial update was missed: %+v", view)
	}
	if _, err := Attach(i, "counter", store); !errors.Is(err, ErrStoreExists) {
		t.Fatalf("duplicate attachment error = %v", err)
	}
	if _, err := Attach[int](i, "nil", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil attachment error = %v", err)
	}
}

func TestConcurrentCaptureAndCloseReleaseSubscriptions(t *testing.T) {
	i := New(Options{Limit: 20})
	store := nodra.New(0)
	mustAttach(t, i, "primary", store)
	ready, start := make(chan struct{}, 8), make(chan struct{})
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Go(func() {
			stop, err := Attach(i, string(rune('a'+n)), store)
			if errors.Is(err, ErrClosed) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			defer stop()
			ready <- struct{}{}
			<-start
			for range 50 {
				if err := store.Do("increment", func(v *int) error { *v++; return nil }); err != nil {
					t.Error(err)
				}
				_ = i.Snapshot()
			}
		})
	}
	for range 8 {
		<-ready
	}
	close(start)
	mustUpdate(t, store.Do("before close", func(v *int) error { *v++; return nil }))
	mustUpdate(t, i.Close())
	wg.Wait()
	mustUpdate(t, i.Close())
	if _, err := Attach(i, "late", store); !errors.Is(err, ErrClosed) {
		t.Fatalf("attach after close = %v", err)
	}
	if err := i.Apply("late", json.RawMessage(`0`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("apply after close = %v", err)
	}
	if len(i.Snapshot().Stores) != 0 {
		t.Fatal("Close retained attached stores")
	}
}

func TestConcurrentRestoreNeverTargetsReplacementStore(t *testing.T) {
	for range 2000 {
		inspector := New(Options{AllowRestore: true})
		old := nodra.New(17)
		detach := mustAttach(t, inspector, "counter", old)
		sequence := inspector.Snapshot().Events[0].Sequence
		replacement := nodra.New(99)
		ready, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			var first sync.Once
			for {
				select {
				case <-stop:
					done <- nil
					return
				default:
				}
				err := inspector.Restore("counter", sequence)
				first.Do(func() { close(ready) })
				if err != nil && !errors.Is(err, ErrNotFound) {
					done <- err
					return
				}
			}
		}()
		<-ready
		detach()
		mustAttach(t, inspector, "counter", replacement)
		close(stop)
		mustUpdate(t, <-done)
		if got := replacement.Get(); got != 99 {
			t.Fatalf("old snapshot restored into replacement: %d", got)
		}
		mustUpdate(t, inspector.Close())
	}
}
