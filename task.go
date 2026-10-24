package nodra

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

var (
	ErrSuperseded    = errors.New("nodra: async action superseded")
	ErrInvalidAction = errors.New("nodra: invalid async action")
)

// AsyncState describes the latest run. Progress is between zero and one; Error is diagnostic text. Treat the action's status store as read-only.
type AsyncState struct {
	ID       uint64
	Name     string
	Running  bool
	Progress float64
	Error    string
}

// Task is one run of an AsyncAction. Cancel requests cooperative cancellation; Wait waits for work and its completion notifications, independently of the waiter's context. Do not Wait from the task's own work, hooks or synchronous subscribers.
type Task struct {
	ID     uint64
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}
	err    error
}

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

// AsyncAction runs work outside the Store lock and commits only the latest uncanceled run. Input and result ownership follows Store's policy; copy mutable inputs before Run. Work must honor its context. Progress, status subscribers and action hooks can run on background goroutines.
type AsyncAction[T, I, R any] struct {
	store    *Store[T]
	name     string
	work     func(context.Context, I, func(float64)) (R, error)
	apply    func(*T, R) error
	state    *Store[AsyncState]
	mu       sync.Mutex
	latest   *Task
	runs     map[*Task]struct{}
	next     uint64
	closed   bool
	closedCh chan struct{}
}

func NewAsyncAction[T, I, R any](store *Store[T], name string, work func(context.Context, I, func(float64)) (R, error), apply func(*T, R) error) (*AsyncAction[T, I, R], error) {
	if store == nil || work == nil || apply == nil {
		return nil, ErrInvalidAction
	}
	a := &AsyncAction[T, I, R]{store: store, name: name, work: work, apply: apply, state: New(AsyncState{Name: name}), runs: make(map[*Task]struct{}), closedCh: make(chan struct{})}
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

// Run cancels the preceding run and starts a new one. A stale or canceled result never enters the Store. Canceling a Task does not stop a different run.
func (a *AsyncAction[T, I, R]) Run(ctx context.Context, input I) (*Task, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	if a.latest != nil {
		a.latest.cancel(ErrSuperseded)
	}
	a.next++
	runCtx, cancel := context.WithCancelCause(ctx)
	task := &Task{ID: a.next, ctx: runCtx, cancel: cancel, done: make(chan struct{})}
	a.latest = task
	a.runs[task] = struct{}{}
	a.mu.Unlock()
	// Notifications may reenter Run; the ID check prevents an older initialization from replacing a newer status.
	initErr := taskBoundary(func() error {
		return a.state.Update(func(s *AsyncState) {
			if s.ID < task.ID {
				*s = AsyncState{ID: task.ID, Name: a.name, Running: true}
			}
		})
	})
	go a.run(task, input, initErr)
	return task, nil
}

func (a *AsyncAction[T, I, R]) run(task *Task, input I, initErr error) {
	event, hooks := a.store.newAction(a.name)
	err := taskBoundary(func() error {
		if initErr != nil {
			return initErr
		}
		if cause := context.Cause(task.ctx); cause != nil {
			return cause
		}
		for _, hook := range hooks {
			if hook.Before != nil {
				hook.Before(event)
			}
		}
		if cause := context.Cause(task.ctx); cause != nil {
			return cause
		}
		result, err := a.work(task.ctx, input, func(progress float64) {
			if math.IsNaN(progress) || math.IsInf(progress, 0) || task.ctx.Err() != nil {
				return
			}
			_ = a.state.Update(func(s *AsyncState) {
				if s.ID == task.ID && s.Running {
					s.Progress = max(s.Progress, min(1, max(0, progress)))
				}
			})
		})
		if cause := context.Cause(task.ctx); cause != nil {
			return cause
		}
		if err != nil {
			return err
		}
		// Hold the generation lock through commit cloning, then release before Store notifications. Failed mutations and clone panics release it in the defer too.
		locked := false
		defer func() {
			if locked {
				a.mu.Unlock()
			}
		}()
		return a.store.updateCommitted(a.name, MutationAction, func(s *T) error {
			a.mu.Lock()
			locked = true
			if a.latest != task {
				return ErrSuperseded
			}
			if a.closed {
				return ErrClosed
			}
			if cause := context.Cause(task.ctx); cause != nil {
				return cause
			}
			return a.apply(s, result)
		}, func(uint64) { a.mu.Unlock(); locked = false })
	})
	if hookErr := taskBoundary(func() error { a.store.finishAction(event, hooks, err); return nil }); hookErr != nil {
		err = errors.Join(err, hookErr)
	}
	// Status failures cannot strand a task or prevent Close from releasing it.
	if statusErr := taskBoundary(func() error {
		return a.state.Update(func(s *AsyncState) {
			if s.ID != task.ID {
				return
			}
			s.Running = false
			if err == nil {
				s.Progress = 1
			} else {
				s.Error = err.Error()
			}
		})
	}); statusErr != nil && !errors.Is(statusErr, ErrClosed) {
		err = errors.Join(err, statusErr)
	}
	task.cancel(err)
	a.mu.Lock()
	delete(a.runs, task)
	if a.closed && len(a.runs) == 0 {
		_ = a.state.Close()
	}
	task.err = err
	close(task.done)
	a.mu.Unlock()
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
