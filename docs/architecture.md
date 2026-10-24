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
Different concurrent writers may invoke listeners concurrently. Subscriptions
discard obsolete snapshots before accepting them, but a callback already in
progress can still finish after one for a newer version. Applications that need
serialized side effects should use their own queue or mutex.

`SubscribeWith` can deliver the initial snapshot and can compare values per subscriber. `SubscribeSelector` accepts any projection type. The default uses `==` only when both dynamic values are comparable (preserving pointer identity), otherwise `reflect.DeepEqual`; interfaces containing slices or maps are safe. `SelectorOptions.Equals` replaces the default comparison entirely. Default deep equality distinguishes nil and empty collections. Large collections should use a suitable custom comparator, such as `slices.Equal` or `maps.Equal`. Projection references remain subject to the source store ownership policy and must not be mutated outside an update. Selectors and derived getters recheck the source after subscribing to catch changes between the initial read and registration. They track source versions to discard outdated computations, including when the latest selected value is equal to the cached value. Intermediate changes during initialization may be coalesced. This does not serialize user callbacks: callbacks from different writers can still overlap, and a callback that has already started may finish after one for a newer version. Equality functions run outside subscription and derived-cache locks, so a comparison panic propagates without poisoning the subscription. Comparisons may run concurrently and retry against a newer baseline; use pure, concurrency-safe comparators. A comparison failure leaves the previous projection and its version unchanged. The source write has already committed when a notification or comparison panics. If immediate delivery or initial catch-up panics during subscription, derived or history construction, its registered source listener is removed before the panic propagates.

## History

`Track` checks the source again after registering and ignores obsolete versions.
Changes during initialization or delivered out of order may coalesce; history
does not promise an entry for every concurrent mutation. Undo and redo catch up
with committed source state before selecting a target. Stack changes and the
new history version commit under the source write lock, after a successful commit
clone and before notifications. Closed-store errors and clone panics therefore
leave the undo/redo stacks intact. Concurrent travel and travel reentered from
a listener follow those same commit boundaries.

Undo/redo inside an open `Batch` returns `ErrHistoryBatch` without changing state
or stacks. Complete the batch first: restoring state needs its own publication
boundary. Closing history releases its subscription without closing the store.

## Persistence

Persistence is explicit. `Storage` owns bytes and `Codec[T]` owns encoding.
`FileStorage` and `JSONCodec[T]` provide an atomic JSON file path out of the box. The
core package does not assume a filesystem, JSON, database or encryption scheme.
`AutoSaveWithErrors` exposes save failures through a bounded error channel;
`AutoSave` is available when an application intentionally wants best-effort
saving.

Each `Persistence` instance serializes `Save` calls, taking the current state only after acquiring the save lock and holding it through encoding and storage. This prevents a slow older encode or write from replacing a newer save. Use one persistence instance per storage destination with ordinary FileStorage; separate instances and direct storage calls are not coordinated unless all writers explicitly use CheckedFileStorage. `Load` is a startup operation, not a transaction with concurrent store updates. It publishes the loaded value after decoding, so autosave subscriptions can safely save it again.

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

`Invalidate(store, window)` connects a store subscription to `Window.Invalidate`. `InvalidateSelector` and `InvalidateSelectorWith` instead subscribe to a projection with the existing selector equality, initialization catch-up and version ordering. The selector must include every business-state value displayed by that window. This filters requests for native frames; MyGo still owns rendering, and there is no automatic tracking of field reads.

All three helpers register a window-close listener and return an idempotent stop function that removes both registrations. A destroyed window subscribes to nothing. A second destruction check catches a close between the initial check and listener registration; late subscription handles are released immediately if closing races with initialization. Initialization panics release the close listener and propagate unchanged. Stop prevents callbacks not yet past the active check from requesting a frame, but does not drain callbacks already running. Hiding a window preserves its subscriptions. Closing or detaching one window does not close the Store or affect other windows.

