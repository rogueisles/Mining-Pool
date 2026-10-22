#!/usr/bin/env bash
# Isolated Phase 6A. Never accepts a live daemon endpoint or pool configuration.
set -euo pipefail
if [ "$#" -ne 5 ]; then
  echo "usage: $0 /absolute/dogecoin/bin /absolute/redis-server /absolute/node/source /absolute/go1.16.15/bin/go /absolute/new-report-directory" >&2
  exit 2
fi
forge_doge_bin="$1"
forge_redis="$2"
forge_node="$3"
forge_go="$4"
forge_report="$5"
for forge_arg in "$@"; do
  [[ "$forge_arg" = /* ]] || { echo 'all paths must be absolute' >&2; exit 2; }
done
[[ "$("$forge_go" version)" == 'go version go1.16.15 '* ]] || { echo 'Go 1.16.15 required' >&2; exit 2; }
test -x "$forge_redis"
test -x "$forge_doge_bin/dogecoind"
test -d "$forge_node"
mkdir "$forge_report" # Refuse to overwrite a previous report.
forge_pool="$(cd "$(dirname "$0")/.." && pwd)"
cd "$forge_pool"
export FORGE_PHASE6_REDIS_SERVER="$forge_redis"
{
  "$forge_go" version
  "$forge_doge_bin/dogecoind" --version
  "$forge_redis" --version
  git rev-parse HEAD
  git status --short
} > "$forge_report/versions.txt"
forge_gate() {
  local name="$1"
  shift
  if "$@" > "$forge_report/$name.log" 2>&1; then
    printf '%s PASS\n' "$name" | tee -a "$forge_report/results.txt"
  else
    printf '%s FAIL\n' "$name" | tee -a "$forge_report/results.txt"
    tail -80 "$forge_report/$name.log" >&2
    exit 1
  fi
}
forge_gate dogecoin-redis bash scripts/test-forge-dogecoin.sh "$forge_doge_bin" "$forge_go"
# Do not allow an accidentally skipped acceptance test to appear as a pass.
grep -q -- '--- PASS: TestOfficialDogecoinPhase6DurableAccounting' "$forge_report/dogecoin-redis.log"
forge_gate node-interop bash scripts/test-forge-node.sh "$forge_node" "$forge_go"
forge_gate compile-all "$forge_go" test -vet=off -run '^$' ./...
forge_gate pool-build "$forge_go" build -o "$forge_report/open-progpow-pool" .
forge_gate maintenance-build "$forge_go" build -o "$forge_report/forge-maintenance" ./cmd/forge-maintenance
forge_gate status-build "$forge_go" build -o "$forge_report/forge-status" ./cmd/forge-status
printf '%s\n' 'Phase 6A passed. Real DOGE and Redis restart verified; LTC acceptance and target-winning OGG remain OPEN.' | tee -a "$forge_report/results.txt"
