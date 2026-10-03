# mygo-nodra

**Nodra** is a small, concurrency-safe reactive state store for Go desktop applications. It provides Zustand-like ergonomics for Go while keeping the core independent from MyGo and any UI toolkit.

## Install

```sh
go get github.com/yldm-tech/mygo-nodra
```

```go
import nodra "github.com/yldm-tech/mygo-nodra"

tasks := nodra.New([]Task{})
stop := tasks.Subscribe(func(snapshot nodra.Snapshot[[]Task]) {
	// Ask your UI to redraw.
})
defer stop()

tasks.Update(func(value *[]Task) {
	*value = append(*value, task)
})
```

## Features

- Generic `Store[T]` with `Get`, `Snapshot`, `Set`, and `Update`
- Synchronous subscriptions after the store lock is released
- Nested `Batch` updates that publish one final snapshot
- `SubscribeSelector` for focused updates
- Context-bound, bounded `Watch` streams
- Safe concurrent reads and writes
- Optional MyGo adapter at `integrations/mygo`

## Repository layout

```text
store.go                 core Store[T] implementation
store_test.go            concurrency and behavior tests
integrations/mygo/       MyGo Window.Invalidate adapter
examples/basic/          standalone usage example
docs/architecture.md     design and threading model
```

Nodra does not deep-copy generic values. Use immutable value conventions or
copy slices and maps at the update boundary.

## Verification

```sh
go test -race ./...
go vet ./...
```
