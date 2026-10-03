// Package mygo connects Nodra stores to MyGo native windows without making
// the core store depend on the MyGo runtime.
package mygo

import (
	"github.com/egoist/mygo"
	"github.com/yldm-tech/mygo-nodra"
)

// SubscribeInvalidation requests a new native frame whenever store publishes
// a snapshot. The returned function disconnects the subscription.
func SubscribeInvalidation[T any](store *nodra.Store[T], window *mygo.Window) func() {
	if store == nil || window == nil {
		return func() {}
	}
	return store.Subscribe(func(nodra.Snapshot[T]) { window.Invalidate() })
}
