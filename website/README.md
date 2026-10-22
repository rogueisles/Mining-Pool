# Optional reference pool website

These are the source components behind the reference pool's mainnet website:
static page generator, browser script, styles/assets and read-only Python server.
The public example configuration replaces the reference pool's address, hostname
and mining endpoint with placeholders. It contains no private backend credentials.

The website is optional. Existing pools can retain their own frontend and consume
their own ledger/API. The mining backend and approved signer client do not depend
on this design or on a website being hosted at pool.rogueisles.com.

## Build and preview

Edit `config.json`: choose your pool's name, HTTPS hostname, public Stratum URL,
public RGN beneficiary, fee/threshold/maturity policy and wallet RPC description.
Do not put native RPC passwords or wallet secrets in it; `/api/config` is public.
The treasury addresses are public network settings and are not your payout wallet.

```bash
python3 website/build.py
python3 website/server.py \
  --host 127.0.0.1 --port 8091 \
  --upstream http://127.0.0.1:8080 \
  --rpc http://127.0.0.1:18545 \
  --state /absolute/private/path/website-history \
  --preview
```

The static generator writes `website/dist/`. The server maintains its own
read-only cache/history; it never controls mining, Redis or payments. Preview
mode is for local checks, not a public production deployment. Configure your own
HTTPS reverse proxy, fixed private upstreams, service supervision and admin
authentication before exposing the site. No certificate account, DNS credential,
operator auth file, cache database or production web-server configuration is included.

The public server supports pool stats/miners/blocks/payments, account pages and
selected read-only RGN block/transaction lookups. It is not a generic RPC proxy
and must not be expanded to forward wallet or administrative methods.

## Live health requires an adapter

The core pool API provides accounting/statistics. The richer `/api/health` used
by the reference frontend additionally describes native health, canonical peer
checks, work freshness, service state and payment readiness. A bare core API may
not supply that endpoint; the website then reports unavailable/stale health.

`health-collector-reference.py.txt` supplies the reference collector's source
for developers to review and adapt. It keeps the reference deployment's service
names, private file schema and paths, but its wallet binding is a placeholder.
It is **not** an installer or a ready-to-run generic collector. Map those bindings
to your own setup and test its permissions/freshness rules before scheduling it.
The new `operator/gate.json` schema differs from the original deployment's
`/etc/rgn-pool-mainnet/control.json`; do not point the collector at that new file
without adapting the reader. Your health adapter must also implement native
authentication using your own private configuration, never the public website config.

The reference adapter emits a small allowlisted object including:

```text
updated, chainId, genesisHash, activationHeight
miningArmed, miningState, readyForNiceHash
freshCombinedJob, accountingAcceptsWork, poolServicesAcceptsWork
ogg: { height, syncing, peers }                 # retained schema field name
external: { digibyte: {...}, dogecoin: {...} }
peerCanonicalCheck: { healthy, commonHeight, hash, lagBlocks, state, checkedAt }
accounting: { healthy, health, workPaused, pendingRecords, uncreditedRecords }
payouts: { enabled, scheduled, state, interval, walletReady, workerRunning }
services, stratum, payoutsEnabled
```

No actual health output is included. Before activation display
`awaiting-fork-height`; after activation show accepting work only with fresh
eligible jobs, working accounting, healthy chain identity and canonical peer
agreement. Failed/old observations must not imply ready status. This reporting
does not itself arm mining or enable payouts.

The reference wallet readiness challenge verifies that the local private wallet
is available; it is not a miner payment. Exclude raw signatures, wallet data,
credentials, private endpoints and detailed process/config contents from public output.

## Source notes

The server and browser script include the reference live-health display changes.
Website endpoint defaults are adjusted for the public example (core API 8080,
local RGN HTTP 18545). Branding/assets are provided for the reference design;
replace them where appropriate and retain third-party logo/trademark attribution.
The site never needs a user's seed phrase, private key or wallet connection to
show mining/account statistics.
