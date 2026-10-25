# Rogue Network Pool — FORGE V5

Reference Scrypt mining pool and work signer for Rogue Network (RGN), including
DigiByte, Dogecoin and optional SOQ native work. The coordinated V5 upgrade is
scheduled for **RGN block 1,121,800**. V4 continues below that height. The new
target is **12.5 seconds from activation**; historical rules and rewards per
block remain unchanged.

**All RGN validators, including exchange nodes, must upgrade before activation.**
Pool operators must also update their mining adapter, signing services and safety
checks. Updating only a pool does not upgrade other validating nodes.

- [Complete V5 integration and administration guide](docs/FORGE-V5-OPERATORS.md)
- [Protocol and proof overview](docs/FLEXIBLE-FORGE-V5-CANDIDATE.md)
- [Approved public activation profile and review](config/MAINNET-V5-RELEASE.md)
- [RGN node source](https://github.com/rogueisles/Rogue-Network)
- [Node downloads](https://github.com/rogueisles/Rogue-Node/releases/tag/v4.1.0)
- [Docker](https://hub.docker.com/r/roguenetwork/rogue-network)
- [Reference pool](https://pool.rogueisles.com/)

## How mining works

Miners connect using Scrypt Stratum. The pool builds compatible native templates
and an RGN commitment, obtains the existing authorized work quorum, and submits
winning work to the applicable native chains and RGN. A share is not a block;
each chain applies its own target, canonical-chain rules and reward maturity.

At least one eligible, healthy **DGB, DOGE or SOQ** core source must be present.
An optional source failing does not make other eligible core sources invalid.
If every core source, the work quorum or effective hashpower is unavailable,
block production can pause until those services recover.

Included native coinbase rewards are committed to their approved treasury
destinations. RGN miner accounting and RGN payments remain separate. The upgrade
does not automatically sell native rewards, buy RGN, burn funds or move existing
wallet balances. Native payouts become usable only after their own maturity.

## Connecting miners to the reference pool

```text
Algorithm: Scrypt
URL:       stratum+tcp://stratum.rogueisles.com:3333
Worker:    YOUR_PUBLIC_RGN_ADDRESS.worker1
Password:  x
```

Use a public receiving address, never a private key. Node binaries do not start
a complete solo miner by themselves. Independent pools need their own miner
ledger, beneficiary and protected payment wallet in addition to the adapter.

## Building and configuring an independent pool

The release contains Linux AMD64 pool, work-signer and offline administrator
tools. Source builds use **Go 1.16.15 on Ubuntu 22.04 with CGO enabled**, as
described in the operator guide; dependencies remain locked by `go.mod` and
`go.sum`.

```sh
go build -mod=readonly -trimpath -o rgn-pool .
go build -mod=readonly -trimpath -o rgn-forge-signer ./cmd/forge-live-signer
go build -mod=readonly -trimpath -o forge-registry-admin ./cmd/forge-registry-admin
```

Read the complete operator guide before creating a runtime configuration. Use
the same approved RGN activation profile and parent-bound registry state. Keep
native RPCs and payment wallets private. Never copy registry administrator keys
onto a pool or online work signer. Example/test keys are not production keys.

Before the activation height configure only the established DGB/DOGE endpoints
and, optionally, the specifically supported SOQ endpoint. Other registered
extras should be enabled only after V5 activates. The legacy V4 path skips SOQ;
the updated pool switches proof versions automatically at the agreed height.

Production supervision must accept the exact reviewed scheduled configuration
before H and enforce it from H. An old exact-configuration guard can otherwise
pause mining even when the node itself is compatible. Update work gates,
readiness checks and the health collector together; preserve wallet policy,
accounting journals, Redis deduplication and recovery protections.

## Future coins and treasury changes

The initial registry includes DGB, DOGE and SOQ. A **3-of-4 separate administrator
quorum** authorizes compatible future coin registrations and treasury changes.
Operations are committed in RGN blocks, take at least **720 blocks of notice**,
and are spaced at least **64 blocks** apart. All upgraded validators derive the
same effective registry from the canonical chain. Local settings cannot change
consensus or redirect a registered treasury.

Compatible additions do not require another RGN validator release when they fit
the installed verifier and bounds. Pools wanting the new coin still need its
native backend/integration. A new proof format or consensus verifier defect can
require a coordinated software upgrade; registering a coin again cannot repair
verification code. DGB/DOGE/SOQ core identities cannot be removed or disabled.
Treasury changes affect future outputs and do not transfer existing balances.

Limits are **16 registry slots, six sources per job, five administrator keys,
one operation per block and 16 KiB for the complete proof**. Supported auxiliary
IDs and their 32-leaf tree positions must not collide. Ordinary RGN validators
verify the proof and registry; they do not run every native full node.

## Verification and recovery

Private staging tests exercised actual Scrypt Stratum mining, canonical RGN and
native regtest blocks, all seven nonempty core subsets, wallet transfers,
cross-server peers, fresh synchronization, outages/recovery, durable accounting,
treasury/admin rotations and a later compatible extra. Real native mainnet
proposal checks accepted correctly constructed DGB/DOGE/SOQ bodies and rejected
overclaims. These checks are not evidence of a mined mainnet SOQ reward.

Bounded 10,000-header stress and verification benchmarks passed. Their measured
memory/timing scope is described in the qualification report; they are not a
guarantee about full mainnet database size, every server or external availability.

Back up runtime configuration, wallet custody and durable accounting before
upgrading. Replace services sequentially, preserve the mining quorum and verify
canonical heads, accepted shares and payments. Before H, a coherent rollback can
withdraw the schedule. Once V5 blocks exist, keep V5 validation and repair or omit
optional sources; reverting validators to V4 is unsafe.

[Historical V4 setup](docs/history/v4-operator-guide.md) and the old optional-SOQ
proposal are archived for reference and do not configure this release.

The 4.1.0 packaging retains the tested node's internal build identifier `rgn400v5`. This identifier is opaque; the release version, scheduled profile and published checksums identify this package.
