#!/usr/bin/env bash
set -euo pipefail

# Compare identical workloads in an isolated baseline source directory and this checkout, on one machine and Go toolchain. The baseline receives only the compatibility benchmark fixture.
if [[ $# != 2 ]]; then
  echo 'Usage: scripts/benchmark.sh <isolated-baseline-directory> <results-directory>' >&2
  exit 2
fi
benchmark_candidate=$(git rev-parse --show-toplevel)
benchmark_base=$(cd "$1" && pwd)
mkdir -p "$2"
benchmark_results=$(cd "$2" && pwd)
benchmark_tool=${BENCHSTAT:-benchstat}
if [[ "$benchmark_base" == "$benchmark_candidate" ]]; then
  echo 'The baseline must be an independent source directory.' >&2
  exit 2
fi
if ! command -v "$benchmark_tool" >/dev/null; then
  echo 'Install golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e outside the module, or set BENCHSTAT.' >&2
  exit 2
fi
mkdir -p "$benchmark_base/internal/perfbench"
cp "$benchmark_candidate/internal/perfbench/benchmark_test.go" "$benchmark_base/internal/perfbench/benchmark_test.go"
export GOWORK=off
export GOMAXPROCS=2
{
  go version
  go env GOOS GOARCH
  git -C "$benchmark_candidate" rev-parse HEAD
  if git -C "$benchmark_base" rev-parse HEAD >/dev/null 2>&1; then
    git -C "$benchmark_base" rev-parse HEAD
  else
    echo "Baseline source directory: $benchmark_base"
  fi
  echo 'Interleaved baseline/candidate samples; count=10; benchtime=100ms; cpu=2.'
} > "$benchmark_results/environment.txt"
: > "$benchmark_results/base.txt"
: > "$benchmark_results/candidate.txt"
(cd "$benchmark_base" && go test -run '^$' ./internal/perfbench >/dev/null)
(cd "$benchmark_candidate" && go test -run '^$' ./internal/perfbench >/dev/null)
for sample in {1..10}; do
  echo "Collecting benchmark sample $sample/10" >&2
  (cd "$benchmark_base" && go test -run '^$' -bench=. -benchmem -benchtime=100ms -count=1 -cpu=2 ./internal/perfbench) >> "$benchmark_results/base.txt"
  (cd "$benchmark_candidate" && go test -run '^$' -bench=. -benchmem -benchtime=100ms -count=1 -cpu=2 ./internal/perfbench) >> "$benchmark_results/candidate.txt"
done
(cd "$benchmark_candidate" && go test -run '^$' -bench='Benchmark(ActionScheduling|DerivedProjectionPipeline|CombinedValues|InspectorTaskProgress)$' -benchmem -benchtime=100ms -count=10 -cpu=2 . ./devtools) > "$benchmark_results/features.txt"
"$benchmark_tool" "$benchmark_results/base.txt" "$benchmark_results/candidate.txt" > "$benchmark_results/comparison.txt"
"$benchmark_tool" "$benchmark_results/features.txt" > "$benchmark_results/features-summary.txt"
cat "$benchmark_results/comparison.txt"
cat "$benchmark_results/features-summary.txt"
