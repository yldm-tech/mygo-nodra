package nodra

import "time"

type TaskPhase string

const (
	TaskQueued     TaskPhase = "queued"
	TaskRunning    TaskPhase = "running"
	TaskSucceeded  TaskPhase = "succeeded"
	TaskCanceled   TaskPhase = "canceled"
	TaskSuperseded TaskPhase = "superseded"
	TaskFailed     TaskPhase = "failed"
)

// ActionTrace observes asynchronous task lifecycles. ID is the source Store action ID; RunID is local to its AsyncAction. Revision orders trace notifications even when callbacks overlap. Version identifies a published commit when Committed is true. Progress is monotonic for one task. Completion ActionEvents remain a separate, backward-compatible stream.
type ActionTrace struct {
	Metrics     TaskMetrics
	ID          uint64
	RunID       uint64
	StoreID     string
	Name        string
	Key         string
	Phase       TaskPhase
	Progress    float64
	Revision    uint64
	TaskVersion uint64
	Version     uint64
	Committed   bool
	At          time.Time
	Duration    time.Duration
	Err         error
}

// TaskMetrics separates elapsed debounce, eligible queue wait, user work and commit attempt. Commit includes Store lock wait, cloning, apply and synchronous mutation notifications, even on failure. Work excludes Before hooks. These phases exclude other hooks/status callbacks and need not sum to Duration. QueueDepth counts live waiting tasks (including debounce); ActiveWorkers includes canceled work until it returns. Counts are sampled per event for this AsyncAction, not globally.
type TaskMetrics struct {
	Debounce      time.Duration `json:"debounce"`
	Queue         time.Duration `json:"queue"`
	Work          time.Duration `json:"work"`
	Commit        time.Duration `json:"commit"`
	QueueDepth    int           `json:"queueDepth"`
	ActiveWorkers int           `json:"activeWorkers"`
}

func (s *Store[T]) SubscribeActionTrace(fn func(ActionTrace)) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return func() {}
	}
	s.nextID++
	id := s.nextID
	s.traceListeners[id] = fn
	s.mu.Unlock()
	return func() { s.mu.Lock(); delete(s.traceListeners, id); s.mu.Unlock() }
}

func (s *Store[T]) prepareTrace(event ActionTrace) (ActionTrace, []func(ActionTrace)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traceRevision++
	event.Revision = s.traceRevision
	if !s.closed {
		if event.Phase == TaskQueued || event.Phase == TaskRunning {
			s.activeTasks[event.ID] = event
		} else {
			delete(s.activeTasks, event.ID)
		}
	}
	listeners := make([]func(ActionTrace), 0, len(s.traceListeners))
	for _, fn := range s.traceListeners {
		listeners = append(listeners, fn)
	}
	return event, listeners
}

// TaskSnapshot identifies current queued/running tasks and the source trace revision at capture time. Terminal tasks are released by the source; Task.Status retains their final state.
type TaskSnapshot struct {
	Revision uint64
	Tasks    []ActionTrace
}

func (s *Store[T]) TaskSnapshot() TaskSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ActionTrace, 0, len(s.activeTasks))
	for _, event := range s.activeTasks {
		result = append(result, event)
	}
	return TaskSnapshot{Revision: s.traceRevision, Tasks: result}
}

// ActiveTasks returns queued/running tasks independently of diagnostic sampling. The slice is owned by the caller.
func (s *Store[T]) ActiveTasks() []ActionTrace { return s.TaskSnapshot().Tasks }

func (r ReadOnly[T]) ActiveTasks() []ActionTrace {
	if r.store == nil {
		return nil
	}
	return r.store.ActiveTasks()
}

func (r ReadOnly[T]) TaskSnapshot() TaskSnapshot {
	if r.store == nil {
		return TaskSnapshot{}
	}
	return r.store.TaskSnapshot()
}

func (r ReadOnly[T]) SubscribeActionTrace(fn func(ActionTrace)) func() {
	if r.store == nil {
		return func() {}
	}
	return r.store.SubscribeActionTrace(fn)
}
