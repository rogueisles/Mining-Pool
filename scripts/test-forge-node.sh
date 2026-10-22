#!/usr/bin/env bash
# Offline cross-repository proof interoperability, never a live RPC connection.
set -euo pipefail
forge_node="$(cd "${1:?node Go source directory required}" && pwd)"
forge_go="${2:?absolute Go 1.16.15 binary path required}"
forge_pool="$(cd "$(dirname "$0")/.." && pwd)"
[[ "$forge_go" = /* ]] || { echo 'Go binary path must be absolute' >&2; exit 1; }
[[ "$("$forge_go" version)" == 'go version go1.16.15 '* ]] || { echo 'Go 1.16.15 required' >&2; exit 1; }
forge_tmp="$(mktemp -d)"
trap 'rm -rf "$forge_tmp"' EXIT
export FORGE_ISSUED_FIXTURE="$forge_tmp/issued.json"
export FORGE_POOL_FIXTURE="$forge_tmp/pool-proof.json"
(
  cd "$forge_node"
  GO111MODULE=off bash build/env.sh "$forge_go" test -vet=off -count=1 ./miner -run '^TestExportForgePoolWork$'
)
(
  cd "$forge_pool"
  "$forge_go" test -vet=off -count=1 ./proxy -run '^TestExportForgeNodeFixture$'
)
(
  cd "$forge_node"
  GO111MODULE=off bash build/env.sh "$forge_go" test -vet=off -count=1 -v ./consensus/forge -run '^TestPoolProofInterop$'
)
echo 'Pool/node structural interoperability passed; full target-winning submission is not claimed.'
