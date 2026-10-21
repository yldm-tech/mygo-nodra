#!/usr/bin/env bash
# Checks one module of the monorepo the way a consumer sees it: outside the go.work workspace, resolving every requirement through the module proxy.
#
# Usage: .github/scripts/check-module.sh <module dir relative to the repository root>
set -euo pipefail

repo_module=github.com/yldm-tech/mygo-nodra
dir=${1#./}
cd "$(git rev-parse --show-toplevel)/$dir"
export GOWORK=off

want="$repo_module/$dir"
got=$(go mod edit -json | jq -r .Module.Path)
if [[ "$got" != "$want" ]]; then
	echo "::error file=$dir/go.mod::module path is $got, but a module in $dir/ must be $want or go get cannot resolve it"
	exit 1
fi
echo "module path: $got"

replaces=$(go mod edit -json | jq -r '.Replace // [] | .[] | .Old.Path')
if [[ -n "$replaces" ]]; then
	echo "::error file=$dir/go.mod::go.mod has replace directives, which consumers ignore; keep local wiring in go.work instead: $replaces"
	exit 1
fi

# A requirement on another module of this repository at v0.0.0 only resolves inside the workspace. That is expected until the required module has its first release, after which the post-release workflow bumps the requirement and this check is enforced.
pending=$(go mod edit -json | jq -r --arg prefix "$repo_module/" '.Require // [] | .[] | select((.Path | startswith($prefix)) and .Version == "v0.0.0") | .Path')
if [[ -n "$pending" ]]; then
	for path in $pending; do
		echo "::notice title=$dir standalone check skipped::$dir requires $path v0.0.0, which exists only inside the workspace. The standalone check is enforced once $path is released and the requirement is bumped to that tag."
	done
	exit 0
fi

go mod tidy -diff
go build ./...
go test ./...
