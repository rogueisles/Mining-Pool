#!/usr/bin/env bash
# Fresh loopback-only Litecoin regtest; no existing datadir/config accepted.
set -euo pipefail
if [ "$#" -ne 3 ]; then
  echo "usage: $0 /absolute/litecoin/bin /absolute/go1.16.15/bin/go /absolute/redis-server" >&2
  exit 2
fi
forge_ltc_bin="$1"
forge_go="$2"
export FORGE_PHASE6_REDIS_SERVER="$3"
[[ "$forge_ltc_bin" = /* && "$forge_go" = /* && "$FORGE_PHASE6_REDIS_SERVER" = /* ]] || exit 2
[[ "$("$forge_go" version)" == 'go version go1.16.15 '* ]] || exit 2
forge_ltc_data=$(mktemp -d)
forge_ltc_peer=$(mktemp -d)
forge_ltc_pid=''
forge_peer_pid=''
cleanup() {
  for forge_pid in "$forge_ltc_pid" "$forge_peer_pid"; do
    if [ -n "$forge_pid" ]; then kill "$forge_pid" 2>/dev/null || true; wait "$forge_pid" 2>/dev/null || true; fi
  done
  rm -rf "$forge_ltc_data" "$forge_ltc_peer"
}
trap cleanup EXIT
cat > "$forge_ltc_data/litecoin.conf" <<'EOF'
regtest=1
server=1
listen=1
dnsseed=0
discover=0
upnp=0
rpcuser=forge-regtest
rpcpassword=disposable-test-only
[regtest]
bind=127.0.0.1
port=19454
connect=127.0.0.1:19456
rpcbind=127.0.0.1
rpcallowip=127.0.0.1
rpcport=19455
EOF
sed -e 's/port=19454/port=19456/' -e 's/rpcport=19455/rpcport=19457/' -e 's/connect=127.0.0.1:19456/connect=0/' "$forge_ltc_data/litecoin.conf" > "$forge_ltc_peer/litecoin.conf"
"$forge_ltc_bin/litecoind" -datadir="$forge_ltc_peer" -printtoconsole > "$forge_ltc_peer/console.log" 2>&1 &
forge_peer_pid=$!
timeout 30 "$forge_ltc_bin/litecoin-cli" -datadir="$forge_ltc_peer" -rpcwait getblockchaininfo >/dev/null
"$forge_ltc_bin/litecoind" -datadir="$forge_ltc_data" -printtoconsole > "$forge_ltc_data/console.log" 2>&1 &
forge_ltc_pid=$!
forge_cli() { "$forge_ltc_bin/litecoin-cli" -datadir="$forge_ltc_data" "$@"; }
timeout 30 "$forge_ltc_bin/litecoin-cli" -datadir="$forge_ltc_data" -rpcwait getblockchaininfo >/dev/null
test "$(forge_cli getblockcount)" = 0
for forge_attempt in $(seq 1 100); do
  if [ "$(forge_cli getconnectioncount)" -gt 0 ]; then break; fi
  sleep 0.1
done
test "$(forge_cli getconnectioncount)" -gt 0
forge_cli createwallet phase6 >/dev/null
forge_ltc_address=$(forge_cli getnewaddress '' bech32)
forge_cli generatetoaddress 110 "$forge_ltc_address" >/dev/null
export FORGE_LTC_TEST_RPC=http://127.0.0.1:19455
export FORGE_LTC_SIGNER_RPC=http://127.0.0.1:19457
FORGE_LTC_TEST_STAGE=empty "$forge_go" test -vet=off -count=1 -v ./forgepool -run '^TestOfficialLitecoinAcceptance$'
forge_cli sendtoaddress "$forge_ltc_address" 1 >/dev/null
FORGE_LTC_TEST_STAGE=witness "$forge_go" test -vet=off -count=1 -v ./forgepool -run '^TestOfficialLitecoinAcceptance$'
forge_ltc_height=$(forge_cli getblockcount)
forge_cli generatetoaddress "$((431-forge_ltc_height))" "$forge_ltc_address" >/dev/null
forge_mweb_address=$(forge_cli getnewaddress '' mweb)
forge_cli sendtoaddress "$forge_mweb_address" 1 >/dev/null
FORGE_LTC_TEST_STAGE=mweb-pegin "$forge_go" test -vet=off -count=1 -v ./forgepool -run '^TestOfficialLitecoinAcceptance$'
FORGE_LTC_TEST_STAGE=mweb-empty "$forge_go" test -vet=off -count=1 -v ./forgepool -run '^TestOfficialLitecoinAcceptance$'
"$forge_go" test -vet=off -count=1 -v ./proxy -run '^TestOfficialLitecoin'
