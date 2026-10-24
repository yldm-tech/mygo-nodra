package devtools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yldm-tech/nodra"
)

func TestHistoryByteBudgetEvictsOldPayloads(t *testing.T) {
	inspector := New(Options{Limit: 1000, MaxHistoryBytes: 2500})
	defer func() { _ = inspector.Close() }()
	store := nodra.New(strings.Repeat("a", 400))
	mustAttach(t, inspector, "large", store)
	for n := range 20 {
		mustUpdate(t, store.Set(strings.Repeat(string(rune('b'+n)), 400)))
	}
	snapshot := inspector.Snapshot()
	if len(snapshot.Events) >= 3 || snapshot.Retention.Dropped == 0 || snapshot.Retention.Bytes > 2500 {
		t.Fatalf("unbounded retention: %+v, events=%d", snapshot.Retention, len(snapshot.Events))
	}
	if snapshot.Stores[0].Version != 20 || snapshot.Events[len(snapshot.Events)-1].Version != 20 {
		t.Fatal("eviction lost the latest state")
	}
	inspector.Clear()
	snapshot = inspector.Snapshot()
	if snapshot.Retention.Bytes != 0 || snapshot.Retention.Dropped != 0 || len(snapshot.Events) != 0 || len(snapshot.Stores) != 1 {
		t.Fatal("clear did not reset retention")
	}
}

func TestOversizedStateDoesNotBreakSourceAndCanRecover(t *testing.T) {
	inspector := New(Options{AllowRestore: true, MaxStateBytes: 64})
	defer func() { _ = inspector.Close() }()
	store := nodra.New("small")
	mustAttach(t, inspector, "large", store)
	mustUpdate(t, store.Set(strings.Repeat("x", 1000)))
	view := inspector.Snapshot().Stores[0]
	if view.State != nil || view.Restorable || !strings.Contains(view.Error, "inspection limit") || len(store.Get()) != 1000 {
		t.Fatalf("oversized state=%+v", view)
	}
	mustUpdate(t, store.Set("recovered"))
	view = inspector.Snapshot().Stores[0]
	if view.Error != "" || !view.Restorable || string(view.State) != `"recovered"` {
		t.Fatalf("state did not recover=%+v", view)
	}
}

func TestStateLimitRunsAfterRedaction(t *testing.T) {
	inspector := New(Options{MaxStateBytes: 16, Redact: func(string, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`"masked"`), nil }})
	defer func() { _ = inspector.Close() }()
	mustAttach(t, inspector, "large", nodra.New(strings.Repeat("x", 1000)))
	if view := inspector.Snapshot().Stores[0]; view.Error != "" || string(view.State) != `"masked"` {
		t.Fatalf("redacted state=%+v", view)
	}
}

func TestSingleEventAboveHistoryBudgetKeepsCurrentState(t *testing.T) {
	inspector := New(Options{MaxHistoryBytes: 1})
	defer func() { _ = inspector.Close() }()
	store := nodra.New(1)
	mustAttach(t, inspector, "counter", store)
	mustUpdate(t, store.Set(2))
	snapshot := inspector.Snapshot()
	if len(snapshot.Events) != 0 || snapshot.Retention.Dropped != 2 || string(snapshot.Stores[0].State) != "2" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
