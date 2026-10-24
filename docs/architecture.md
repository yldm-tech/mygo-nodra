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
callback returns an error or panics. This is a shallow value rollback: the store
does not clone maps, slices or pointers. Copy referenced data before mutating it,
so that errors, panics, `Reset` and retained snapshots keep their expected values.
For example:

```go
err := store.UpdateErr(func(state *State) error {
    items := maps.Clone(state.Items)
    items["word"] = updatedItem
    state.Items = items
    return validate(*state)
})
```

This example assumes the map values do not themselves contain mutable shared
data; nested reference types need copying too. Do not mutate data returned by
`Get` or `Snapshot` outside an update, and do not retain a mutable alias passed
to `New` or `Set` for later mutation.

`Batch` groups nested notifications and publishes
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
Selectors and derived getters recheck the source after subscribing to catch
changes between the initial read and registration. They track source versions
to discard outdated computations, including when the latest selected value is
equal to the cached value. Intermediate changes during initialization may be
coalesced. This does not serialize user callbacks: callbacks from different
writers can still overlap, and a callback that has already started may finish
after one for a newer version.

## Persistence

Persistence is explicit. `Storage` owns bytes and `Codec[T]` owns encoding.
`FileStorage` and `JSONCodec[T]` provide an atomic JSON file path out of the box. The
core package does not assume a filesystem, JSON, database or encryption scheme.
`AutoSaveWithErrors` exposes save failures through a bounded error channel;
`AutoSave` is available when an application intentionally wants best-effort
saving.

Each `Persistence` instance serializes `Save` calls, taking the current state
only after acquiring the save lock and holding it through encoding and storage.
This prevents a slow older encode or write from replacing a newer save. Use
one persistence instance per storage destination; separate instances and direct
storage calls are not coordinated. `Load` is a startup operation, not a
transaction with concurrent store updates. It publishes the loaded value after
decoding, so autosave subscriptions can safely save it again.

Stopping `AutoSaveWithErrors` unsubscribes, closes the error channel and releases
its background watcher even when the context and store remain open. An already
running save may still finish after stop; stop does not drain pending writes.

## MyGo integration

`Invalidate(store, window)` connects a store subscription to `Window.Invalidate` and returns the function that stops it. It lives in the root package, so the module requires MyGo, but the Store never calls into MyGo itself: it works without a running MyGo app, in services and in tests.

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

Go has no component hooks or proxy-based mutable objects. A MyGo view reads a snapshot during its render and subscribes to invalidation with `Invalidate`; background work calls `Update` and the subscription requests a new native frame.
