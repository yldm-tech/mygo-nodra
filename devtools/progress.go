package devtools

import (
	"time"

	"github.com/yldm-tech/nodra"
)

// At most one pending progress payload and timer per retained running task. All timer ownership and retention changes are protected by Inspector.mu.
type taskProgress struct {
	last       time.Time
	pending    Event
	hasPending bool
	timer      *time.Timer
}

func (i *Inspector) forgetProgressLocked(node *attached, id uint64) {
	if progress := node.progress[id]; progress != nil && progress.timer != nil {
		progress.timer.Stop()
	}
	delete(node.progress, id)
}

func (i *Inspector) cancelProgressLocked(node *attached) {
	for id := range node.progress {
		i.forgetProgressLocked(node, id)
	}
}

func (i *Inspector) recordTaskLocked(node *attached, event Event) {
	i.forgetProgressLocked(node, event.ActionID)
	event.Sequence = i.next + 1
	if !i.retainTaskLocked(node, event) {
		return
	}
	i.appendLocked(event)
	node.revision = i.revision
	if i.options.TaskProgressInterval > 0 && event.Phase == nodra.TaskRunning {
		if node.progress == nil {
			node.progress = make(map[uint64]*taskProgress)
		}
		node.progress[event.ActionID] = &taskProgress{last: time.Now()}
	}
}

func (i *Inspector) flushProgress(node *attached, id uint64, progress *taskProgress) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.paused || i.stores[node.view.ID] != node || node.progress[id] != progress || !progress.hasPending {
		return
	}
	event := progress.pending
	i.recordTaskLocked(node, event)
}
