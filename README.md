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
- `SubscribeMutations` with direct, patch, reset and action metadata
- `Create`, `Define`, `State`, `Patch`, and `Reset` aliases for familiar Pinia/Zustand workflows
- Store IDs and independent named store instances
- Action lifecycle hooks: before, after, and error
- Nested `Batch` notification groups that publish one final snapshot
- `Transaction` for one serialized, fallible mutation
- `Track` history with bounded undo and redo
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

The writer schedules the current state once at startup. `Flush` waits for the
version visible at the call; canceling one flush wait leaves the worker running.
Failed writes stay pending and retry on the next update or flush. `Errors()` is
a bounded stream for background failures; `Flush` and `Close` return errors
directly. `Close` stops observing, attempts the final flush, and stops even on
failure. Retry a failed shutdown write with `persistence.Save` and a fresh context.
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

The browser shows multiple stores, live state, bounded mutation/action history,
action durations and errors, before/after diffs, filtering and JSON export. Set
`AllowRestore: true` explicitly to enable typed JSON editing and restoring a
retained snapshot. Restoration calls Set and emits new events; it does not reverse
external action effects. `Redact` filters state before capture and disables
restoration. No external assets, services or frontend build step are required.

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

CI runs race tests and vet on Linux, macOS and Windows. It also runs golangci-lint, two 10-second fuzz targets, benchmark smoke tests, cross builds for Linux and Windows, and checks that the module path is `github.com/yldm-tech/nodra`, that there is no other `go.mod`, `go.work` or `replace` directive, and that `go mod tidy` leaves no diff.

### Fuzzing and benchmarks

```sh
go test -run '^$' -fuzz=FuzzVersionedJSONDecode -fuzztime=30s -parallel=2 .
go test -run '^$' -fuzz=FuzzHistoryStateMachine -fuzztime=30s -parallel=2 .
go test -run '^$' -bench=. -benchmem -count=5 .
```

Fuzzing checks accepted versioned/legacy JSON round trips and compares bounded undo/redo/batch operations with an independent state-machine model. Benchmarks report store updates with 0/1/100 listeners, full cloning of a 1,000-entry map, and selection of 1,000-element slices with default versus custom equality. Run repeated measurements on the same Go version, hardware and load before comparing changes. The CI `-benchtime=1x` run only checks execution, with no performance thresholds.

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
