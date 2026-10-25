# RGN + optional SOQ: pool integration candidate

> **Archived earlier V4 proposal; superseded by V5.** No mainnet activation was
> scheduled by this document. Its `dgb-doge-soq` policy and pool-local SOQ wallet
> instructions are not V5 deployment instructions. Read the
> [FORGE V5 operator guide](docs/FORGE-V5-OPERATORS.md) for approved global native
> treasuries, `registry-v5`, separate offline administration and the height-gated
> 12.5-second target. The remainder is preserved as design history.

**Not activated on RGN mainnet. Keep the current working production setup.**

See the companion node upgrade document for the height-gated consensus change.
This reference implementation adds SOQ to the existing shared Scrypt job and
submits each native winner independently. Miners keep using ordinary Scrypt
Stratum; no new mining algorithm or miner firmware is introduced.

## Availability

After the final fork height, a pool may supply RGN work with any nonempty subset
of DGB, DOGE and SOQ. The reference pool tries all configured healthy sources.
If SOQ is unavailable or cannot get signer quorum, it rebuilds DGB/DOGE work.
If DGB and DOGE are unavailable but SOQ is healthy and authorized, SOQ can carry
RGN work. If all configured native sources are unavailable, work pauses and
miners are disconnected until valid work returns. The existing DGB/DOGE health
hysteresis remains; a transition can briefly pause during independent checks.

Once the new fork is active, a signer can authorize a healthy subset even when
other sources are healthy. It requires an RGN header whose hash equals the work
leaf and whose number satisfies the activation setting. A caller-supplied number
alone is insufficient. SOQ requires a V4 certificate.

The current `dgb-doge` policy remains available. The new explicit policy is
`dgb-doge-soq`. The legacy development five-chain policy is unchanged; PEP,
DINGO and BELLS are not enabled on RGN mainnet by this change.

## SOQ backend

Use official SOQ v2.5.3 or a subsequently reviewed compatible version. Its
mainnet genesis is:

```
0d828600816cbd7c23789660b53f90cb6ec7ff85540698e13845eb2d2f0486a8
```

Run the backend with `enablemining=1`, `disablewallet=1`, `txindex=1`, and
authenticated RPC bound to loopback. A remote backend should be reached over an
authenticated private tunnel. Do not publish native RPC or place RPC passwords
or signer keys in GitHub. The node stores no SOQ spending key.

The pool checks mainnet identity, synchronization, peers, tip freshness, native
address decoding and the exact payout script. It negotiates SegWit templates,
builds the native coinbase, and asks the daemon to validate both the full claim
and a one-base-unit overclaim probe. Signers repeat native validation independently.

Official integration reference:
https://github.com/soqucoin/soqucoin/blob/main/doc/pool-integration.md

## Configuration, after review

Keep all existing RGN/DGB/DOGE settings. The reference pool adds
`proxy.forge.optionalSoqBlock` and selects `externalChainPolicy: dgb-doge-soq`.
`additionalAux` contains one SOQ entry with `symbol`, `chainId`, `genesis`,
`payoutAddress` and `endpoint`. The endpoint has the existing `url`, `username`,
`password`, `timeout` and `payoutScript` fields. A public example fragment is
included in the review bundle; its placeholders are not runnable credentials.

Each signer selects the same policy and `optionalSoqBlock`, retaining DGB and
DOGE entries in `multi` and adding SOQ. Its SOQ entry includes `payoutAddress`,
`payoutScript`, `chainId`, `genesis` and the signer's own authenticated loopback
RPC endpoint. The four existing signer identities and two-approval quorum stay
unchanged. No SOQ address is added to the node's consensus configuration.

The pool's current intended SOQ reward destination is:

```
sq1ptk79z875zygp72awur73j9zk332r0vqkjwcsp3mcdg73nqyuwktq87gczj
```

Native validated script:

```
51205dbc511fd411101f2baee0fd1914568c5437b01693b100c7786a3d19809c7596
```

This is this pool's destination, not a requirement for other pools. Spending is
handled by the address owner's SOQ wallet. This integration does not need its
seed or private key. SOQ coinbase maturity is 288 native blocks.

## Work and proof changes

The node's `eth_createAuxBlock` adds `headerRlp` and `optionalSoq`. An upgraded
pool binds the unsealed header into `MultiSignRequest.RGNHeader` and checks both
the local activation height and the node's authorization flag. SOQ must not be
requested or included before activation. The V4 proof encodes SOQ as another
sorted native auxiliary reference and full coinbase/header/merkle evidence.

The shared parent contains RGN and all selected native leaves. Native SOQ wins
use the existing full-block `submitblock` path with the SOQ AuxPoW branch; the
independent DGB/DOGE paths are preserved. Every target is checked separately.

## Accounting and recovery

SOQ outcomes have a separate `soqucoin` field in receipts and observations, a
`soq` Redis submission ledger and their own durable submission intents. A SOQ
win does not create an RGN payout round. Lost replies are checked against the
native chain before reconciliation, without resubmitting already confirmed
payloads or crediting shares twice. Submission acknowledgment is not proof of a
canonical or mature native reward.

Current RGN miner accounting, hourly payout interval, fee policy and reward
allocation are preserved. SOQ is paid directly in the native coinbase to the
pool destination. It is not silently added to miners' RGN payout balances.

## Reproduce the checks

Use Go 1.16.15 and the repository's existing dependency pins. Run
`scripts/test-rogue-source.py` with a local Redis binary and a new report
directory; it starts its own disposable database and enables the race detector.
Use the supplied native lab driver for seven local regression-chain combinations.
The pool fixture export and companion node tests establish proof interoperability
and activation boundaries. The `forge-soq-preflight` command reads private RPC
settings from stdin and validates an unsolved native template; it has no submit
operation. Never use the regtest block-submission tests against mainnet.

Publication and activation are intentionally separate from this review bundle.
