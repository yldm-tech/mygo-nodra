# Changelog

## [Unreleased]

- **Breaking:** the module path now matches its directory: import the MyGo integration as `github.com/yldm-tech/mygo-nodra/packages/mygo` (was `github.com/yldm-tech/mygo-nodra/mygo`). Since the monorepo move, `go get` could not resolve the old path.
