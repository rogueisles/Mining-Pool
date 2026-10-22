# Operating and recovering a reference pool

## Separate responsibilities

The RGN node validates the chain. The FORGE pool builds and submits mining work.
Public template signers approve eligible native commitments. A separate private
wallet service signs RGN payments. These are different processes and roles;
template approval keys never belong in a pool payout wallet.

Use a dedicated node for mining work so beneficiary changes cannot interfere
with another operator. Preserve its datadir and identity on updates. Keep live
accounting and pending-submission state persistent across container or service
replacement. Keep a consistent backup before changing a ledger schema.

## Configuration that is fixed versus operator-owned

Fixed for this network release: chain/genesis, activation height, V4 commitments,
approved signers/quorum, treasury scripts, reward schedule and validity rules.
Do not edit the bundled consensus profile to work around rejection.

Operator-owned: pool beneficiary, native RPC connection details, private Redis,
Stratum address/port, frontend hostname, service users, backups and payment policy.
The config generator owns only the new output directory. It does not install
daemons, edit firewall rules, overwrite an old pool or connect to production.

## Before opening Stratum

1. Match the canonical RGN chain and height against another independent peer.
2. Check full node config with `operator/work_gate.py --check`. It is read-only.
3. Confirm both native mainnet backends are synchronized, correctly authenticated
   and provide the proposal/template functions the reference implementation uses.
4. Run isolated source and helper tests. Review submitted-proof and accounting
   journals, free disk, native chainstate growth and service restart behavior.
5. Confirm the pool beneficiary, public signer set and automatic treasury scripts.
6. Arm the work gate explicitly, keeping candidate-height gating intact.
7. Qualify actual hardware/client shares, canonical blocks and recovery before
   advertising the service or accepting rental hashpower.

For a local pause, disarm the gate:

```bash
python3 operator/work_gate.py --config "$RGN_PRIVATE/gate.json" --disarm
```

This refuses new work issuance and proof submission through the gate. It is not
a remote ASIC stop command. Stop the pool's mining service as well when taking
the pool out of service; do not keep advertising an unavailable endpoint.

## Failure handling

| Condition | Required response |
| --- | --- |
| No eligible DGB or DOGE candidate | Pause/refresh work; never issue a zero-participation proof |
| Fewer than two distinct valid approvals | Pause new jobs; never fabricate, repeat or bypass signatures |
| Native tip or RGN head changes | Rebuild the affected work and reject stale submissions correctly |
| Redis or journal unavailable | Protect durable accounting and pause if the backend cannot accept work |
| Wrong genesis, config, wallet, no peers or syncing | Refuse work and investigate the configuration/connection |
| Native daemon warming up | Wait for healthy RPC; do not erase chainstate or loosen policy |
| Unknown submission result | Reconcile the original candidate; preserve the exact proof bytes |
| Unknown payout result | Keep the signed journal and ledger lock; reconcile the exact transaction hash |
| Disk space below operational guard | Pause affected services and reclaim only verified obsolete data |

Do not solve a fault by deleting journals, flushing Redis, changing consensus,
replacing approved treasury destinations or publishing administrative RPC.

## Accounting and payout recovery

Run one owner for each ledger role. The unlocker must use `forgeBlock=1100100`
and canonical miner rewards, not the legacy pre-fork formula after activation.
Retain historical ledger identifiers even when the frontend says RGN.

A payout timeout can mean the transaction was broadcast. Inspect the private
signed journal, transaction hash, canonical receipt, nonce and pending ledger
payment before retrying. A second fresh transaction may pay the same debt twice.
Do not clear a payout lock or use legacy `RESOLVE_PAYOUT=1` blindly; that option
requires a reviewed reconciliation of the actual ledger and chain outcome.

The optional private wallet service journals signed bytes before broadcasting
and refuses another payment while the previous intent is unresolved. Recovery
must reuse/reconcile that same intent. A canonical successful receipt settles
the service journal; the worker separately settles its ledger record. Reorgs
still require operational reconciliation. Keep backups and monitor both sides.

The sample interval `1h` is a recurring worker delay. The worker also processes
eligible balances immediately on startup; it is not a promise to pay exactly
at every wall-clock hour. Payment requires a matured ledger balance over the
threshold, wallet funds for value plus gas, healthy peers and a working signer.

## Public health

Publish only selected status: network height, sync state, eligible participation,
job freshness, quorum availability, accounting readiness and payment state.
Never expose native RPC credentials, private endpoints, wallet password files,
keystores, raw signed transaction journals or administrative controls.

The core API and `cmd/forge-status` expose reference accounting/status data.
The optional website consumes a read-only adapter. Its richer mainnet health
schema is documented in `website/README.md`; merely serving static HTML does
not establish mining readiness. Distinguish `awaiting-fork-height`, accepting
work, unavailable and recovery-required states. Do not show a green ready label
from stale data or the existence of a listening port alone.

## Services and persistence

Supervise the node, private native connections, work gate, mining backend, API,
unlocker and optional payment processes independently. Use service users, private
file permissions, restart policies and clean stop timeouts. A generic systemd
template is provided as an example, not a production installer.

Back up the ledger and journals together using a consistent procedure. Pruning
native block files does not cap chainstate or wallet size. RGN full state garbage
collection is not a bounded-history pruning promise. Never remove a database
while the node has it open. Published node updates should preserve genesis,
balances, contracts, identity and consensus configuration.
