package nodra

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var (
	ErrSuperseded    = errors.New("nodra: async action superseded")
	ErrInvalidAction = errors.New("nodra: invalid async action")
	ErrQueueFull     = errors.New("nodra: async action queue full")
)

type ActionPolicy uint8

const (
	LatestWins ActionPolicy = iota
	QueueAll
)

// ActionOptions configures cooperative work scheduling. LatestWins (default) replaces only the preceding task with the same Key; nil Key uses one shared key. QueueAll keeps every accepted task. MaxConcurrent bounds work including canceled work that has not returned; zero is unbounded for LatestWins and defaults to one for QueueAll. QueueLimit bounds waiting tasks (default 64 when bounded). Starts are FIFO; with concurrency above one commits follow completion order. Key must be pure and concurrency-safe.
type ActionOptions[I any] struct {
	Policy        ActionPolicy
	Key           func(I) string
	MaxConcurrent int
	QueueLimit    int
	// Debounce delays LatestWins work per key until this interval after acceptance. Superseded inputs never start work. Zero preserves immediate scheduling; QueueAll rejects a positive delay.
	Debounce time.Duration
}

// AsyncState describes the latest run. Progress is between zero and one; Error is diagnostic text. Treat the action's status store as read-only.
type AsyncState struct {
	Metrics  TaskMetrics
	ID       uint64
	Name     string
	Running  bool
	Progress float64
	Error    string
	Phase    TaskPhase
	Key      string
	Revision uint64
}

// Task is one run of an AsyncAction. Cancel requests cooperative cancellation; Wait waits for work and its completion notifications, independently of the waiter's context. Do not Wait from the task's own work, hooks or synchronous subscribers.
type Task struct {
	ID             uint64
	ctx            context.Context
	cancel         context.CancelCauseFunc
	done           chan struct{}
	err            error
	key            string
	state          *Store[AsyncState]
	event          ActionEvent
	hooks          []ActionHooks
	started        bool
	ready          bool
	readyAt        time.Time
	acquiredAt     time.Time
	workStarted    time.Time
	workFinished   time.Time
	commitStarted  time.Time
	commitFinished time.Time
	finishedAt     time.Time
}

func (t *Task) Status() ReadOnly[AsyncState] { return t.state.ReadOnly() }

