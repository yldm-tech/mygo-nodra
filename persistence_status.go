package nodra

import "errors"

// PersistenceState describes this async writer's durable acknowledgement, not writes made through other Persistence instances. HasSaved distinguishes an unsaved initial version zero. Pending remains true after failures and shutdown with unsaved changes. Error is the last write failure, cleared on retry. Revision orders publications independently of Store versions.
type PersistenceState struct {
	Revision         uint64
	RequestedVersion uint64
	SavedVersion     uint64
	HasSaved         bool
	Pending          bool
	Saving           bool
	Closed           bool
	Error            string
}

// Status exposes a read-only observable suitable for MyGo invalidation and DevTools. Subscribers run synchronously outside writer locks; do not Flush or Close this writer from its own subscribers. Subscriber panics cannot stop persistence or shutdown.
func (a *AsyncPersistence[T]) Status() ReadOnly[PersistenceState] { return a.state.ReadOnly() }

var errPersistenceStatusUnchanged = errors.New("nodra: persistence status unchanged")

func (a *AsyncPersistence[T]) publishStatus() {
	a.mu.Lock()
	state := PersistenceState{Revision: a.revision, RequestedVersion: a.requested, SavedVersion: a.saved, HasSaved: a.hasSaved, Pending: a.pendingLocked(), Saving: a.saving, Closed: a.stopped}
	if a.lastErr != nil && !a.saving {
		state.Error = a.lastErr.Error()
	}
	a.mu.Unlock()
	_ = taskBoundary(func() error {
		return a.state.UpdateErr(func(current *PersistenceState) error {
			if current.Revision >= state.Revision {
				return errPersistenceStatusUnchanged
			}
			*current = state
			return nil
		})
	})
}
