package nodra

import "time"

func (a *AsyncAction[T, I, R]) markTime(target *time.Time) {
	a.mu.Lock()
	*target = time.Now()
	a.mu.Unlock()
}

func elapsedUntil(start, end, now time.Time) time.Duration {
	if start.IsZero() || start.After(now) {
		return 0
	}
	if end.IsZero() || end.After(now) {
		end = now
	}
	return max(0, end.Sub(start))
}

func (a *AsyncAction[T, I, R]) metrics(task *Task) TaskMetrics {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := task.finishedAt
	if now.IsZero() {
		now = time.Now()
	}
	metrics := TaskMetrics{
		Debounce:      elapsedUntil(task.event.Started, task.readyAt, now),
		Queue:         elapsedUntil(task.readyAt, task.acquiredAt, now),
		Work:          elapsedUntil(task.workStarted, task.workFinished, now),
		Commit:        elapsedUntil(task.commitStarted, task.commitFinished, now),
		ActiveWorkers: a.active,
	}
	for _, pending := range a.pending {
		if pending.ctx.Err() == nil {
			metrics.QueueDepth++
		}
	}
	return metrics
}