func (t *Task) Cancel()               { t.cancel(context.Canceled) }
func (t *Task) Done() <-chan struct{} { return t.done }
func (t *Task) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-t.done:
		return t.err
	default:
	}
	select {
	case <-t.done:
		return t.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AsyncAction runs work outside the Store lock with an explicit commit policy. The default accepts only the latest uncanceled run per key. Input and result ownership follows Store's policy; copy mutable inputs before Run. Work must honor its context. Progress, status subscribers and action hooks can run on background goroutines.
type AsyncAction[T, I, R any] struct {
	store       *Store[T]
	name        string
	work        func(context.Context, I, func(float64)) (R, error)
	apply       func(*T, R) error
	state       *Store[AsyncState]
	mu          sync.Mutex
	runs        map[*Task]struct{}
	next        uint64
	closed      bool
	closedCh    chan struct{}
	options     ActionOptions[I]
	latestByKey map[string]*Task
	pending     []*Task
	active      int
	changed     chan struct{}
}

func NewAsyncAction[T, I, R any](store *Store[T], name string, work func(context.Context, I, func(float64)) (R, error), apply func(*T, R) error) (*AsyncAction[T, I, R], error) {
	return NewAsyncActionWithOptions(store, name, work, apply, ActionOptions[I]{})
}

func NewAsyncActionWithOptions[T, I, R any](store *Store[T], name string, work func(context.Context, I, func(float64)) (R, error), apply func(*T, R) error, options ActionOptions[I]) (*AsyncAction[T, I, R], error) {
	if store == nil || work == nil || apply == nil || options.MaxConcurrent < 0 || options.QueueLimit < 0 || options.Policy > QueueAll || options.Debounce < 0 || (options.Policy == QueueAll && options.Debounce > 0) {
		return nil, ErrInvalidAction
	}
	if options.Policy == QueueAll && options.MaxConcurrent == 0 {
		options.MaxConcurrent = 1
	}
	if options.QueueLimit == 0 && (options.MaxConcurrent > 0 || options.Debounce > 0) {
		options.QueueLimit = 64
	}
	a := &AsyncAction[T, I, R]{store: store, name: name, work: work, apply: apply, state: New(AsyncState{Name: name}), runs: make(map[*Task]struct{}), closedCh: make(chan struct{})}
	a.options = options
	a.latestByKey = make(map[string]*Task)
	a.changed = make(chan struct{})
	go func() {
		select {
		case <-store.done:
			a.shutdown()
		case <-a.closedCh:
		}
	}()
	return a, nil
}

// Status returns the observable status of the latest run, suitable for MyGo invalidation or DevTools attachment. Callers must not write to this store.
func (a *AsyncAction[T, I, R]) Status() ReadOnly[AsyncState] { return a.state.ReadOnly() }

// Run accepts work according to ActionOptions. LatestWins supersedes the preceding task with the same key; QueueAll retains every accepted task. Canceled and superseded results never commit. ErrQueueFull rejects work without canceling existing tasks.
func (a *AsyncAction[T, I, R]) Run(ctx context.Context, input I) (*Task, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := ""
	if a.options.Key != nil {
		key = a.options.Key(input)
	}
	event, hooks := a.store.newAction(a.name)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	select {
	case <-a.store.done:
		a.mu.Unlock()
		return nil, ErrClosed
	default:
	}
	previous := a.latestByKey[key]
	queued := 0
	for _, pending := range a.pending {
		if pending.ctx.Err() == nil && (a.options.Policy != LatestWins || pending != previous) {
			queued++
		}
	}
	if a.options.QueueLimit > 0 && queued >= a.options.QueueLimit {
		a.mu.Unlock()
		return nil, ErrQueueFull
	}
	if previous != nil && a.options.Policy == LatestWins {
		previous.cancel(ErrSuperseded)
	}
	a.next++
	runCtx, cancel := context.WithCancelCause(ctx)
	event.RunID, event.Key = a.next, key
	initial := AsyncState{ID: a.next, Name: a.name, Running: true, Phase: TaskQueued, Key: key, Metrics: TaskMetrics{QueueDepth: queued + 1, ActiveWorkers: a.active}}
	event.Started = time.Now()
	task := &Task{ID: a.next, ctx: runCtx, cancel: cancel, done: make(chan struct{}), key: key, state: New(initial), event: event, hooks: hooks, ready: a.options.Debounce == 0, readyAt: event.Started.Add(a.options.Debounce)}
	a.latestByKey[key] = task
	a.runs[task] = struct{}{}
	a.pending = append(a.pending, task)
	a.signalLocked()
	a.mu.Unlock()
	// Notifications may reenter Run; the ID check prevents an older initialization from replacing a newer status.
	initErr := taskBoundary(func() error {
		return a.state.Update(func(s *AsyncState) {
			if s.ID < task.ID {
				*s = initial
			}
		})
	})
	if initErr == nil {
		initErr = taskBoundary(func() error { a.trace(task, initial, nil); return nil })
	}
	go a.run(task, input, initErr)
	return task, nil
}

func (a *AsyncAction[T, I, R]) run(task *Task, input I, initErr error) {
	event, hooks := task.event, task.hooks
	err := taskBoundary(func() error {
		if initErr != nil {
			return initErr
		}
		if a.options.Debounce > 0 {
			timer := time.NewTimer(max(0, time.Until(task.readyAt)))
			defer timer.Stop()
			select {
			case <-task.ctx.Done():
				return context.Cause(task.ctx)
			case <-timer.C:
			}
			a.mu.Lock()
			task.ready = true
			a.signalLocked()
			a.mu.Unlock()
		}
		if err := a.acquire(task); err != nil {
			return err
		}
		if err := a.status(task, func(s *AsyncState) { s.Phase = TaskRunning }); err != nil {
			return err
		}
		if cause := context.Cause(task.ctx); cause != nil {
			return cause
		}
		event.Metrics = a.metrics(task)
		for _, hook := range hooks {
			if hook.Before != nil {
				hook.Before(event)
			}
		}
		if cause := context.Cause(task.ctx); cause != nil {
			return cause
		}
		a.markTime(&task.workStarted)
		result, err := func() (R, error) {
			defer a.markTime(&task.workFinished)
			return a.work(task.ctx, input, func(progress float64) {
				if math.IsNaN(progress) || math.IsInf(progress, 0) || task.ctx.Err() != nil {
					return
				}
				if err := a.status(task, func(s *AsyncState) {
					if s.Running {
						s.Progress = max(s.Progress, min(1, max(0, progress)))
					}
				}); err != nil {
					task.cancel(err)
				}
			})
		}()
		if cause := context.Cause(task.ctx); cause != nil {
			return cause
		}
		if err != nil {
			return err
		}
		// Hold the generation lock through commit cloning, then release before Store notifications. Failed mutations and clone panics release it in the defer too.
		a.markTime(&task.commitStarted)
		defer a.markTime(&task.commitFinished)
		locked := false
		defer func() {
			if locked {
				a.mu.Unlock()
			}
		}()
		return a.store.updateCommitted(a.name, MutationAction, func(s *T) error {
			a.mu.Lock()
			locked = true
			if a.options.Policy == LatestWins && a.latestByKey[task.key] != task {
				return ErrSuperseded
			}
			if a.closed {
				return ErrClosed
			}
			if cause := context.Cause(task.ctx); cause != nil {
				return cause
			}
			return a.apply(s, result)
		}, func(version uint64) {
			event.Version = version
			event.Committed = true
			task.event = event
			a.mu.Unlock()
			locked = false
		}, event.ID)
	})
	a.markTime(&task.finishedAt)
	event.Metrics = a.metrics(task)
	if hookErr := taskBoundary(func() error { a.store.finishAction(event, hooks, err); return nil }); hookErr != nil {
		err = errors.Join(err, hookErr)
	}
	// Status failures cannot strand a task or prevent Close from releasing it.
	if statusErr := taskBoundary(func() error {
		return a.status(task, func(s *AsyncState) {
			s.Running = false
			s.Phase = taskPhase(err)
			if err == nil {
				s.Progress = 1
			} else {
				s.Error = err.Error()
			}
		})
	}); statusErr != nil && !errors.Is(statusErr, ErrClosed) {
		err = errors.Join(err, statusErr)
		_ = taskBoundary(func() error {
			return a.status(task, func(s *AsyncState) { s.Running = false; s.Phase = TaskFailed; s.Error = err.Error() })
		})
	}
	task.cancel(err)
	_ = task.state.Close()
	a.mu.Lock()
	a.removePendingLocked(task)
	if task.started {
		a.active--
	}
	delete(a.runs, task)
	if a.latestByKey[task.key] == task {
		delete(a.latestByKey, task.key)
	}
	if a.closed && len(a.runs) == 0 {
		_ = a.state.Close()
	}
	task.err = err
	close(task.done)
	a.signalLocked()
	a.mu.Unlock()
}

func taskPhase(err error) TaskPhase {
	if err == nil {
		return TaskSucceeded
	}
	if errors.Is(err, ErrSuperseded) {
		return TaskSuperseded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClosed) {
		return TaskCanceled
	}
	return TaskFailed
}

func (a *AsyncAction[T, I, R]) status(task *Task, update func(*AsyncState)) error {
	stateErr := taskBoundary(func() error {
		// Sample under the status lock so concurrent progress cannot publish an older elapsed duration with a newer revision. No action lock holder waits on this task's status lock.
		return task.state.Update(func(s *AsyncState) { update(s); s.Metrics = a.metrics(task); s.Revision++ })
	})
	state := task.state.Get()
	globalErr := taskBoundary(func() error {
		return a.state.Update(func(s *AsyncState) {
			if s.ID == task.ID && s.Revision <= state.Revision {
				*s = state
			}
		})
	})
	traceErr := taskBoundary(func() error { a.trace(task, state, nil); return nil })
	return errors.Join(stateErr, globalErr, traceErr)
}

func (a *AsyncAction[T, I, R]) trace(task *Task, state AsyncState, err error) {
	if err == nil && state.Error != "" {
		err = errors.New(state.Error)
	}
	a.mu.Lock()
	event := task.event
	a.mu.Unlock()
	// Order the source's active-task bookkeeping with terminal status, then release every lock before trace subscribers can reenter.
	task.state.mu.RLock()
	if task.state.value.Revision > state.Revision {
		task.state.mu.RUnlock()
		return
	}
	trace, listeners := a.store.prepareTrace(ActionTrace{ID: event.ID, RunID: task.ID, StoreID: event.StoreID, Name: a.name, Key: task.key, Phase: state.Phase, Progress: state.Progress, TaskVersion: state.Revision, Version: event.Version, Committed: event.Committed, At: time.Now(), Duration: time.Since(event.Started), Metrics: state.Metrics, Err: err})
	task.state.mu.RUnlock()
	for _, listener := range listeners {
		listener(trace)
	}
}

func (a *AsyncAction[T, I, R]) signalLocked() { close(a.changed); a.changed = make(chan struct{}) }
func (a *AsyncAction[T, I, R]) removePendingLocked(task *Task) {
	for index, pending := range a.pending {
		if pending == task {
			copy(a.pending[index:], a.pending[index+1:])
			a.pending[len(a.pending)-1] = nil
			a.pending = a.pending[:len(a.pending)-1]
			return
		}
	}
}
func (a *AsyncAction[T, I, R]) acquire(task *Task) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for {
		if cause := context.Cause(task.ctx); cause != nil {
			return cause
		}
		firstReady := (*Task)(nil)
		for _, pending := range a.pending {
			if pending.ready && pending.ctx.Err() == nil {
				firstReady = pending
				break
			}
		}
		if firstReady == task && (a.options.MaxConcurrent == 0 || a.active < a.options.MaxConcurrent) {
			a.removePendingLocked(task)
			a.active++
			task.started = true
			task.acquiredAt = time.Now()
			a.signalLocked()
			return nil
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-task.ctx.Done():
		case <-changed:
		}
		a.mu.Lock()
	}
}

// Close prevents new runs, cancels every active run and waits for cooperative work within ctx. The status store closes after the final run's terminal status. A timed-out Close can be called again to wait for remaining work. Do not Close synchronously from work, hooks or subscribers of this action; cancel there and close from the owner instead.
func (a *AsyncAction[T, I, R]) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tasks := a.shutdown()
	for _, task := range tasks {
		select {
		case <-task.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (a *AsyncAction[T, I, R]) shutdown() []*Task {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.closedCh)
	}
	tasks := make([]*Task, 0, len(a.runs))
	for task := range a.runs {
		task.cancel(ErrClosed)
		tasks = append(tasks, task)
	}
	a.mu.Unlock()
	if len(tasks) == 0 {
		_ = a.state.Close()
	}
	return tasks
}

// PanicError reports a panic at the asynchronous user-code boundary. The Store may already have committed if a notification or completion hook panicked.
type PanicError struct{ Value any }

func (e *PanicError) Error() string { return fmt.Sprintf("nodra: async action panic: %v", e.Value) }

func taskBoundary(fn func() error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &PanicError{Value: value}
		}
	}()
	return fn()
}