Widget inputs bind pointers to local snapshot fields; commit only edited fields with `Store.Update` so unrelated concurrent changes survive. Keep transient drafts and widget state in the UI owner. Store subscriptions and selectors run on writer goroutines, while MyGo's `Window.Invalidate` safely posts the frame request to its main thread. Other UI-owned changes still require `Window.Update`.

The helpers live in the root package, so the module requires MyGo, but the Store never calls into MyGo itself: it works without a running MyGo app, in services and in tests.

## DevTools integration

The optional `devtools` subpackage depends on the core and observes exported
snapshot, mutation and action APIs. The core does not import the inspector,
HTTP transport or embedded assets. Explicit attachment IDs allow heterogeneous
stores; generation IDs prevent restoring a previous instance after detach and
reattach, including replacement racing an in-flight restore. A restore retains
its original attachment identity through application; it cannot resolve a reused
ID to a new target. Captured current state is guarded by source versions, while
the bounded timeline preserves callback arrival order. All JSON exports own their buffers.
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

## Async work and commit boundary

`AsyncAction` has a generation lock independent of its source Store. A run does work without either lock, then takes the source write lock and generation lock to check ownership and apply its result. The generation lock remains held through commit cloning and is released before Store notifications. Failed mutations and clone panics release both locks without a partial commit. Starting a newer run cannot pass an older run's accepted commit boundary; newer work never has its results overwritten by an obsolete run. Cancellation is cooperative for work, and external effects are outside the rollback boundary. The status Store and source action events are separate observations, so they do not form a multi-store transaction.

Each run owns its context and completion channel. Source closure requests cancellation; explicit Close prevents new runs, cancels all runs and waits within the owner's context. The final run closes the status Store only after its terminal status is committed. Work, status subscribers and action hooks are asynchronous user-code boundaries: panics become task errors. A task cannot wait for its own completion from a synchronous callback. Work that ignores cancellation can outlive a timed-out Close; callers can close again to wait after releasing that work.

## Projection ownership and cost

`Select` is an explicit borrowed-read API. Its callback executes under the source read lock, does not receive a full-state clone, and must return owned projection data without mutating or retaining source references. `SubscribeProjectionWith` borrows immutable committed snapshots and reuses selector equality, version ordering, initialization catch-up and panic cleanup. Existing reads and selectors retain full-state isolation. Normal subscriptions no longer retain/copy an equality baseline when no equality function is configured. Benchmarks cover 10,000-entry reads and concurrent updates with one or ten normal/projected selectors.

## Edit history policy

`TrackWith` can compare only relevant fields and merge recorded values into current state on travel. Ignored changes advance the observed source version without adding a step or invalidating redo. Restore must explicitly merge ignored fields when they should survive travel. Policy callbacks are pure and non-reentrant because history catches up while holding its lock and, during travel, the Store lock. BeginGroup keeps notifications live while recording only the first relevant baseline; groups nest and may span frames. All writers join an open group. Undo/redo during a group returns ErrHistoryGroup. Evicted and popped entries are zeroed so unused slice capacity does not retain old state references.


## Read capability and owned shutdown

`ReadOnly[T]` is a concrete wrapper with an unexported backing pointer. Its method set has no mutations, hooks or Close; converting it to Reader does not make the writable Store recoverable by a type assertion. Reader is sealed to Nodra sources so borrowed projections can use their commit-isolation guarantees. Observational helpers accept both writable Stores and read-only views. The status of an Action and each Task use this boundary; source lifetime remains with the owner.

Scope closes observations before LIFO drains, with context cancellation at the start. Registration and shutdown share one lock, but all user cleanup runs outside it. Late observations stop immediately; late drains receive the first shutdown context and return ErrClosed plus their own error. The first Close runs cleanup once; other callers wait on one completion channel or their own context. A deadline cannot preempt cleanup that ignores cancellation. Persistence workers that must flush use a context independent of Scope.Context. Scope has no implicit global registry or ownership of shared stores.

