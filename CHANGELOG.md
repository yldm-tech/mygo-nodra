# Changelog

## [0.7.0](https://github.com/yldm-tech/nodra/compare/v0.6.0...v0.7.0) (2026-10-24)


### ⚠ BREAKING CHANGES

* compose derived state, debounce actions and bound diagnostic costs ([#25](https://github.com/yldm-tech/nodra/issues/25))

### Features

* compose derived state, debounce actions and bound diagnostic costs ([#25](https://github.com/yldm-tech/nodra/issues/25)) ([797a848](https://github.com/yldm-tech/nodra/commit/797a848b375772cb9aea85fccf6f084aad1797b2))

## [0.6.0](https://github.com/yldm-tech/nodra/compare/v0.5.0...v0.6.0) (2026-10-24)


### Features

* complete async state, history and DevTools workflows ([#21](https://github.com/yldm-tech/nodra/issues/21)) ([e2d780d](https://github.com/yldm-tech/nodra/commit/e2d780dbfe88029c23b18782436f01b46c8614ec))

## [0.5.0](https://github.com/yldm-tech/nodra/compare/v0.4.0...v0.5.0) (2026-10-24)


### Features

* add selector-based MyGo invalidation with automatic cleanup ([#19](https://github.com/yldm-tech/nodra/issues/19)) ([dc7cb70](https://github.com/yldm-tech/nodra/commit/dc7cb70ffbf8806014137eb931fddab3719c49f1))

## [0.4.0](https://github.com/yldm-tech/nodra/compare/v0.3.0...v0.4.0) (2026-10-24)


### Features

* support collection selectors and bounded large-state DevTools ([#16](https://github.com/yldm-tech/nodra/issues/16)) ([8938323](https://github.com/yldm-tech/nodra/commit/893832330aa4975bd7b6aa3f915bd1675ea5e086))

## [0.3.0](https://github.com/yldm-tech/nodra/compare/v0.2.0...v0.3.0) (2026-10-24)


### Features

* add state isolation, migrations, async persistence, and DevTools ([#12](https://github.com/yldm-tech/nodra/issues/12)) ([3b652d2](https://github.com/yldm-tech/nodra/commit/3b652d28de2ee2c8a4778a29c132852149f9c289))


### Bug Fixes

* preserve reactive versions and history commit consistency ([#14](https://github.com/yldm-tech/nodra/issues/14)) ([eed35be](https://github.com/yldm-tech/nodra/commit/eed35be1ac6147edf34321013dde18a501e9da6c))

## [0.2.0](https://github.com/yldm-tech/nodra/compare/v0.1.0...v0.2.0) (2026-10-21)


### ⚠ BREAKING CHANGES

* the import path github.com/yldm-tech/nodra/mygo is removed. Replace mygo.SubscribeInvalidation(store, window) from that package with nodra.Invalidate(store, window) from github.com/yldm-tech/nodra.

### Features

* move MyGo invalidation into the root package as nodra.Invalidate ([#10](https://github.com/yldm-tech/nodra/issues/10)) ([3ed08e7](https://github.com/yldm-tech/nodra/commit/3ed08e7405971f75aa126695d819554a63cf2ebd))

## 0.1.0 (2026-10-21)


### ⚠ BREAKING CHANGES

* publish as one module at the repository root, github.com/yldm-tech/nodra ([#8](https://github.com/yldm-tech/nodra/issues/8))

### Features

* publish as one module at the repository root, github.com/yldm-tech/nodra ([#8](https://github.com/yldm-tech/nodra/issues/8)) ([e9ace62](https://github.com/yldm-tech/nodra/commit/e9ace62137e470e36a92d2e94580ef72058a883f))

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
