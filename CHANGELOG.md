# Changelog

## Unreleased

- **Breaking:** module paths now match their directories: import the core as `github.com/yldm-tech/mygo-nodra/packages/nodra` (was `github.com/yldm-tech/mygo-nodra`) and the MyGo integration as `github.com/yldm-tech/mygo-nodra/packages/mygo` (was `github.com/yldm-tech/mygo-nodra/mygo`). Since the monorepo move, `go get` could not resolve either old path.
- Added store lifecycle and `ErrClosed`.
- Added immediate and equality-aware subscriptions.
- Added rollback-safe `UpdateErr`.
- Added nested batches, bounded undo/redo history and pluggable persistence.
- Added observable autosave failures and store-closing watch channels.
- Added JSONCodec, atomic FileStorage, and a runnable native MyGo example.
- Added Pinia/Zustand-style Create, Define, State, Patch, Reset, Derive, selector previous values, named actions, mutation metadata, and action lifecycle hooks.
