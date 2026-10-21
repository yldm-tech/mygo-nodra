#!/usr/bin/env bash
# Starts the test workflow for a PR whose head was pushed with GITHUB_TOKEN.
#
# GitHub records the PR's pull_request run but holds it at action_required, because the bot counts as a first-time contributor. Approving that run starts it, and its checks report on the PR. If no such run shows up or approval is refused, the workflow is dispatched on the branch instead; those results show on the commit but not in the PR's checks.
#
# Usage: .github/scripts/start-pr-ci.sh <branch> <head sha>
set -euo pipefail

branch=$1
sha=$2

runs() {
	gh run list --workflow test.yml --commit "$sha" --event pull_request --json databaseId,status,conclusion --jq "$1"
}

for _ in $(seq 1 12); do
	if [[ "$(runs '[.[] | select(.conclusion != "action_required")] | length')" != "0" ]]; then
		echo "test workflow already running or done for $sha"
		exit 0
	fi
	held=$(runs '.[] | select(.conclusion == "action_required") | .databaseId')
	if [[ -n "$held" ]]; then
		approved=true
		for id in $held; do
			if ! gh api -X POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/approve" >/dev/null; then
				approved=false
			fi
		done
		if [[ "$approved" == "true" ]]; then
			echo "approved test workflow run(s) $held for $branch"
			exit 0
		fi
		break
	fi
	sleep 5
done

echo "::warning::could not start the pull_request test run for $branch; dispatching the test workflow on the branch instead, so its results show on commit $sha rather than in the PR's checks"
if [[ "$(gh run list --workflow test.yml --commit "$sha" --event workflow_dispatch --json databaseId --jq length)" == "0" ]]; then
	gh workflow run test.yml --ref "$branch"
fi
