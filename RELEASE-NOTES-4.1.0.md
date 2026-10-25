# Rogue Network 4.1.0 — FORGE V5

**Required coordinated upgrade before block 1,121,800.**
Estimated activation: **27 October 2026 around 13:35 UTC**. The block height
determines activation; this time is an estimate and can change with block speed.

## What changes

- Optional **SOQ** native merged-mining work joins the existing DGB/DOGE core set.
  A valid job needs at least one eligible core source; healthy DGB/DOGE work can
  continue when SOQ is unavailable.
- A bounded, chain-committed registry supports later compatible coins and delayed
  treasury-address changes. Separate **3-of-4 administrator approvals** authorize
  these operations; ordinary mining services do not hold that authority.
- The target becomes **12.5 seconds from block 1,121,800**. Original genesis,
  historical validation, reward allocations and rewards per block remain intact.
- Updated pool/signers switch from V4 to V5 at the agreed height and preserve
  durable accounting, native submission reconciliation and recovery safeguards.

Future additions must fit the installed proof verifier and limits. This is not
permission to add arbitrary algorithms or repair consensus code through a registry
entry. Those changes can require another coordinated software upgrade.

## Who must update

**Node operators and exchanges:** install the new binary/image with the supplied
scheduled mainnet profile before activation. Preserve the existing data directory
and wallet custody. All validating nodes need this initial upgrade, including
nodes that do not mine. A pool-only update is insufficient.

**Pool operators:** also update the adapter, work signers, exact-profile safety
gates, readiness checks and health display. Keep native RPC/payment credentials
private. DGB/DOGE treasury commitments are preserved; verify any SOQ endpoint and
the approved SOQ receiving script before enabling it. See the complete integration
guide in Mining-Pool.

**Miners:** compatible Scrypt miners can use the pool's existing Stratum endpoint
and public RGN worker address. An ordinary node binary does not launch a full solo
miner. Share acceptance and native block wins are different events; each chain
retains its own difficulty, canonical-chain rules and maturity.

**Wallet users:** this node release does not require sharing/importing a seed or
private key. Use a service whose RGN node is updated. No token swap is introduced
by this upgrade.

## Downloads and verification

- Node binaries: https://github.com/rogueisles/Rogue-Node/releases/tag/v4.1.0
- Complete node source: https://github.com/rogueisles/Rogue-Network
- Pool, work signer and offline administration: https://github.com/rogueisles/Mining-Pool
- Docker: https://hub.docker.com/r/roguenetwork/rogue-network
- Pool: https://pool.rogueisles.com/
- Documentation: https://rogueisles.com/atlas/
- Explorer: https://scan.rogueisles.com/

The supplied binary artifacts target **Linux AMD64 / Ubuntu 22.04 or compatible**.
Check SHA256SUMS before installation and PACKAGE-SHA256SUMS after extraction.
The exact approved scheduled profile SHA256 is:

```text
87c66819311b112adce09855f9c2c38490fb32a193ea35ca7c466892319aefd8
```

```sh
docker pull roguenetwork/rogue-network:4.1.0
```

Use the versioned tag and recorded immutable digest. Upgrade instructions in the
node/Docker README preserve the data volume and bind RPC privately. Verify the
loaded profile, canonical head, connected peers and continued chain progress.
Local identity health alone is not evidence of complete synchronization.

## Treasury administration and limits

The approved bootstrap contains the four supplied public administrator addresses
and a 3-of-4 quorum, separate from the unchanged mining work quorum. Initial wallet
control is the operator's custody assertion; the release process neither generated
nor obtained the corresponding private keys. Administrator operations need at
least 720 blocks of notice and are spaced at least 64 blocks apart.

The protocol caps the registry at 16 slots, native sources per job at six,
administrator keys at five, operations at one per block and complete proof data
at 16 KiB. Ordinary RGN validators do not run a full node for every native chain.
Treasury rotation changes future outputs; it does not transfer old wallet funds.
No automatic sale, buyback, conversion or burn is introduced by this release.

## Test scope and recovery

Qualification includes private Scrypt mining through compiled pool/signers,
canonical RGN/native regtest blocks, all seven core-source subsets, transfers,
peer synchronization, fresh synchronization, service/native/quorum recovery,
durable accounting, treasury/admin rotation and a later compatible extra coin.
Real DGB/DOGE/SOQ mainnet proposal checks accepted valid unsolved block bodies and
rejected reward overclaims. These are not claims of a mined mainnet SOQ reward.
See the attached qualification/build reports for artifact identities, container
checks, regression results and measured verification/memory scope.

Before activation, a coherent rollback can withdraw the future schedule. After
V5 blocks exist, retain V5 validation and repair or omit optional native sources;
reverting to a V4-only validator is unsafe. If every eligible core source, mining
quorum or effective hashpower is unavailable, block production can pause.
