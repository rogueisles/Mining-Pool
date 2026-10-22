#!/usr/bin/env bash
# Isolated official-daemon acceptance gate. Loopback-only P2P, fresh datadirs.
set -euo pipefail
if [ "$#" -ne 2 ]; then
  echo "usage: $0 /absolute/dogecoin/bin /absolute/go1.16.15/bin/go" >&2
  exit 2
fi
forge_doge_bin="$1"
forge_go="$2"
forge_doge_data=$(mktemp -d)
forge_doge_peer=$(mktemp -d)
forge_doge_pid=''
forge_peer_pid=''
cleanup() {
  if [ -n "$forge_doge_pid" ]; then
    kill "$forge_doge_pid" 2>/dev/null || true
    wait "$forge_doge_pid" 2>/dev/null || true
  fi
  if [ -n "$forge_peer_pid" ]; then
    kill "$forge_peer_pid" 2>/dev/null || true
    wait "$forge_peer_pid" 2>/dev/null || true
  fi
  rm -rf "$forge_doge_data"
  rm -rf "$forge_doge_peer"
}
trap cleanup EXIT
cat > "$forge_doge_data/dogecoin.conf" <<'EOF'
regtest=1
server=1
listen=1
bind=127.0.0.1
port=19444
connect=127.0.0.1:19446
dnsseed=0
discover=0
upnp=0
rpcbind=127.0.0.1
rpcallowip=127.0.0.1
rpcport=19445
rpcuser=forge-regtest
rpcpassword=disposable-test-only
EOF
sed -e 's/port=19444/port=19446/' -e 's/rpcport=19445/rpcport=19447/' -e 's/connect=127.0.0.1:19446/connect=0/' "$forge_doge_data/dogecoin.conf" > "$forge_doge_peer/dogecoin.conf"
"$forge_doge_bin/dogecoind" -datadir="$forge_doge_peer" -printtoconsole > "$forge_doge_peer/console.log" 2>&1 &
forge_peer_pid=$!
timeout 30 "$forge_doge_bin/dogecoin-cli" -datadir="$forge_doge_peer" -rpcwait getblockchaininfo > /dev/null
"$forge_doge_bin/dogecoind" -datadir="$forge_doge_data" -printtoconsole > "$forge_doge_data/console.log" 2>&1 &
forge_doge_pid=$!
timeout 30 "$forge_doge_bin/dogecoin-cli" -datadir="$forge_doge_data" -rpcwait getblockchaininfo > /dev/null
test "$("$forge_doge_bin/dogecoin-cli" -datadir="$forge_doge_data" getblockcount)" = 0
forge_connected=0
for attempt in $(seq 1 100); do
  if [ "$("$forge_doge_bin/dogecoin-cli" -datadir="$forge_doge_data" getconnectioncount)" -gt 0 ]; then
    forge_connected=1
    break
  fi
  sleep 0.1
done
test "$forge_connected" = 1
"$forge_doge_bin/dogecoin-cli" -datadir="$forge_doge_data" generate 40 > /dev/null
export FORGE_DOGE_TEST_RPC=http://127.0.0.1:19445
export FORGE_DOGE_SIGNER_RPC=http://127.0.0.1:19447
"$forge_go" test -vet=off -count=1 -v ./forgepool -run '^TestOfficialDogecoin'
"$forge_go" test -vet=off -count=1 -v ./proxy -run '^TestOfficialDogecoin'
"$forge_go" test -vet=off -count=1 -v ./forgesigner -run '^TestOfficialDogecoin'
