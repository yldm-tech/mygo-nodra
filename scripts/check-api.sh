#!/usr/bin/env bash
set -euo pipefail

# Compare public module APIs against an independent checkout. Breaking changes require an exact, reviewed report tied to the base commit; a general bypass is intentionally unsupported.
if [[ $# != 1 ]]; then
  echo 'Usage: scripts/check-api.sh <baseline-directory>' >&2
  exit 2
fi
api_candidate=$(cd "$(dirname "$0")/.." && pwd)
api_base=$(cd "$1" && pwd)
api_tool=${APIDIFF:-apidiff}
api_base_sha=$(git -C "$api_base" rev-parse HEAD)
api_expected="$api_candidate/.github/api-changes/$api_base_sha.txt"
api_tmp=$(mktemp -d)
trap 'rm -f "$api_tmp/base.export" "$api_tmp/candidate.export" "$api_tmp/incompatible.txt" "$api_tmp/expected.txt"; rmdir "$api_tmp"' EXIT
export GOWORK=off
api_module=github.com/yldm-tech/nodra
(cd "$api_base" && "$api_tool" -m -w "$api_tmp/base.export" "$api_module")
(cd "$api_candidate" && "$api_tool" -m -w "$api_tmp/candidate.export" "$api_module")
"$api_tool" -m "$api_tmp/base.export" "$api_tmp/candidate.export"
"$api_tool" -m -incompatible "$api_tmp/base.export" "$api_tmp/candidate.export" | LC_ALL=C sort > "$api_tmp/incompatible.txt"
if [[ -f "$api_expected" ]]; then
  LC_ALL=C sort "$api_expected" > "$api_tmp/expected.txt"
else
  : > "$api_tmp/expected.txt"
fi
if ! diff -u "$api_tmp/expected.txt" "$api_tmp/incompatible.txt"; then
  echo "Unexpected API breakage. Intentional changes require the exact incompatible report in .github/api-changes/$api_base_sha.txt and migration guidance in the PR." >&2
  exit 1
fi
