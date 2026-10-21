# mygo-nodra

**Nodra** is a production-oriented reactive state store for Go desktop applications. It gives MyGo apps Zustand-like ergonomics without coupling the core to MyGo, a renderer, or a persistence backend.

## Install

```sh
go get github.com/yldm-tech/mygo-nodra/packages/nodra
```

For native MyGo window invalidation, also add the optional adapter:

```sh
go get github.com/yldm-tech/mygo-nodra/packages/mygo
```

```go
import nodra "github.com/yldm-tech/mygo-nodra/packages/nodra"

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
- `mygo` adapter package for `Window.Invalidate`

## Design guarantees

- Store locks are released before user listeners run, so listeners can safely read or update the same store.
- Versions increment once per published update, not once per mutation inside a batch.
- Closing a store is permanent; updates return `nodra.ErrClosed` and active watches terminate.
- Values are not deep copied. Use immutable value conventions or copy slices and maps at the update boundary.
- Watch channels are best effort and bounded; slow consumers may skip intermediate snapshots.

## Repository layout

```text
packages/nodra/                 core store, history, persistence, and tests
packages/mygo/                  MyGo native window adapter and tests
examples/                       standalone and native MyGo examples
docs/architecture.md           lifecycle and threading details
go.work                        workspace joining the three Go modules
```

## Verification

```sh
go test -race ./packages/nodra/... ./packages/mygo/... ./examples/...
go vet ./packages/nodra/... ./packages/mygo/... ./examples/...
```

Run the native MyGo example with `go run ./examples/mygo`.

CI also lints every module with golangci-lint and checks each one outside the workspace (`GOWORK=off`, see `.github/scripts/check-module.sh`): the `module` path must match its directory, `go mod tidy` must leave no diff, and `go build` / `go test` must pass against published versions only. A module that still requires another module of this repository at `v0.0.0` skips that check with a notice until the required module is released and the requirement is bumped.

## Releases

`packages/nodra` and `packages/mygo` are released independently by [release-please](https://github.com/googleapis/release-please) as Go submodule tags, `packages/nodra/vX.Y.Z` and `packages/mygo/vX.Y.Z`, each with its own `CHANGELOG.md`. `examples` is not released.

1. Every push to `main` updates one release PR per module that has releasable commits since its last tag. Merging that PR tags the release and publishes a GitHub release.
2. After a release, `post-release.yml` checks that the tag resolves through `proxy.golang.org`, then opens a `fix(deps)` PR that moves the modules requiring it onto the new tag.
3. A release PR stays in draft while its module still requires another module of this repository at `v0.0.0`, so `packages/mygo` is released only after it requires a published core.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/), and only commits that touch a module's directory count toward its release:

- `feat: ...` adds a feature (minor bump)
- `fix: ...` fixes a bug (patch bump)
- `feat!: ...` or a `BREAKING CHANGE:` footer marks a breaking change (minor bump while the version is below 1.0.0)
- `docs`, `test`, `refactor`, `ci`, `build` and `chore` do not trigger a release

Use a scope for the module where it helps, e.g. `fix(mygo): ...`.

Release PRs and dependency bump PRs are pushed with the workflow's `GITHUB_TOKEN`, which does not trigger workflows, so the release workflow dispatches the `test` workflow on them. Setting a `RELEASE_PLEASE_TOKEN` repository secret (a fine-grained PAT or GitHub App token with contents and pull request write access) makes them trigger CI directly instead.


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

Each call to `New` creates an independent instance with the same initial-state
factory. The ID is available to mutation and action observers for logging and
devtools integrations.
