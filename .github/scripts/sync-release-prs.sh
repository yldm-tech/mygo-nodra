#!/usr/bin/env bash
# Runs after release-please on main. For each component's open release PR:
# - keeps it in draft while the component still requires another module of this repository at v0.0.0, because a tag cut from it would not build for consumers; marks it ready again once the requirement is bumped;
# - when DISPATCH_CI is true (release-please pushed with GITHUB_TOKEN, which triggers no workflows), dispatches the test workflow on the PR head unless it already ran there.
set -euo pipefail

repo_module=github.com/yldm-tech/mygo-nodra
base=${GITHUB_REF_NAME:-main}
cd "$(git rev-parse --show-toplevel)"
dirs=$(jq -r '.packages | keys[]' release-please-config.json)

for dir in $dirs; do
	component=$(jq -r --arg d "$dir" '.packages[$d].component // $d' release-please-config.json)
	branch="release-please--branches--$base--components--$component"
	pr=$(gh pr list --head "$branch" --base "$base" --state open --json number,isDraft,headRefOid --jq '.[0] // empty | "\(.number) \(.isDraft) \(.headRefOid)"')
	if [[ -z "$pr" ]]; then
		continue
	fi
	read -r number is_draft head_sha <<<"$pr"

	pending=$(cd "$dir" && GOWORK=off go mod edit -json | jq -r --arg prefix "$repo_module/" '.Require // [] | .[] | select((.Path | startswith($prefix)) and .Version == "v0.0.0") | .Path' | paste -sd, -)
	if [[ -n "$pending" && "$is_draft" == "false" ]]; then
		gh pr ready "$number" --undo
		gh pr comment "$number" --body "Converted to draft: $dir still requires $pending at v0.0.0, so a tag cut from this PR would not build outside the workspace. Merge the dependency bump PR opened after that module's release first; this PR is marked ready again automatically."
	elif [[ -z "$pending" && "$is_draft" == "true" ]]; then
		gh pr ready "$number"
		gh pr comment "$number" --body "Marked ready: $dir no longer requires an unreleased module of this repository."
	fi

	if [[ "${DISPATCH_CI:-false}" == "true" ]]; then
		runs=$(gh run list --workflow test.yml --commit "$head_sha" --json databaseId --jq length)
		if [[ "$runs" == "0" ]]; then
			gh workflow run test.yml --ref "$branch"
		fi
	fi
done
