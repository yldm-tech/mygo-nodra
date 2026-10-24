package devtools

import (
	"context"
	"testing"
	"time"

	"github.com/yldm-tech/nodra"
)

func TestTaskCaptureProgressRetentionPauseAndOutcome(t *testing.T) {
	i := New(Options{Limit: 3})
	defer func() { _ = i.Close() }()
	store := nodra.New(0)
	mustAttach(t, i, "search", store)
	entered, release := make(chan struct{}), make(chan struct{})
	action, err := nodra.NewAsyncAction(store, "search", func(_ context.Context, _ int, report func(float64)) (int, error) {
		report(.5)
		close(entered)
		<-release
		return 5, nil
	}, func(s *int, v int) error { *s = v; return nil })
	mustUpdate(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	task, err := action.Run(context.Background(), 0)
	mustUpdate(t, err)
	<-entered
	for n := range 10 {
		mustUpdate(t, store.Set(n))
	}
	snapshot := i.Snapshot()
	if len(snapshot.Events) != 3 || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Progress != .5 || snapshot.Tasks[0].Phase != nodra.TaskRunning {
		t.Fatalf("active task lost during eviction: %+v", snapshot.Tasks)
	}
	unchanged := i.Changes(snapshot.Revision, snapshot.Cursor, 50)
	if unchanged.TasksChanged || len(unchanged.Tasks) != 0 {
		t.Fatal("idle poll retransmitted tasks")
	}
	mustUpdate(t, i.SetCapture(true, 0))
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mustUpdate(t, task.Wait(ctx))
	mustUpdate(t, i.SetCapture(false, 0))
	snapshot = i.Snapshot()
	if snapshot.Tasks[0].Phase != "unobserved" || snapshot.Tasks[0].Committed {
		t.Fatalf("invented paused outcome: %+v", snapshot.Tasks[0])
	}
	if string(snapshot.Stores[0].State) != "5" {
		t.Fatal("resume did not refresh state")
	}
}

func TestTaskCaptureAttachesDuringWorkAndLinksCommit(t *testing.T) {
	store := nodra.New(0)
	entered, release := make(chan struct{}), make(chan struct{})
	action, err := nodra.NewAsyncAction(store, "load", func(context.Context, int, func(float64)) (int, error) { close(entered); <-release; return 4, nil }, func(s *int, v int) error { *s = v; return nil })
	mustUpdate(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	task, err := action.Run(context.Background(), 0)
	mustUpdate(t, err)
	<-entered
	i := New(Options{})
	defer func() { _ = i.Close() }()
	mustAttach(t, i, "store", store)
	if len(i.Snapshot().Tasks) != 1 {
		t.Fatal("attachment missed active task")
	}
	close(release)
	mustUpdate(t, task.Wait(context.Background()))
	snapshot := i.Snapshot()
	last := snapshot.Tasks[0]
	if last.Phase != nodra.TaskSucceeded || !last.Committed || last.Version != 1 {
		t.Fatalf("outcome: %+v", last)
	}
	var linked bool
	for _, event := range snapshot.Events {
		if event.Kind == "mutation" && event.ActionID == last.ActionID && event.Version == last.Version {
			linked = true
		}
	}
	if !linked {
		t.Fatal("task not linked to its mutation")
	}
}

func TestTaskSummariesAreBoundedAndRejectDelayedTraces(t *testing.T) {
	i := New(Options{Limit: 2, MaxHistoryBytes: 1024})
	defer func() { _ = i.Close() }()
	mustAttach(t, i, "source", nodra.New(0))
	node := i.stores["source"]
	for id := uint64(1); id <= 4; id++ {
		i.recordTrace(node, nodra.ActionTrace{ID: id, RunID: id, Phase: nodra.TaskRunning, TaskVersion: 1, At: time.Now()})
	}
	i.recordTrace(node, nodra.ActionTrace{ID: 1, Phase: nodra.TaskRunning, TaskVersion: 10})
	i.recordTrace(node, nodra.ActionTrace{ID: 4, Phase: nodra.TaskSucceeded, TaskVersion: 2})
	i.recordTrace(node, nodra.ActionTrace{ID: 4, Phase: nodra.TaskRunning, TaskVersion: 1})
	snapshot := i.Snapshot()
	if len(snapshot.Tasks) != 2 || node.taskBytes > 1024 || snapshot.Stores[0].TasksDropped != 2 {
		t.Fatalf("task retention: %+v bytes=%d", snapshot, node.taskBytes)
	}
	for _, event := range snapshot.Tasks {
		if event.ActionID == 1 || (event.ActionID == 4 && event.Phase != nodra.TaskSucceeded) {
			t.Fatal("delayed trace resurrected task")
		}
	}
}
