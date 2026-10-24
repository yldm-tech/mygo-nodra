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
callback returns an error or panics. With `New`, this is a shallow value rollback: the store
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

`NewWithClone` provides a second ownership policy. It clones initial state into
independent current/reset values, clones update candidates before user code runs,
and clones again on commit so retaining a candidate cannot modify owned state.
Reads and each notification receive separate copies, including mutation Previous
and Value metadata. Failed callbacks and failed commit clones never replace the
owned state or publish; the store lock is released even if a clone panics. Reset,
batch metadata and undo/redo retain their previous values with mutable references.
The clone function is application-supplied, must copy all mutable nested data,
must be concurrency-safe, and must not reenter the store. The default policy and
its zero-copy reference semantics remain available through `New` and `Define`;
`DefineWithClone` propagates isolation to each named instance.

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

### Schema migration

`VersionedJSONCodec` owns the schema envelope and migration chain, so the same
logic works with file, encrypted or database storage. Its positive target
version is written as `schemaVersion` alongside `state`. Migration n upgrades
only the state payload from n to n+1. Opt-in unversioned payloads start at zero;
`schemaVersion` is a reserved top-level key for envelope detection. Future and
negative versions are rejected with `ErrUnsupportedVersion`, missing steps with
`ErrMissingMigration`. Malformed envelopes and invalid migration output fail too.
All steps and final typed decoding complete before `Persistence.Load` calls Set.
Migration errors wrap their original cause and identify the failing step. No
failed decode changes state, emits a store update, or writes storage. Load itself
does not rewrite successfully migrated data; Save or autosave persists the target
schema. Configuration and the migration map must not be mutated concurrently.

### Async write-behind

`Persistence.AutoSaveAsync` owns one worker and initially schedules the current
state. Store notifications only request a version and wake the worker through a
bounded signal channel; they never encode or wait on storage. A trailing-edge
debounce merges bursts. The worker uses the persistence save lock to snapshot,
encode and write, then acknowledges the version actually written. Changes that
arrive during a write cause a follow-up write of the latest snapshot. No queue of
full state snapshots accumulates. Cloned stores provide isolated snapshots for
encoding; default stores must keep referenced data immutable.

Flush captures a target version, bypasses debounce, and waits for an acknowledgement
at or beyond it. Overlapping flushes share writes. Canceling an individual flush
wait does not cancel the worker. A write failure is observable through Errors and
the active flush wait; the state remains pending and a subsequent change or Flush
retries it. There is no unbounded automatic retry loop. Errors has capacity eight
and drops excess entries, so callers requiring a reliable result must use Flush
or Close. These barriers do not provide cross-process durability or transactions
with independent persistence instances.

Close stops subscriptions before capturing the final target, flushes, cancels the
worker and waits for shutdown within its context. It always stops, including on
write errors; repeated and concurrent closes return the first close result. A
concurrent caller can abandon its own wait without canceling another caller's
close. Finish mutating the store before Close. Cancel the parent and close the
store afterward: those two signals stop the worker without guaranteeing a drain.
A backend must honor its context for timely termination. If Close reports a
failure, the store still owns the data; retry with Persistence.Save using a fresh
context or surface the error before exiting.

## MyGo integration

`Invalidate(store, window)` connects a store subscription to `Window.Invalidate` and returns the function that stops it. It lives in the root package, so the module requires MyGo, but the Store never calls into MyGo itself: it works without a running MyGo app, in services and in tests.

## DevTools integration

The optional `devtools` subpackage depends on the core and observes exported
snapshot, mutation and action APIs. The core does not import the inspector,
HTTP transport or embedded assets. Explicit attachment IDs allow heterogeneous
stores; generation IDs prevent restoring a previous instance after detach and
reattach. Captured current state is guarded by source versions, while the bounded
timeline preserves callback arrival order. All JSON exports own their buffers.
The loopback browser UI and authenticated API share this model. State restoration
strictly decodes and calls Set outside inspector locks, so normal subscriptions
and persistence can run. Redaction occurs before retention and disables restoring
masked state. See [DevTools](devtools.md) for setup and operational semantics.

## API mapping from Pinia and Zustand

Nodra intentionally maps concepts, not framework syntax:

| Pinia / Zustand | Nodra | Notes |
|---|---|---|
| `create` / `createStore` | `Create(initial)` | Generic store value is explicit Go state |
| immutable helpers / owned drafts | `NewWithClone(initial, clone)` | Optional isolated references and rollback |
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
| persisted schema migrations | `VersionedJSONCodec` | Sequential steps, legacy opt-in, future-version rejection |
| debounced persistence | `AutoSaveAsync`, `Flush`, `Close` | One coalescing worker with explicit durability barriers |

Go has no component hooks or proxy-based mutable objects. A MyGo view reads a snapshot during its render and subscribes to invalidation with `Invalidate`; background work calls `Update` and the subscription requests a new native frame.
