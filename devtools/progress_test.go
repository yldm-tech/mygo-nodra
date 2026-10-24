package devtools

import (
	"errors"
	"testing"
	"time"

	"github.com/yldm-tech/nodra"
)

func traceForTest(id, version uint64, phase nodra.TaskPhase, progress float64) nodra.ActionTrace {
	return nodra.ActionTrace{ID: id, RunID: id, TaskVersion: version, Phase: phase, Progress: progress, At: time.Now()}
}

func TestProgressCoalescesAndPreservesLifecycle(t *testing.T) {
	i := New(Options{TaskProgressInterval: time.Hour, Limit: 50})
	defer func() { _ = i.Close() }()
	mustAttach(t, i, "source", nodra.New(0))
	node := i.stores["source"]
	i.recordTrace(node, traceForTest(1, 0, nodra.TaskQueued, 0))
	i.recordTrace(node, traceForTest(1, 1, nodra.TaskRunning, 0))
	for version := uint64(2); version <= 100; version++ {
		i.recordTrace(node, traceForTest(1, version, nodra.TaskRunning, float64(version)/101))
	}
	if got := i.Snapshot(); len(got.Events) != 3 || got.Stores[0].TaskProgressCoalesced != 99 {
		t.Fatalf("progress flood was retained: events=%d stores=%+v", len(got.Events), got.Stores)
	}
	i.mu.Lock()
	progress := node.progress[1]
	i.mu.Unlock()
	i.flushProgress(node, 1, progress)
	if got := i.Snapshot(); got.Tasks[0].TaskVersion != 100 || len(got.Events) != 4 {
		t.Fatal("trailing update was not the latest")
	}
	i.recordTrace(node, traceForTest(1, 101, nodra.TaskRunning, .999))
	terminal := traceForTest(1, 102, nodra.TaskSucceeded, 1)
	terminal.Committed = true
	terminal.Version = 9
	i.recordTrace(node, terminal)
	i.flushProgress(node, 1, progress)
	i.recordTrace(node, traceForTest(1, 99, nodra.TaskRunning, .5))
	got := i.Snapshot()
	if got.Tasks[0].Phase != nodra.TaskSucceeded || got.Tasks[0].Version != 9 || len(got.Events) != 5 || len(node.progress) != 0 {
		t.Fatalf("terminal lost: %+v", got.Tasks)
	}
}

func TestProgressTimerFlushAndErrorBypass(t *testing.T) {
	i := New(Options{TaskProgressInterval: 10 * time.Millisecond})
	defer func() { _ = i.Close() }()
	mustAttach(t, i, "source", nodra.New(0))
	node := i.stores["source"]
	i.recordTrace(node, traceForTest(1, 1, nodra.TaskRunning, 0))
	i.recordTrace(node, traceForTest(1, 2, nodra.TaskRunning, .4))
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for i.Snapshot().Tasks[0].TaskVersion != 2 {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("trailing progress timer did not flush")
		}
	}
	errTrace := traceForTest(1, 3, nodra.TaskRunning, .5)
	errTrace.Err = errors.New("diagnostic error")
	i.recordTrace(node, errTrace)
	if i.Snapshot().Tasks[0].Error != "diagnostic error" {
		t.Fatal("error trace was delayed")
	}
}

func TestProgressTimersAreBoundedAndCleaned(t *testing.T) {
	for _, cleanup := range []string{"pause", "clear", "detach", "close"} {
		t.Run(cleanup, func(t *testing.T) {
			i := New(Options{Limit: 2, TaskProgressInterval: time.Hour})
			defer func() { _ = i.Close() }()
			stop, err := Attach(i, "source", nodra.New(0))
			mustUpdate(t, err)
			node := i.stores["source"]
			for id := uint64(1); id <= 10; id++ {
				i.recordTrace(node, traceForTest(id, 1, nodra.TaskRunning, 0))
				i.recordTrace(node, traceForTest(id, 2, nodra.TaskRunning, .5))
			}
			if len(node.progress) > 2 {
				t.Fatal("timers exceeded retention budget")
			}
			switch cleanup {
			case "pause":
				mustUpdate(t, i.SetCapture(true, 0))
			case "clear":
				i.Clear()
			case "detach":
				stop()
			case "close":
				mustUpdate(t, i.Close())
			}
			if len(node.progress) != 0 {
				t.Fatal("cleanup retained timers")
			}
		})
	}
}
