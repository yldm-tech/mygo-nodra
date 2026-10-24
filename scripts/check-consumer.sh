#!/usr/bin/env bash
set -euo pipefail

# Build and run from a separate module with workspace wiring disabled. With a version, resolve only the published module; otherwise use this checkout through the consumer's own replace directive.
if [[ $# -gt 1 ]]; then
  echo 'Usage: scripts/check-consumer.sh [released-version]' >&2
  exit 2
fi
consumer_source=$(cd "$(dirname "$0")/.." && pwd)
consumer_tmp=$(mktemp -d)
trap 'rm -f "$consumer_tmp/go.mod" "$consumer_tmp/go.sum" "$consumer_tmp/consumer_test.go"; rmdir "$consumer_tmp"' EXIT
cp "$consumer_source/testdata/consumer/consumer_test.go" "$consumer_tmp/consumer_test.go"
cd "$consumer_tmp"
export GOWORK=off
go mod init example.com/nodra-consumer
if [[ $# == 1 ]]; then
  go mod edit -require="github.com/yldm-tech/nodra@$1"
else
  go mod edit -require=github.com/yldm-tech/nodra@v0.0.0 -replace="github.com/yldm-tech/nodra=$consumer_source"
fi
go mod tidy
go test -mod=readonly -timeout=60s .
