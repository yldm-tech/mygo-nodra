package devtools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yldm-tech/nodra"
)

func TestIncrementalChangesAndHistoryPagination(t *testing.T) {
	i := New(Options{Limit: 6})
	defer func() { _ = i.Close() }()
	store := nodra.New(0)
	detach, err := Attach(i, "counter", store)
	mustUpdate(t, err)
	initial := i.Changes(0, 0, 2)
	if !initial.Reset || len(initial.Stores) != 1 || len(initial.Events) != 1 {
		t.Fatalf("initial: %+v", initial)
	}
	unchanged := i.Changes(initial.Revision, initial.Cursor, 2)
	if unchanged.Reset || len(unchanged.Stores) != 0 || len(unchanged.Events) != 0 {
		t.Fatalf("unchanged poll retransmitted payloads: %+v", unchanged)
	}
	for n := 1; n <= 7; n++ {
		mustUpdate(t, store.Set(n))
	}
	page := i.Changes(0, 0, 2)
	if len(page.Events) != 2 || page.Events[0].Sequence != 7 || page.More || page.Cursor != 8 || page.Oldest != 3 {
		t.Fatalf("latest page: %+v", page)
	}
	older := i.EventsBefore(page.Events[0].Sequence, 2)
	if len(older.Events) != 2 || older.Events[0].Sequence != 5 || !older.More {
		t.Fatalf("older: %+v", older)
	}
	catchup := i.Changes(initial.Revision, initial.Cursor, 2)
	if !catchup.More || len(catchup.Events) != 2 || catchup.Cursor != 4 {
		t.Fatalf("catch-up: %+v", catchup)
	}
	next := i.Changes(catchup.Revision, catchup.Cursor, 2)
	if len(next.Stores) != 0 || len(next.Events) != 2 || next.Events[0].Sequence != 5 {
		t.Fatalf("next: %+v", next)
	}
	page.Stores[0].State[0] = 'x'
	page.Events[0].State[0] = 'x'
	snapshot := i.Snapshot()
	if string(snapshot.Stores[0].State) != "7" || !json.Valid(snapshot.Events[4].State) {
		t.Fatal("incremental payload aliased inspector")
	}
	detach()
	removed := i.Changes(snapshot.Revision, snapshot.Cursor, 2)
	if len(removed.StoreIDs) != 0 || removed.Revision <= snapshot.Revision {
		t.Fatal("detach missing from incremental poll")
	}
	i.Clear()
	cleared := i.Changes(removed.Revision, removed.Cursor, 2)
	if !cleared.Reset || len(cleared.Events) != 0 {
		t.Fatalf("clear did not reset cache: %+v", cleared)
	}
}

func TestCapturePolicySkipsEncodingAndResumesCurrentState(t *testing.T) {
	var encodes atomic.Int64
	i := New(Options{SampleInterval: time.Hour, Redact: func(_ string, raw json.RawMessage) (json.RawMessage, error) { encodes.Add(1); return raw, nil }})
	defer func() { _ = i.Close() }()
	store := nodra.New(0)
	mustAttach(t, i, "counter", store)
	mustUpdate(t, store.Set(1))
	before := encodes.Load()
	mustUpdate(t, store.Set(2))
	if encodes.Load() != before {
		t.Fatal("sampled update still encoded state")
	}
	mustUpdate(t, i.SetCapture(true, time.Hour))
	mustUpdate(t, store.Set(3))
	if encodes.Load() != before || store.Get() != 3 {
		t.Fatal("pause encoded or blocked application state")
	}
	if got := i.Snapshot(); string(got.Stores[0].State) != "1" || !got.Capture.Paused || got.Capture.Skipped != 2 {
		t.Fatalf("paused capture: %+v", got)
	}
	mustUpdate(t, i.SetCapture(false, 0))
	if got := i.Snapshot(); string(got.Stores[0].State) != "3" || got.Capture.Paused || encodes.Load() != before+1 {
		t.Fatalf("resume did not catch up: %+v", got)
	}
}

func TestIncrementalHTTPAndCaptureValidation(t *testing.T) {
	i := New(Options{})
	defer func() { _ = i.Close() }()
	mustAttach(t, i, "counter", nodra.New(0))
	for _, path := range []string{"/api/changes?revision=-1", "/api/changes?limit=201", "/api/events?before=bad"} {
		if w := request(i, "GET", path, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	for _, body := range []string{`{"sampleIntervalMS":-1}`, `{"sampleIntervalMS":60001}`, `{"unknown":true}`, `null trailing`} {
		if w := request(i, "POST", "/api/capture", body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
	if w := request(i, "POST", "/api/capture", `{"paused":true,"sampleIntervalMS":100}`); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	first := request(i, "GET", "/api/changes", "")
	var page Changes
	mustUpdate(t, json.Unmarshal(first.Body.Bytes(), &page))
	if !page.Capture.Paused || len(page.Stores) != 1 {
		t.Fatalf("HTTP changes: %+v", page)
	}
	path := fmt.Sprintf("/api/changes?revision=%d&cursor=%d", page.Revision, page.Cursor)
	var unchanged Changes
	mustUpdate(t, json.Unmarshal(request(i, "GET", path, "").Body.Bytes(), &unchanged))
	if len(unchanged.Stores) != 0 || len(unchanged.Events) != 0 {
		t.Fatal("HTTP unchanged poll retransmitted state")
	}
	if w := request(i, "GET", "/api/events?limit=1", ""); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
}

func BenchmarkIncrementalIdlePoll(b *testing.B) {
	i := New(Options{})
	defer func() { _ = i.Close() }()
	store := nodra.New(make([]int, 10000))
	_, err := Attach(i, "large", store)
	if err != nil {
		b.Fatal(err)
	}
	snapshot := i.Snapshot()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		i.Changes(snapshot.Revision, snapshot.Cursor, 50)
	}
}
