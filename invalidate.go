package nodra

import "github.com/egoist/mygo"

// Invalidate requests a new frame of window's native Content whenever store publishes a snapshot, so a MyGo view that reads the store during its render shows the new state. It returns the function that stops the subscription; with a nil store or window it subscribes to nothing and returns a no-op.
func Invalidate[T any](store *Store[T], window *mygo.Window) (stop func()) {
	if store == nil || window == nil {
		return func() {}
	}
	return store.Subscribe(func(Snapshot[T]) { window.Invalidate() })
}