## Bounded task scheduling and tracing

Each action owns a FIFO of accepted tasks, a running-work count and latest ownership per key. Canceling work does not free its slot until that run exits. Queued tasks wait on change/cancellation channels rather than polling. Queue admission excludes canceled and replaceable queued tasks; rejection leaves existing contexts intact. QueueAll never applies latest-owner rejection. With one worker, acceptance order also determines commit order; concurrent work can complete in any order.

Task status revisions prevent overlapping progress notifications from replacing a newer global status. Preparing a source trace is ordered with that task's status lock; trace callbacks run after every lock is released. This prevents delayed progress from resurrecting a terminal source entry. Source active-task snapshots include a trace revision for attach/resume reconciliation. Terminal entries leave that map, while each Task owns its closed status snapshot. Action ID is allocated by the source; RunID belongs to one AsyncAction. Published mutation and completion metadata share the exact accepted commit version. Batches intentionally coalesce action identity at their later publication boundary.

## Recovery boundary

Persistence validation happens on isolated snapshots/decoded values before replacement, never by mutating Store-owned state. File backup is a second atomic file replacement after the primary; backup failure can therefore report a partially successful Save. Loading a backup is a read plus validated memory replacement and deliberately does not repair or delete the primary. Startup must decide how to handle a failed primary load before enabling autosave. The library does not silently choose a recovery policy. Plain FileStorage does not coordinate independent writers.

`FileStorage.WithConflictDetection` returns a pointer backend with a baseline SHA-256 content fingerprint and a cancelable per-instance gate. It canonicalizes the parent directory and uses `gofrs/flock` on a persistent `.lock` sidecar for cross-process compare-and-replace serialization. Lock handles close after every operation; the sidecar must remain while instances are active so all writers lock the same inode. A conflict returns expected/actual fingerprints without replacing primary or backup. Load explicitly accepts the current primary baseline, while LoadBackup leaves it unchanged. The primary-replaced callback advances this instance's baseline before attempting backup, preserving retry after a partial Save. These guarantees require cooperating writers at the same canonical regular local file. Noncooperating concurrent editors can race the check/rename, and content hashes do not detect equal-content or reverted changes. Symlink/nonregular primary files are rejected; hard-link aliases and network locks are outside the contract.

## Value composition, history budgets and progress control

Observable is a minimal snapshot/subscription boundary for Stores, read-only views and derived values. Derived snapshot subscribers receive source versions without exposing a writable Store. Combine protects its latest input pair and per-source versions with one private mutex; it releases that mutex before projecting or notifying. A combination's local version orders accepted input changes; it provides eventual consistency, not multi-source atomicity or glitch-free diamond graphs. Constructor catch-up and existing derived version checks discard obsolete work. Close releases source subscriptions, while derived references remain read-only under the existing ownership contract.

History caches application size estimates alongside undo/redo values and budgets both stacks. Estimators run before stack changes using the existing pure callback contract. Internal commit bookkeeping invokes no user callbacks, then availability is published to a separate read-only status Store after releasing both locks. A history revision prevents late publishers from replacing newer availability. Current state and metadata are outside the payload estimate.

Debounce timers wait outside worker slots. LatestWins retains an independent deadline per key and supersedes previous waiting work; only ready uncanceled pending tasks compete for slots. QueueAll rejects debounce instead of silently dropping accepted inputs. Close cancels timers through the normal task context and waits on task completion.

Inspector task progress coalescing is opt-in and independent of mutation sampling. A retained running task owns at most one bounded pending Event and trailing timer, guarded by Inspector.mu. Lifecycle changes bypass the interval; terminal capture, eviction and every detach/shutdown boundary release timer ownership. Timer callbacks recheck inspector state, attachment identity and timer ownership before recording, preventing delayed callbacks from reviving cleared, detached or completed work.
