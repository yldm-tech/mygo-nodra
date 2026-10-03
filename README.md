# mygo-nodra

**Nodra** is a production-oriented reactive state store for Go desktop applications. It gives MyGo apps Zustand-like ergonomics without coupling the core to MyGo, a renderer, or a persistence backend.

## Install

```sh
go get github.com/yldm-tech/mygo-nodra
```

```go
import nodra "github.com/yldm-tech/mygo-nodra"

tasks := nodra.New([]Task{})
stop := tasks.SubscribeWith(nodra.SubscribeOptions[[]Task]{Immediate: true}, func(snapshot nodra.Snapshot[[]Task]) {
	window.Invalidate()
})
defer stop()

tasks.Update(func(value *[]Task) {
	*value = append(*value, task)
})
```

## What is included

- Generic, concurrency-safe `Store[T]` with versioned snapshots
- `Set`, `Update`, and `UpdateErr` with rollback on returned errors and panics
- `Subscribe` and `SubscribeWith` with immediate delivery and per-subscriber equality
- `SubscribeSelector` for focused updates
- Nested `Batch` notification groups that publish one final snapshot
- `Transaction` for one serialized, fallible mutation
- `Track` history with bounded undo and redo
- `Persistence[T]` with pluggable storage and codec interfaces
- Ready-to-use `FileStorage` and `JSONCodec[T]` with atomic file replacement
- `AutoSaveWithErrors` for observable persistence failures
- Context-bound, bounded `Watch` streams that close with the store
- `integrations/mygo` adapter for `Window.Invalidate`

## Design guarantees

- Store locks are released before user listeners run, so listeners can safely read or update the same store.
- Versions increment once per published update, not once per mutation inside a batch.
- Closing a store is permanent; updates return `nodra.ErrClosed` and active watches terminate.
- Values are not deep copied. Use immutable value conventions or copy slices and maps at the update boundary.
- Watch channels are best effort and bounded; slow consumers may skip intermediate snapshots.

## Repository layout

```text
store.go / store_test.go       core store and concurrency tests
history.go / history_test.go   bounded undo/redo
persist.go / persist_test.go   storage and codec integration
file.go / file_test.go         atomic JSON file persistence
integrations/mygo/             MyGo native window adapter
examples/basic/                standalone usage example
examples/mygo/                 runnable native MyGo example
docs/architecture.md           lifecycle and threading details
```

## Verification

```sh
go test -race ./...
go vet ./...
```

Run the native MyGo example with `go run ./examples/mygo`.
