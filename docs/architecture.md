# Nodra architecture

Nodra has one state owner and explicit integration boundaries:

```text
                 ┌─ selector subscribers
application ───► Store[T] ─┼─ normal subscribers ─► UI invalidation
                 ├─ History
                 ├─ Persistence
                 └─ Watch channels
```

## Store lifecycle

`New` creates an open store at version zero. `Set` and `Update` publish a new
snapshot. `UpdateErr` restores the previous value and publishes nothing when the
callback returns an error or panics. `Batch` groups nested notifications and publishes
once at the outermost boundary. Concurrent writers may join an open batch; use
`Transaction` for one serialized mutation. A closed store rejects future writes with
`ErrClosed`, removes subscribers and closes active `Watch` streams.

## Notifications

Listeners run synchronously on the writer goroutine after the store lock is
released. This makes a listener safe to call `Get`, `Update`, `Close` or
unsubscribe, while preserving an ordered callback sequence for one writer.
Different concurrent writers may invoke listeners concurrently. Applications
that need serialized side effects should use their own queue or mutex.

`SubscribeWith` can deliver the initial snapshot and can compare values per
subscriber. `SubscribeSelector` is a convenience for comparable projections.

## Persistence

Persistence is explicit. `Storage` owns bytes and `Codec[T]` owns encoding.
`FileStorage` and `JSONCodec[T]` provide an atomic JSON file path out of the box. The
core package does not assume a filesystem, JSON, database or encryption scheme.
`AutoSaveWithErrors` exposes save failures through a bounded error channel;
`AutoSave` is available when an application intentionally wants best-effort
saving.

## MyGo integration

`packages/mygo` imports MyGo and only connects a store subscription to
`Window.Invalidate`. The core Store remains usable in other Go UI toolkits,
services and tests without importing MyGo.

## API mapping from Pinia and Zustand

Nodra intentionally maps concepts, not framework syntax:

| Pinia / Zustand | Nodra | Notes |
|---|---|---|
| `create` / `createStore` | `Create(initial)` | Generic store value is explicit Go state |
| Pinia `defineStore(id, options)` | `Define(id, initial).New()` | Factory creates independent named instances |
| `getState` | `Get` / `State` / `Snapshot` | Snapshot includes a monotonic version |
| `set` | `Set` / `Update` | Errors are explicit and writes are concurrency safe |
| `$patch(fn)` | `Patch(fn)` / `Batch(fn)` | One publication for grouped changes |
| `$reset()` | `Reset()` | Restores the initial value |
| `getters` / computed | `Derive(store, selector, equals)` | Memoized derived value with subscriptions |
| `subscribeWithSelector` | `SubscribeSelectorWith` | Current and previous selected values |
| Pinia `actions` | `Action` / `Do` | Named action events include duration and errors |
| Pinia `$onAction` | `SubscribeActionHooks` | Before, after and error callbacks |
| `$subscribe` | `Subscribe` / `SubscribeMutations` | Mutation metadata includes type and previous state |
| persist middleware | `Persistence`, `JSONCodec`, `FileStorage` | Storage and encoding remain replaceable |

Go has no component hooks or proxy-based mutable objects. A MyGo view reads a
snapshot during its render and subscribes to invalidation through
`packages/mygo`; background work calls `Update` and the adapter requests a
new native frame.
