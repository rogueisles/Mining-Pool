#!/usr/bin/env bash
# Disposable full-pool process test. No live config or RPC endpoint is accepted.
set -euo pipefail
if [ "$#" -ne 3 ]; then
  echo "usage: $0 /absolute/go1.16.15/bin/go /absolute/redis-server /absolute/new-report-directory" >&2
  exit 2
fi
forge_go="$1"
forge_redis="$2"
forge_report="$3"
for forge_arg in "$@"; do
  [[ "$forge_arg" = /* ]] || { echo 'absolute paths required' >&2; exit 2; }
done
[[ "$("$forge_go" version)" == 'go version go1.16.15 '* ]] || { echo 'Go 1.16.15 required' >&2; exit 2; }
test -x "$forge_redis"
mkdir "$forge_report"
cd "$(dirname "$0")/.."
{
  "$forge_go" version
  "$forge_redis" --version
  git rev-parse HEAD
  git status --short
} > "$forge_report/versions.txt"
# Instrument the actual pool, while the CPU miner in the test driver runs at
# normal speed. A separate package race gate covers driver/shared Go code.
GOFLAGS= "$forge_go" build -race -o "$forge_report/pool-race" .
FORGE_PHASE6_POOL_BINARY="$forge_report/pool-race" \
FORGE_PHASE6_REDIS_SERVER="$forge_redis" \
GOFLAGS= "$forge_go" test -vet=off -count=1 -timeout 8m \
  -run '^TestForgePoolProcessStratumCrashRecovery$' -v ./proxy \
  2>&1 | tee "$forge_report/process.log"
grep -q -- '--- PASS: TestForgePoolProcessStratumCrashRecovery' "$forge_report/process.log"
sha256sum "$forge_report/pool-race" > "$forge_report/BUILT-BINARY-SHA256.txt"
