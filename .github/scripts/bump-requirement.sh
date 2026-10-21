#!/usr/bin/env bash
# Bumps every workspace module that requires <module> to <version>, resolving the new version through the module proxy (GOWORK=off) so go.mod and go.sum match what consumers see.
#
# Usage: .github/scripts/bump-requirement.sh <module dir, e.g. packages/nodra> <version, e.g. v0.1.0>
# Prints the directories it changed.
set -euo pipefail

repo_module=github.com/yldm-tech/mygo-nodra
target_dir=${1#./}
version=$2
target="$repo_module/$target_dir"
root=$(git rev-parse --show-toplevel)
cd "$root"
# Read the module list before leaving workspace mode; assignments propagate failures under set -e, loop headers do not.
dirs=$(go work edit -json | jq -r '.Use[].DiskPath | ltrimstr("./")')
export GOWORK=off

for dir in $dirs; do
	[[ "$dir" == "$target_dir" ]] && continue
	cd "$root/$dir"
	if ! go mod edit -json | jq -e --arg p "$target" '.Require // [] | any(.Path == $p)' >/dev/null; then
		cd "$root"
		continue
	fi

	# Other in-repo requirements still at v0.0.0 are not fetchable yet; point them at their directories while resolving, then drop the replacements again so go.mod stays publishable.
	temp_replaced=()
	pending=$(go mod edit -json | jq -r --arg prefix "$repo_module/" --arg target "$target" '.Require // [] | .[] | select((.Path | startswith($prefix)) and .Path != $target and .Version == "v0.0.0") | .Path')
	for path in $pending; do
		go mod edit -replace="$path=$root/${path#"$repo_module/"}"
		temp_replaced+=("$path")
	done

	# go get alone cannot move off v0.0.0: it first loads the current graph, and that version does not exist outside the workspace.
	go mod edit -require="$target@$version"
	go get "$target@$version"
	go mod tidy
	for path in ${temp_replaced[@]+"${temp_replaced[@]}"}; do
		go mod edit -dropreplace="$path"
	done

	echo "$dir"
	cd "$root"
done
