# Changelog

## 0.1.0 (2026-10-21)


### Bug Fixes

* **deps:** require packages/nodra v0.1.0 ([#6](https://github.com/yldm-tech/mygo-nodra/issues/6)) ([8f2f472](https://github.com/yldm-tech/mygo-nodra/commit/8f2f472b1f8b9482d023d3e4369be78e5d5ccd44))
* make module paths match their directories ([9932fa3](https://github.com/yldm-tech/mygo-nodra/commit/9932fa348273abc93578ce133de4f83d0c2064e5))
* make module paths match their directories ([253737b](https://github.com/yldm-tech/mygo-nodra/commit/253737bcb60a491332e8c33520185a4fdccf6f15))

## [Unreleased]

- **Breaking:** the module path now matches its directory: import the MyGo integration as `github.com/yldm-tech/mygo-nodra/packages/mygo` (was `github.com/yldm-tech/mygo-nodra/mygo`). Since the monorepo move, `go get` could not resolve the old path.
