package devtools

import (
	"sort"
	"time"

	"github.com/yldm-tech/nodra"
)

func traceEvent(node *attached, trace nodra.ActionTrace) Event {
	message := ""
	if trace.Err != nil {
		message = trace.Err.Error()
	}
	return Event{StoreID: node.view.ID, Generation: node.view.Generation, Kind: "task", ActionID: trace.ID, RunID: trace.RunID, Key: trace.Key, Name: trace.Name, Phase: trace.Phase, Progress: trace.Progress, TaskVersion: trace.TaskVersion, TraceRevision: trace.Revision, Version: trace.Version, Committed: trace.Committed, At: trace.At, Duration: trace.Duration, Error: message}
}

func (i *Inspector) tasksLocked() []Event {
	tasks := []Event{}
	for _, node := range i.stores {
		tasks = append(tasks, node.tasks...)
	}
	sort.Slice(tasks, func(a, b int) bool { return tasks[a].At.After(tasks[b].At) })
	return tasks
}

// Task summaries have a separate per-store count/byte budget matching Options.Limit/MaxHistoryBytes. Retaining summaries independently keeps running work visible when the mutation timeline rolls over. A floor prevents delayed notifications from resurrecting evicted tasks.
func (i *Inspector) retainTaskLocked(node *attached, event Event) bool {
	if event.ActionID <= node.taskFloor {
		return false
	}
	index := -1
	for n, previous := range node.tasks {
		if previous.ActionID == event.ActionID {
			if event.TaskVersion <= previous.TaskVersion {
				return false
			}
			index = n
			break
		}
	}
	size := eventBytes(event)
	if size > i.options.MaxHistoryBytes {
		node.view.TasksDropped++
		i.revision++
		node.revision = i.revision
		return false
	}
	if index >= 0 {
		node.taskBytes -= eventBytes(node.tasks[index])
		node.tasks[index] = event
	} else {
		node.tasks = append(node.tasks, event)
		sort.Slice(node.tasks, func(a, b int) bool { return node.tasks[a].ActionID < node.tasks[b].ActionID })
	}
	node.taskBytes += size
	i.trimTasksLocked(node)
	i.revision++
	node.revision = i.revision
	return event.ActionID > node.taskFloor
}

func (i *Inspector) trimTasksLocked(node *attached) {
	for len(node.tasks) > 0 && (len(node.tasks) > i.options.Limit || node.taskBytes > i.options.MaxHistoryBytes) {
		node.taskBytes -= eventBytes(node.tasks[0])
		node.taskFloor = node.tasks[0].ActionID
		node.tasks[0] = Event{}
		node.tasks = node.tasks[1:]
		node.view.TasksDropped++
	}
}

func (i *Inspector) recordTrace(node *attached, trace nodra.ActionTrace) {
	if !i.allowCapture(node, false) {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.paused || i.stores[node.view.ID] != node {
		return
	}
	event := traceEvent(node, trace)
	event.Sequence = i.next + 1
	if !i.retainTaskLocked(node, event) {
		return
	}
	i.appendLocked(event)
	node.revision = i.revision
}

// A resumed inspector can recover currently active tasks but cannot reconstruct outcomes omitted during pause. Mark those summaries explicitly instead of claiming success or cancellation.
func (i *Inspector) refreshTasks(node *attached, snapshot nodra.TaskSnapshot) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.paused || i.stores[node.view.ID] != node {
		return
	}
	active := make(map[uint64]bool, len(snapshot.Tasks))
	for _, trace := range snapshot.Tasks {
		active[trace.ID] = true
		if i.retainTaskLocked(node, traceEvent(node, trace)) {
			i.revision++
			node.revision = i.revision
		}
	}
	for n, event := range node.tasks {
		if (event.Phase == nodra.TaskQueued || event.Phase == nodra.TaskRunning) && !active[event.ActionID] && event.TraceRevision <= snapshot.Revision {
			event.Phase = "unobserved"
			event.Error = ""
			event.At = time.Now().UTC()
			node.taskBytes += eventBytes(event) - eventBytes(node.tasks[n])
			node.tasks[n] = event
			i.revision++
			node.revision = i.revision
		}
	}
	i.trimTasksLocked(node)
}
