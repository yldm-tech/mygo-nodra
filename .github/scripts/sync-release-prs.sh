#!/usr/bin/env bash
# Runs after release-please on main. When START_PR_CI is true (release-please pushed with GITHUB_TOKEN), starts the test workflow on the head of the open release PR through start-pr-ci.sh.
set -euo pipefail

if [[ "${START_PR_CI:-false}" != "true" ]]; then
	exit 0
fi

base=${GITHUB_REF_NAME:-main}
gh pr list --base "$base" --state open --label "autorelease: pending" --json headRefName,headRefOid --jq '.[] | "\(.headRefName) \(.headRefOid)"' |
	while read -r branch head_sha; do
		.github/scripts/start-pr-ci.sh "$branch" "$head_sha"
	done
