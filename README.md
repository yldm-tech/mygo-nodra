# nodra

**Nodra** is a production-oriented reactive state store for Go desktop applications. It is a state library for [MyGo](https://github.com/egoist/mygo) apps with Zustand-like ergonomics, and does not tie your state to a renderer or a persistence backend.

## Install

```sh
go get github.com/yldm-tech/nodra@latest
```

The core API is in `github.com/yldm-tech/nodra`. The optional local inspector is
in `github.com/yldm-tech/nodra/devtools`, so applications can omit its server and UI.

## MyGo usage

The MyGo integration targets v0.6.3, pinned in `go.mod` and shared by the native example.

Redraw a native MyGo window whenever its store publishes:

```go
stop := nodra.Invalidate(store, window)
```

The subscription stops automatically when the window closes. Call `stop()` to detach it earlier; repeated calls are safe. A hidden window stays subscribed so it can be shown again. Each window has its own subscription, and closing one does not close the shared Store.

For a window that only displays part of the state, select that part so unrelated updates do not request a frame:

```go
stop := nodra.InvalidateSelector(store, window, func(s AppState) int {
    return s.Count
})
```

`InvalidateSelectorWith` accepts `SelectorOptions[V]`, including `Equals` for collections and `Immediate` to request a frame on attachment. It uses the same equality and ownership rules as `SubscribeSelectorWith`. Select every value the view displays; multiple windows can select different values from the same Store. This filters redraw requests, not individual UI elements, and does not track field reads automatically.

Keep local UI state, such as a draft or an open popover, in the view's Go owner or MyGo's `ui.Local`. For a widget that edits shared business state, bind a local snapshot field and commit only the changed field:

```go
enabled := store.Get().Enabled
if ui.Checkbox(c, &enabled, "Enable sync").Changed() {
    if err := store.Update(func(s *AppState) { s.Enabled = enabled }); err != nil {
        log.Print(err)
    }
}
```

Replacing the whole snapshot with `Set` here could overwrite another field changed by a background task. Subscribers and selectors run on writer goroutines, so update other UI-owned fields through `window.Update`; the invalidation helpers only request a goroutine-safe redraw. Stopping does not wait for an already running callback, and a selector panic during attachment releases the window listener before propagating.

`go run ./examples/mygo` demonstrates a counter and a preferences window with independent selectors, field-level widget commits, and a background action.

## Composing derived state

`Observable[T]` is the small versioned value interface shared by Stores, read-only views and derived values. `Derive`, selectors and MyGo invalidation accept it; `Derived.Subscribe` keeps its existing value callback, while `SubscribeSnapshot` exposes versions for composition.

These helpers now accept Observable instead of Reader. Ordinary calls remain valid; explicitly declared helper function types must update their parameter type. Reader retains the richer Store observation surface, while borrowed projections and DevTools attachment continue to require Reader.

```go
canEdit := nodra.Combine(user.ReadOnly(), permissions.ReadOnly(),
    func(u User, p Permissions) bool { return u.Active && p.Edit },
    func(a, b bool) bool { return a == b })
label := nodra.Derive(canEdit, func(allowed bool) string {
    if allowed { return "Edit" }
    return "View only"
}, func(a, b string) bool { return a == b })
stop := nodra.Invalidate(label, window)
// At owner shutdown: stop(), then label.Close(), then canEdit.Close().
```

Nest Combine calls for more inputs. Each source version advances independently: combinations converge to the latest observed inputs but are not cross-Store transactions. A diamond graph may emit an intermediate combination before both branches catch up. Keep values that require atomic updates in one Store. Constructors catch registration gaps; obsolete computations do not overwrite newer accepted results. Projectors and comparators must be pure and concurrency-safe, and derived reference values remain read-only. `DeriveProjection` can start a derived chain without full-source subscription clones. Closing a derived value detaches its inputs and freezes its last value; downstream owners still close their own derived values.

## Immediate field editing

`BindField` creates a small immediate binding; its `Edit` callback renders against a local value outside Store locks and commits only when the control reports a change:

```go
enabled, err := nodra.BindField(store,
    func(s AppState) bool { return s.Enabled },
    func(s *AppState, value bool) error { s.Enabled = value; return nil })
// Handle construction errors before rendering.
err = enabled.Edit(func(value *bool) bool {
    return ui.Checkbox(c, value, "Enable sync").Changed()
})
// Surface err in the view or the owner's error handler.
```

The setter runs on the latest state candidate, so unrelated background changes survive. A returned validation error rolls back the write according to the Store's clone policy. Use `NewWithClone` for mutable reference fields; `Get` takes a normal isolated source snapshot. Forms that need Save/Cancel should keep UI-local drafts as in the workbench, rather than committing every edit. The native counter preferences example exercises this binding with MyGo controls.

## Read-only state and lifecycle ownership

Pass `store.ReadOnly()` to UI code that should observe shared state without changing or closing it. The concrete wrapper hides its backing Store; returning an interface whose dynamic value is the writable Store would not provide that boundary. `Reader[T]` is the observation surface accepted by selectors, projections, derived getters, MyGo invalidation and DevTools attachment. A read-only DevTools source remains non-restorable even when inspector restoration is enabled. Read-only values retain the source's cloning/immutability policy; referenced data is not automatically frozen.

```go
view := store.ReadOnly()
nodra.Invalidate(view, window)
count := nodra.Select(view, func(s AppState) int { return len(s.Items) }).Value
```

`AsyncAction.Status()` now returns `ReadOnly[AsyncState]` instead of `*Store[AsyncState]`. Ordinary read/subscription calls continue to work. Change explicit status variable/parameter types to `ReadOnly[AsyncState]` or `Reader[AsyncState]`; remove writes or closes on status and close its owning action instead. Helpers that previously accepted `*Store[T]` now accept `Reader[T]`, so explicitly typed helper function variables may need the same parameter change.

Use a `Scope` for one window/module owner. Register stores first, persistence writers next, then actions; drains execute in reverse order. `Observe` owns unsubscribe functions separately so subscriptions stop before drains. A registration racing with shutdown is cleaned immediately and returns `ErrClosed`.

```go
scope := nodra.NewScope()
_ = scope.Defer(func(context.Context) error { return store.Close() })
_ = scope.Defer(writer.Close)
_ = scope.Defer(action.Close)
_ = scope.Observe(nodra.Invalidate(store.ReadOnly(), window))
task, err := action.Run(scope.Context(), input)
// Handle err. At owner shutdown, use a fresh deadline:
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
err = scope.Close(shutdownCtx)
```

Close cancels `scope.Context()`, stops observations, drains registered resources and preserves all cleanup errors. Concurrent callers share the shutdown result with independent wait deadlines; the first Close supplies the cleanup context. Callbacks must respect that context. Use an independent worker context for write-behind persistence so it can drain after the Scope context is canceled. Do not synchronously close a Scope from one of its own cleanup callbacks, or from a task it must drain. Shared stores can outlive a Scope by leaving their Close unregistered. The workbench demonstrates Scope ownership and window-bound observations.

## Async actions

`Action` and `Do` are synchronous mutations: their callbacks run under the Store write lock. Use `NewAsyncAction` for file/network work, then commit a short state mutation:

```go
search, err := nodra.NewAsyncAction(store, "search", func(ctx context.Context, query string, progress func(float64)) ([]Item, error) {
    return fetchItems(ctx, query)
}, func(s *AppState, items []Item) error {
    s.Items = items
    return nil
})
// Handle err before using search.
task, err := search.Run(ctx, query)
// Handle err; task.Cancel() requests cancellation, task.Wait(waitCtx) observes completion.
```

Each run cancels its predecessor and only the latest uncanceled result can commit, including when old work ignores cancellation. `Status()` is an observable `ReadOnly[AsyncState]` with a run ID, running flag, monotonic progress from zero to one, and error text. It exposes no mutation or close methods. Attach it to MyGo invalidation or DevTools like any Store. Work runs outside the state lock; the apply callback follows normal `UpdateErr` ownership and rollback rules. Copy mutable inputs before Run, or otherwise keep them immutable for the entire run.

Action hooks and events cover the work and commit, with a per-store action ID and duration. Panics at the asynchronous user-code boundary become `PanicError`; a notification/hook panic can happen after a successful commit. Cancellation prevents result commits, but cannot undo network/file side effects already performed. Work must honor its context. Closing the source Store cancels active runs. At owner shutdown, call `search.Close(shutdownCtx)` before closing stores; it cancels and waits, and closes the status Store after terminal status is published. A timed-out Close can be called again. Do not wait or close synchronously from that task's own work, hooks or subscribers.

### Scheduling and per-task status

`NewAsyncActionWithOptions` adds explicit scheduling without changing the default constructor's latest-result policy:

```go
action, err := nodra.NewAsyncActionWithOptions(store, "load item", work, apply,
    nodra.ActionOptions[Request]{
        Policy: nodra.LatestWins,
        Key: func(request Request) string { return request.ItemID },
        MaxConcurrent: 4,
        QueueLimit: 32,
    })
```

LatestWins replaces work only for the same key; different keys are independent. Use `QueueAll` for work that must retain every accepted input. QueueAll defaults to one worker for FIFO starts/commits; with more workers, starts are FIFO and commits follow completion order. LatestWins with zero MaxConcurrent preserves unbounded legacy behavior. Bounded or debounced actions default to 64 pending entries, and a full queue returns `ErrQueueFull` without canceling existing work. A superseded queued entry can be replaced even when that queue is full. Canceled work that has not returned still occupies its concurrency slot. Run accepts input without waiting for a work slot; canceled queued tasks finish without executing work.

Set `ActionOptions.Debounce` to delay `LatestWins` work until a quiet interval after acceptance for its key. Replacing an input cancels its timer and reports `superseded` without executing work. Different keys have independent deadlines. Waiting for debounce does not occupy a worker slot, and eligible keys can start while another key is still waiting. Queue limits include live debounced inputs; cancellation and Close drain waiting tasks. A positive Debounce with QueueAll returns `ErrInvalidAction`, because QueueAll retains every input. Zero preserves existing immediate scheduling. The workbench search uses a 150 ms debounce.

`Task.Status()` exposes that task's key, queued/running/terminal phase, progress and diagnostic error. The status Store closes after terminal status. The action-level Status describes the most recently accepted run, which is not a count of all active work. Use `ActiveTasks()` or `TaskSnapshot()` on the source for current work. Do not wait for another queued task synchronously from work, hooks or subscribers of the same bounded action; that can exhaust its own work slots. Progress may be reported concurrently during work; stop reporting when work returns.

`SubscribeActionTrace` observes async acceptance, start, progress and terminal outcome. Action ID links traces to mutation and completion events; RunID is local to one action. TaskVersion orders one task's status while Revision orders source trace publication; callbacks can still overlap. Committed/Version identify the accepted publication even if notifications later fail. A Store Batch coalesces publication across writers, so its final mutation has no single ActionID and individual batched actions do not claim a published commit version. Duration includes queue waiting time. Terminal task entries are released from the source's active map.

`Metrics` on task status, traces and completion events separates `Debounce`, eligible `Queue` wait, `Work` and `Commit`. Commit measures the entire Store update attempt, including lock wait, clone/apply and synchronous mutation notifications; failed attempts still have a duration. Work excludes Before hooks. Other hooks and status callbacks are outside these phases, so they need not sum to Duration. `QueueDepth` counts live waiting tasks including debounce, and `ActiveWorkers` includes canceled work until it returns. Counts are sampled for that AsyncAction at publication time and are not a global live gauge. Tasks UI and JSON exports carry the same metrics.

## Owned projections for large state

`Get` and ordinary selector subscriptions preserve the Store's full-state cloning policy. For hot reads, `Select` borrows read-only state and copies only the projection you return:

```go
count := nodra.Select(store, func(s AppState) int { return len(s.Items) }).Value
items := nodra.Select(store, func(s AppState) []Item { return slices.Clone(s.Items) }).Value
```

The callback runs under the read lock: do not mutate state, retain source references, reenter the Store or block. Copy nested mutable references too. The returned snapshot includes the source version. `SubscribeProjection` / `SubscribeProjectionWith` offer the same explicit ownership boundary for subscriptions without a whole-state copy per listener. Their projector runs on immutable source snapshots and must return independently owned projections; callbacks must treat current/previous projections as immutable. Default stores still require immutable referenced data. Existing `SubscribeSelector` remains the convenient isolated choice. Only opt in to projections where the smaller copy matters. `InvalidateProjection` / `InvalidateProjectionWith` apply the same policy to window-bound redraws; `DeriveProjection` memoizes an owned projection. These accept read-only views and preserve the projector contract throughout the binding/derived pipeline. Derived reference values and listener arguments are read-only.

## Grouped and filtered undo

```go
history := nodra.TrackWith(store, nodra.HistoryOptions[AppState]{
    Limit: 50,
    Equals: func(a, b AppState) bool { return a.Title == b.Title },
    Restore: func(current, recorded AppState) AppState {
        current.Title = recorded.Title
        return current
    },
})
end, err := history.BeginGroup() // keep end until the drag/edit interaction finishes
// Handle err. Updates still publish for each frame.
end()
```

`Equals` ignores changes outside the edited fields. `Restore` preserves their current values during undo/redo; filtering alone does not preserve fields when restoring whole snapshots. These callbacks must be pure, must not mutate arguments, and must not reenter the Store or History because they can run under locks. Nested groups merge into one undo step; repeated end calls are safe. Concurrent writers join an open group. Travel is rejected with `ErrHistoryGroup` until every group ends. An empty/filtered group adds no step. `Track(store, limit)` retains its existing behavior.

### History payload budgets and availability

`HistoryOptions.MaxBytes` limits the combined estimated payloads in undo and redo, in addition to the undo entry limit. Supply `Size func(T) int64` with an application-specific estimate; for a text document this can be the text length plus known owned payloads. Zero disables sizing and preserves the existing count-only behavior. Negative budgets, a positive budget without Size, or negative estimates panic with `ErrInvalidHistory` before accepting that invalid estimate. Size follows the existing pure/non-reentrant callback rules.

```go
history := nodra.TrackWith(document, nodra.HistoryOptions[Document]{
    Limit: 100,
    MaxBytes: 8 << 20,
    Size: func(d Document) int64 { return int64(len(d.Text)) },
})
stop := nodra.Invalidate(history.Status(), window)
canUndo := history.Status().Get().CanUndo
```

Oldest undo entries are evicted first, then oldest redo entries if necessary; an individually oversized entry is discarded. The estimate excludes the current history value, Store state, container overhead and shared allocations, so it is not a process heap limit. `History.Status()` is a read-only observable with counts, Bytes, Dropped, grouping and actionable CanUndo/CanRedo flags. Group start/end notify even without a source mutation. Status callbacks run outside both history and source locks, can reenter reads, and may overlap like Store callbacks. Close publishes Closed, releases retained entries, then closes the status source. The legacy CanUndo/CanRedo methods continue to report stack presence even during a group; the status flags additionally account for grouping.

## Desktop workflow example

`go run ./examples/workbench` opens a local file-name search and a settings form. It uses real directory traversal, cooperative search cancellation, bounded results, UI-local drafts, asynchronous validation and atomic settings-file writes. Invalid directories and failed writes leave the draft available and the committed preferences unchanged. Cancel edits reloads the committed preferences. The settings path defaults to the user configuration directory; override it with `-settings /path/to/settings.json`. Saves also maintain a `.bak` copy. Add `-devtools` to print a local read-only inspection URL for real search/save task lifecycles.

## Core usage

```go
import nodra "github.com/yldm-tech/nodra"

tasks := nodra.New([]Task{})
stop := tasks.SubscribeWith(nodra.SubscribeOptions[[]Task]{Immediate: true}, func(snapshot nodra.Snapshot[[]Task]) {
	log.Printf("%d tasks", len(snapshot.Value))
})
defer stop()

tasks.Update(func(value *[]Task) {
	*value = append(*value, task)
})
```

## What is included

- Generic, concurrency-safe `Store[T]` with versioned snapshots
- Opt-in `NewWithClone` / `DefineWithClone` for isolated mutable state and deep rollback
- `Set`, `Update`, and `UpdateErr` with value rollback on returned errors and panics
- `Subscribe` and `SubscribeWith` with immediate delivery and per-subscriber equality
- `SubscribeSelector` and `SubscribeSelectorWith` for focused updates with previous values
- `Derive` for memoized computed/getter values
- `Action` / `Do` with action timing and error events
- Latest-result asynchronous actions with cancellation, progress, observable status and explicit shutdown
- Opt-in owned projections that avoid whole-state copies for hot reads and subscriptions
- `SubscribeMutations` with direct, patch, reset and action metadata
- `Create`, `Define`, `State`, `Patch`, and `Reset` aliases for familiar Pinia/Zustand workflows
- Store IDs and independent named store instances
- Action lifecycle hooks: before, after, and error
- Nested `Batch` notification groups that publish one final snapshot
- `Transaction` for one serialized, fallible mutation
- `Track` / `TrackWith` history with bounded undo/redo, recording filters and cross-frame edit groups
- `Persistence[T]` with pluggable storage and codec interfaces
- Ready-to-use `FileStorage` and `JSONCodec[T]` with atomic file replacement
- `VersionedJSONCodec[T]` with sequential schema migrations and explicit legacy JSON support
- `AutoSaveWithErrors` for observable persistence failures
- `AutoSaveAsync` for debounced, coalesced writes with `Flush`, `Close` and an error stream
- Context-bound, bounded `Watch` streams that close with the store
- `Invalidate` and `InvalidateSelector` for window-bound redraw subscriptions with automatic cleanup
- Optional DevTools with a local browser inspector, mutation/action timeline,
  lazy state trees, key/value search, bounded state diffs, snapshot export, JSON editing and opt-in state restoration

## Design guarantees

- Store locks are released before user listeners run, so listeners can safely read or update the same store.
- Versions increment once per published update, not once per mutation inside a batch.
- Closing a store is permanent; updates return `nodra.ErrClosed` and active watches terminate.
- By default, values are not deep copied. Copy reference types before changing them, or use `NewWithClone` with a complete clone function. Cloned stores isolate constructor and `Set` inputs, update candidates, reads, each subscriber, mutation metadata and reset state.
- Selectors and derived getters catch up with changes during initialization and ignore computations from older versions once a newer version has been processed. Callbacks still run on writer goroutines and may run concurrently.
- Concurrent `Save` calls on one `Persistence` instance are serialized from state capture through storage write, so a delayed save cannot overwrite a newer save. Keep one persistence instance per destination; `Load` is intended for startup before concurrent writes.
- Subscriptions discard obsolete snapshots once they have accepted a newer source version. Callbacks that already started may still overlap or finish out of order. Watch channels are bounded and retain the newest accepted version; slow consumers may skip intermediate snapshots.
- History keeps source versions monotonic and commits stack changes with successful undo/redo writes. Failed writes leave stacks intact. Travel inside an open batch returns `ErrHistoryBatch`; finish the batch before undoing or redoing.

## Isolated mutable state

When state contains maps, slices or pointers, opt in to cloning instead of
maintaining copy-on-write manually:

```go
store := nodra.NewWithClone(map[string]int{"count": 0}, func(v map[string]int) map[string]int {
    return maps.Clone(v)
})
err := store.Transaction(func(state *map[string]int) error {
    (*state)["count"]++
    return validate(*state) // an error discards the candidate, including map changes
})
```

Clone all mutable nested references too. The function must support concurrent
calls and must not reenter the store. Each isolation boundary costs a copy; keep
using `New` for immutable state or large data already managed with copy-on-write.
Named factories support the same policy through `DefineWithClone`.

## Collection selectors

Selectors accept slices, maps and values containing interfaces. By default dynamically comparable values use `==` (including pointer identity); other values use `reflect.DeepEqual`. Nil and empty collections differ with the default comparison. Supply `Equals` for domain-specific equality or faster comparisons of large collections:

```go
stop := nodra.SubscribeSelectorWith(store,
    func(s State) []string { return s.Tags },
    func(current, previous []string) { window.Invalidate() },
    nodra.SelectorOptions[[]string]{Equals: slices.Equal[[]string]},
)
defer stop()
```

For maps use `maps.Equal[map[string]int, map[string]int]` or a custom comparator. Comparators must be pure and concurrency-safe; a supplied comparator replaces the default entirely. Selected reference values follow the source store's ownership policy: treat them as read-only and copy before changing them. `SubscribeSelector` offers the same default equality without previous values or options.

## Versioned persistence

`VersionedJSONCodec[T]` wraps state in `{"schemaVersion": N, "state": ...}`.
`Migrations[n]` transforms state JSON from version n to n+1. With
`AllowUnversioned: true`, existing raw JSON enters the chain at version zero:

```go
codec := nodra.VersionedJSONCodec[Settings]{
    Version: 2,
    AllowUnversioned: true,
    Migrations: map[int]nodra.JSONMigration{
        0: migrateLegacyToV1,
        1: migrateV1ToV2,
    },
}
persistence := nodra.NewPersistence(store, nodra.FileStorage{Path: path}, codec)
err := persistence.Load(ctx) // completes migrations before changing the store
```

Missing migrations, invalid migration output and future schemas return errors.
A failed load leaves the store and file untouched. Load before starting autosave;
successful migration is written by a subsequent `Save` or autosave.

## Validated persistence and backup recovery

`NewPersistenceWithOptions` accepts `PersistenceOptions[T]{Validate: ...}`. Validation runs after decoding/migration and before Store replacement, and before encoding/saving. It must be pure and concurrency-safe. A failed load or validation leaves memory and files unchanged. `LoadError` identifies `read`, `decode` or `validate`, distinguishes backup loads, and preserves the underlying error for `errors.Is`/`errors.As`.

Enable `FileStorage{Path: path, Backup: true}` to write an independently replaced `path + ".bak"` copy after successful primary replacement. Backup I/O errors are returned; the primary may already have been saved when a backup fails. The two files are not one transaction. Keep one Persistence writer per destination, and reserve its `.bak` path for this backup. Applications must surface save errors rather than assume both files were updated.

After a failed startup Load, stop and present that error before starting autosave. `persistence.LoadBackup(ctx)` explicitly validates and loads the backup into memory without rewriting either file. A later Save intentionally persists the recovered/current state. No silent fallback discards a corrupt original. Stores without a backup-capable backend return `ErrNoBackup`. Try `go run ./examples/persistent -file /path/to/state.json -recover-backup` to inspect recovery without modifying the files.

For multiple cooperating instances, explicitly enable content-conflict detection:

```go
storage, err := (nodra.FileStorage{Path: path, Backup: true}).WithConflictDetection(ctx)
// Handle err, then use the checked backend with ordinary Persistence APIs.
persistence := nodra.NewPersistence(store, storage, codec)
if err := persistence.Save(ctx); errors.Is(err, nodra.ErrFileConflict) {
    // Ask the application to reload/merge or save a separate copy.
}
```

The constructor observes current primary bytes (or absence) as the baseline without loading Store state. `CheckedFileStorage` checks a SHA-256 fingerprint under an OS advisory lock before replacing the file. Independent instances/processes using this backend coordinate through `path + ".lock"`; reserve that sidecar and never remove it while instances may be active. A `FileConflictError` carries expected/actual fingerprints without file contents. Rejected saves preserve primary and backup bytes, and retries keep failing until the application explicitly loads/reconciles or chooses another destination. `Load` acknowledges current primary bytes, including absence while returning `os.ErrNotExist`; decode/validation errors must still stop startup autosave. `LoadBackup` does not acknowledge changed primary content. A backup failure after primary replacement acknowledges that primary so retry can complete the backup without a false conflict. Async writers surface conflicts through Errors/Flush/Close and remain pending.

All writers must opt in for coordinated replacement. Plain FileStorage and external editors do not honor the lock: changes completed before the check are detected, but an uncooperative concurrent writer can race the check and replacement. This is not an atomic compare-and-swap against arbitrary filesystem writers. Use regular local files and one canonical destination; primary symlinks/nonregular files are rejected, parent directory aliases are canonicalized, and hard-link aliases/network filesystem locking are outside the contract. Equal-content replacements and changes reverted before the next check do not count as content conflicts. The default FileStorage remains unchanged.

## Write-behind persistence

Move encoding and disk I/O off the goroutine updating the store:

```go
writer, err := persistence.AutoSaveAsync(ctx, nodra.AsyncOptions{
    Debounce: 100 * time.Millisecond,
})
// Handle err before using writer.
// Updates now coalesce on one worker; Flush bypasses the debounce delay.
err = writer.Flush(flushCtx)
// At application exit, stop updates and drain before canceling ctx or closing store.
err = writer.Close(shutdownCtx)
```

The writer schedules the current state once at startup. `Flush` waits for the version visible at the call; canceling one flush wait leaves the worker running. Failed writes stay pending and retry on the next update or flush. `Errors()` is a bounded stream for background failures; `Flush` and `Close` return errors directly. `Close` stops observing, attempts the final flush, and stops even on failure. Retry a failed shutdown write with `persistence.Save` and a fresh context.

`writer.Status()` returns an observable `ReadOnly[PersistenceState]` for UI labels and MyGo invalidation. `RequestedVersion` is the newest requested source version and `SavedVersion` is this writer's durable acknowledgement; `HasSaved` distinguishes unsaved version zero. `Pending`, `Saving`, `Error` and `Closed` distinguish waiting, active I/O, failure and shutdown. Pending stays true if shutdown leaves changes unsaved, and retry clears the displayed failure while saving. Revision prevents delayed publishers from replacing newer status. Subscribers run synchronously outside writer locks; do not call Flush or Close from the writer's own status callbacks. Subscriber panics cannot terminate the worker. Writes made separately through `Persistence.Save` or another instance are outside this writer's acknowledgement.

Parent cancellation and store closure stop the worker without a guaranteed drain.

[`examples/persistent`](examples/persistent/main.go) combines cloning, legacy
migration, error handling and graceful shutdown:

```sh
go run ./examples/persistent -file /path/to/state.json
```

## Repository layout

```text
*.go                           store, history, persistence, MyGo invalidation, and tests (package nodra)
devtools/                      optional inspector, local HTTP API and embedded browser UI
examples/                      standalone and native MyGo examples
docs/architecture.md           lifecycle and threading details
```

The repository is one Go module, `github.com/yldm-tech/nodra`, with a single `go.mod` at the root.

## DevTools

```go
import "github.com/yldm-tech/nodra/devtools"

inspector := devtools.New(devtools.Options{}) // read-only by default
defer inspector.Close()
stop, err := devtools.Attach(inspector, "preferences", prefsStore)
// Handle err before using stop; call stop when this store is no longer in scope.
url, err := inspector.Listen() // random loopback port; open the full session URL
```

The browser shows multiple stores, live state, bounded mutation/action history, action durations and errors, before/after diffs, filtering and JSON export. Set `AllowRestore: true` explicitly to enable typed JSON editing and restoring a retained snapshot. Restoration calls Set and emits new events; it does not reverse external action effects. `Redact` filters state before capture and disables restoration. `RedactMetadata` separately filters names, task keys and error text before timeline/task retention, pending progress and export. Metadata callback failures discard the original text and use a fixed diagnostic. Store IDs must be public labels. No external assets, services or frontend build step are required.

Try the [interactive example](examples/devtools/main.go) and see the
[DevTools guide](docs/devtools.md) for lifecycle, redaction and browser tests:

```sh
go run ./examples/devtools -restore
```

![Nodra DevTools inspecting a state change](docs/devtools.png)

## Verification

```sh
go test -race ./...
go vet ./...
```

Run the native MyGo example with `go run ./examples/mygo`.

CI compares public APIs with the PR base using a pinned `golang.org/x/exp/cmd/apidiff` and runs `scripts/check-consumer.sh` in a temporary independent module with `GOWORK=off`. Post-release verification runs the same consumer against the version downloaded through the Go proxy, without a local replace. To reproduce API comparison, set `APIDIFF` to the installed tool and run `bash scripts/check-api.sh /path/to/base-checkout`. Intentional incompatible changes require an exact incompatible report in `.github/api-changes/<base-commit-sha>.txt` plus migration guidance in the PR; unrelated or stale approvals cannot hide additional changes. This checks source compatibility, not runtime semantics or behavior of every downstream application.

CI runs race tests and vet on Linux, macOS and Windows. It also runs golangci-lint, two 10-second fuzz targets, benchmark smoke tests, cross builds for Linux and Windows, and checks that the module path is `github.com/yldm-tech/nodra`, that there is no other `go.mod`, `go.work` or `replace` directive, and that `go mod tidy` leaves no diff.

### Fuzzing and benchmarks

```sh
go test -run '^$' -fuzz=FuzzVersionedJSONDecode -fuzztime=30s -parallel=2 .
go test -run '^$' -fuzz=FuzzHistoryStateMachine -fuzztime=30s -parallel=2 .
go test -run '^$' -bench=. -benchmem -count=10 . ./devtools
```

Fuzzing checks accepted versioned/legacy JSON round trips and compares bounded undo/redo/batch operations with an independent state-machine model. Benchmarks report store updates with 0/1/100 listeners, full cloning of a 1,000-entry map, and selection of 1,000-element slices with default versus custom equality. Run repeated measurements on the same Go version, hardware and load before comparing changes. The CI `-benchtime=1x` run only checks execution. A separate performance workflow checks out the PR base and candidate on the same runner and runs identical compatibility workloads in alternating order, 10 samples each, with one toolchain and two CPUs. It uploads raw samples, environment details and benchstat comparisons. Candidate-only benchmarks measure derived pipelines, combinations, scheduling and inspector off/every/coalesced progress (128 reports per task). Timing changes are review evidence rather than a fixed hosted-runner pass/fail threshold.

For a local comparison, export the baseline sources (v0.6.0 or newer) to an independent directory, install the pinned `golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e` tool outside this module, then run `bash scripts/benchmark.sh /path/to/baseline /path/to/results`. The script copies only the compatibility workload into that baseline directory; do not point it at a checkout with work you need to preserve. The baseline samples use the candidate's exact same workload. Keep raw results rather than comparing numbers measured on different machines or under different load.

An illustrative local run on Apple M4 Pro, darwin/arm64 measured approximately 73 ns per update without listeners, 101 ns with one listener and 1.51 µs with 100 listeners; cloning the map cost 6.81 µs. Slice selection including the projection clone took 6.57 µs with default equality and 1.13 µs with `slices.Equal`. These are machine-specific measurements, not API guarantees.

## Releases

The module is released by [release-please](https://github.com/googleapis/release-please) with plain `vX.Y.Z` tags and one root `CHANGELOG.md`.

1. Every push to `main` updates the release PR when there are releasable commits since the last tag. Merging that PR tags the release and publishes a GitHub release.
2. After a release, `post-release.yml` checks that the tag resolves through `proxy.golang.org`.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):

- `feat: ...` adds a feature (minor bump)
- `fix: ...` fixes a bug (patch bump)
- `feat!: ...` or a `BREAKING CHANGE:` footer marks a breaking change (minor bump while the version is below 1.0.0)
- `docs`, `test`, `refactor`, `ci`, `build` and `chore` do not trigger a release

Use a scope where it helps, e.g. `fix(persist): ...`.

Release PRs are pushed with the workflow's `GITHUB_TOKEN`. GitHub holds their `test` runs at "approval required", so the release workflow approves those runs itself (`.github/scripts/start-pr-ci.sh`). Setting a `RELEASE_PLEASE_TOKEN` repository secret (a fine-grained PAT or GitHub App token with contents and pull request write access) makes them trigger CI directly instead.


## Pinia/Zustand-style example

```go
store := nodra.Create(Counter{Count: 0})
increment := store.Action("increment", func(state *Counter) error {
    state.Count++
    return nil
})
double := nodra.Derive(store, func(state Counter) int {
    return state.Count * 2
}, func(a, b int) bool { return a == b })

_ = increment()
fmt.Println(store.State().Count, double.Get())
```

See [`docs/architecture.md`](docs/architecture.md) for the full concept mapping.


## Named stores

```go
counter := nodra.Define("counter", func() Counter {
    return Counter{Count: 0}
})
store := counter.New()
```

Each call to `New` creates an independent instance with the same initial-state factory. The ID is available to mutation and action observers for logging and devtools integrations.
