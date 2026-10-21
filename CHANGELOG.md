# Changelog

## [packages/nodra 0.1.0 and packages/mygo 0.1.0](https://github.com/yldm-tech/nodra/releases/tag/packages%2Fnodra%2Fv0.1.0) (2026-10-21)

Superseded. These were released as two separate modules, `github.com/yldm-tech/mygo-nodra/packages/nodra` (tag `packages/nodra/v0.1.0`) and `github.com/yldm-tech/mygo-nodra/packages/mygo` (tag `packages/mygo/v0.1.0`), from the repository before it was renamed to `yldm-tech/nodra`. The code now lives in the single module `github.com/yldm-tech/nodra`: import the core as `github.com/yldm-tech/nodra` and the MyGo adapter as `github.com/yldm-tech/nodra/mygo`. The old tags stay in place because the Go module proxy has cached them, but they receive no further releases.

### Highlights

- Added store lifecycle and `ErrClosed`.
- Added immediate and equality-aware subscriptions.
- Added rollback-safe `UpdateErr`.
- Added nested batches, bounded undo/redo history and pluggable persistence.
- Added observable autosave failures and store-closing watch channels.
- Added JSONCodec, atomic FileStorage, and a runnable native MyGo example.
- Added Pinia/Zustand-style Create, Define, State, Patch, Reset, Derive, selector previous values, named actions, mutation metadata, and action lifecycle hooks.
- Added the MyGo adapter, `SubscribeInvalidation`, which requests a new native frame when a store publishes.
