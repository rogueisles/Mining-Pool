# Source provenance and verification scope

## Qualified mining core

The root Go source, module files, tests and protocol/accounting fixtures are
copied from the frozen FORGE V4 source qualified on 2026-10-21. The source
inventory is `evidence/qualified-core-source-sha256.json`.

Only two original files are replaced for publication: the root README and
`.gitignore`. No original compiled Go source, module file, codec or fixture is
changed. The original README is retained as an explicitly historical document.

The documented Linux toolchain is Go 1.16.15 with CGO, `-trimpath` and an empty
build ID. Public-tree verification reproduced the mining binary SHA-256:

```text
1af46493df5cd540d9a0adf532af207a477f415a05d10a099c7e59b02e1e8fdb
```

This is the qualified mining core used by the reference pool. The node binary
is a separate release from Rogue-Network; do not confuse a pool hash with a node
hash or use this module as a replacement node daemon.

The isolated race suite runs `forgepool`, `proxy`, `storage`, `rpc`, `payouts`,
`forgesigner` and `cmd/forge-live-signer` against a new disposable Redis. Some
separate native/process integration tests require their own external fixture
environment; the local suite is not a fresh production-native mining run.

## Public setup helpers

`operator/make_config.py` and `operator/work_gate.py` are public setup additions.
They remove reference-server bindings, supply the fixed treasury/signer profile
and enforce an explicit gate before stateful work issuance. The gate targets a
dedicated node already configured with the operator's own beneficiary; it does
not switch the reference deployment's pre-fork miner address automatically.

Helper tests use simulated RPC and a temporary private config directory. They
cover the activation boundary, unarmed/no-peer/sync/config/wallet failures,
automatic public commitments and private file modes. These are not a completed
fresh third-party mainnet deployment or a generic automated service installer.

## Separate payment reference

The Unix-capable payout worker is a separate copy with the documented transport
patch. Its public builder reproduced qualified binary SHA-256:

```text
8d76210f9f5ac05a67cbc9f3b9a550784fc4fe9164dab9ec3d8f7ca7a890fcb4
```

The wallet-service public source differs from the deployed private profile only
by accepting an operator-configured, nonzero address instead of the reference
pool address. It keeps restrictive chain, endpoint, path and transaction rules.
It was compiled and race-tested against the previously qualified node crypto
source snapshot, recorded by archive hash in the helper evidence. The public
builder instead requires a clean checkout of the pinned public node commit.
The archive-driven local check does not claim that the public Git checkout
workflow was separately exercised in that run.

Payment tests use a public fixture key and simulated upstream, not a production
wallet. They do not authorize or send a real payment. A third-party operator
must qualify funding, credentials, canonical receipt and restart reconciliation
before enabling a live worker.

## Website and publication

The optional website source is derived from the reference mainnet website with
public example configuration and adjusted local endpoint defaults. Its build
produced 15 static pages in a temporary directory. No generated production
website, history database, web auth file, certificate account or native private
configuration is included. The health collector is reviewable source with
deployment bindings marked for adaptation, not a generic installer.

The publication audit starts with an explicit source allowlist, excludes runtime
files, checks fixed deployment bindings/credential patterns and inspects the
small archived replay fixture. A manifest and per-file SHA-256 list describe the
final payload. Test fixture keys and candidate receipts are public test data;
they are not live accounts or production ledger exports.

No production node, native backend, approval key, wallet, ledger or running pool
was changed while preparing this source. No third-party operator integration,
GitHub publication or new mainnet mining result is claimed by these checks.
