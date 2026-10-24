# nodra

**Nodra** is a production-oriented reactive state store for Go desktop applications. It is a state library for [MyGo](https://github.com/egoist/mygo) apps with Zustand-like ergonomics, and does not tie your state to a renderer or a persistence backend.

## Install

```sh
go get github.com/yldm-tech/nodra@latest
```

Everything is in the root package, `github.com/yldm-tech/nodra`.

## MyGo usage

Redraw a native MyGo window whenever its store publishes:

```go
stop := nodra.Invalidate(store, window)
```

Call `stop` when the window closes; `go run ./examples/mygo` shows the whole app.

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
- `AutoSaveWithErrors` for observable persistence failures
- Context-bound, bounded `Watch` streams that close with the store
- `Invalidate` to redraw a MyGo window when a store publishes

## Design guarantees

- Store locks are released before user listeners run, so listeners can safely read or update the same store.
- Versions increment once per published update, not once per mutation inside a batch.
- Closing a store is permanent; updates return `nodra.ErrClosed` and active watches terminate.
- Values are not deep copied, including snapshots, initial values and rollback values. Copy slices, maps and pointed-to data before changing them; mutating shared data in place also changes older snapshots and cannot be undone by rollback or `Reset`.
- Selectors and derived getters catch up with changes during initialization and ignore computations from older versions once a newer version has been processed. Callbacks still run on writer goroutines and may run concurrently.
- Concurrent `Save` calls on one `Persistence` instance are serialized from state capture through storage write, so a delayed save cannot overwrite a newer save. Keep one persistence instance per destination; `Load` is intended for startup before concurrent writes.
- Watch channels are best effort and bounded; slow consumers may skip intermediate snapshots.

## Repository layout

```text
*.go                           store, history, persistence, MyGo invalidation, and tests (package nodra)
examples/                      standalone and native MyGo examples
docs/architecture.md           lifecycle and threading details
```

The repository is one Go module, `github.com/yldm-tech/nodra`, with a single `go.mod` at the root.

## Verification

```sh
go test -race ./...
go vet ./...
```

Run the native MyGo example with `go run ./examples/mygo`.

CI also runs golangci-lint, cross builds for Linux and Windows, and checks that the module path is `github.com/yldm-tech/nodra`, that there is no other `go.mod`, `go.work` or `replace` directive, and that `go mod tidy` leaves no diff.

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
