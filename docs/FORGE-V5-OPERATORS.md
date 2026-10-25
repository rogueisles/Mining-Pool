# FORGE V5 operator guide

**Production activation: RGN block 1,121,800. Upgrade validating nodes and participating pools/signers before that height.**
Keep the working production DGB/DOGE configuration until a reviewed coordinated
release is approved. No production administrator private keys are supplied or
generated. Test fixtures and private-CPU configurations must never receive funds
or be installed over mainnet data.

V5 adds SOQ, compatible optional coins and delayed treasury/admin-key rotation.
The existing RGN history and per-block reward allocation remain unchanged. The
future V5 target is 12.5 seconds, activated by `registryBlock`; historical V4
blocks retain the original 12-second target. Read the companion node's
[protocol specification](https://github.com/rogueisles/Rogue-Network/blob/main/docs/FORGE-V5-REGISTRY.md)
alongside this operational guide.

## Who upgrades

| Participant | Initial V5 activation | Later compatible coin addition |
| --- | --- | --- |
| RGN validators, including exchanges | Compatible node and identical approved fork configuration | No new node update for a supported registration |
| Participating pool | V5 job/proof integration and configuration | Native backend/adapter/configuration for the new coin |
| Mining signing services | V5 validation and trusted RGN registry connection | Native integration and approved public treasury mapping |
| Registry administrators | Separate offline approval workflow | Approve a bounded registration or rotation |
| Miners | Keep ordinary Scrypt Stratum settings | No new miner algorithm or firmware for compatible additions |

At the initial height H, V5 becomes mandatory for every new RGN block. Old V4
proofs are rejected after H to prevent bypassing rotated treasuries. Updating only
a pool cannot activate V5 on a network still running old validators.

Future additions are limited to installed proof profiles. Adding an unrelated
algorithm or new verification format is not a registry setting. Pool/template/RPC
submission faults can usually be repaired without another RGN upgrade when the
corrected proof follows installed rules. Re-registering a coin does not fix an
RGN consensus-verifier bug.

## Mining and treasury rules

The immutable core identities are DGB parent ID 0, DOGE AuxPoW ID 98 and SOQ
AuxPoW ID 21329. At least one must occur in each authorized job; any nonempty
subset can carry RGN. Pools choose the healthy supported sources they actually
operate. Optional registered extras cannot replace this core requirement.

An RGN block does not have to meet every native chain's target. Native wins are
submitted independently and earn native rewards only if canonical and mature.
RGN share credit/payment rounds remain separate from native outcome records.
There is no automatic sale, conversion, buyback or burn in this mining backend.

**Every included native reward pays its approved registry destination.** SOQ is
globally committed in V5, not a pool-local wallet choice. A different public
address in local configuration cannot override the registered output-script hash.
Native rewards are not additional RGN miner income. Existing pool operators must
not promise the same native coinbase revenue to their existing users while also
committing it to the RGN treasury in a FORGE job.

Normal RGN subsidy allocation remains 40% miners, 30% staking, 20% P2E and 10%
treasury. Use `eth_getBlockReward` and canonical receipts for accounting rather
than crediting the whole gross subsidy to miners. Block-indexed issuance/era
heights do not change. Ideal 12.5-second spacing implies about 4% fewer blocks
per day and 4.17% longer calendar estimates than 12 seconds; actual intervals and
native rewards vary.

## Components and key separation

Run a synchronized RGN node, Redis/accounting journal, compatible pool, approved
online work signers and native backends for included coins. Ordinary RGN nodes
and exchanges do not need native daemons or signing keys.

Keep these authorities distinct:

- Mining-work signer keys approve valid jobs after independent native checks.
- Registry administrator keys approve future compatible coin/treasury changes.
- Native treasury wallet keys spend already received native funds.
- The pool's RGN payment key pays miners according to its separate ledger.

Registry administrator keys must be offline and disjoint from active/pending
mining signer keys. They must never appear in the pool, signer service, Docker
image, public source or `registryOperationFile`. The offline tool below generates
public requests/signatures only; it does not generate production keys or contact
RPC. A production key ceremony/custody plan must be approved separately.

## Qualified build environment

The Linux AMD64 release binaries were built on **Ubuntu 22.04** with
**Go 1.16.15**, GCC 11, libc development headers and **CGO enabled**. The native
compiler flags are `CGO_CFLAGS="-O2 -g0"` and `CGO_CXXFLAGS="-O2 -g0"`.
Keep module dependencies locked by `go.mod` and `go.sum`; use
`go build -mod=readonly -trimpath` for the pool, signer and administrator tool.
Other toolchain/platform combinations are not covered by these Linux artifact
checks. See the release qualification/build reports for exact artifact hashes.

## Pool configuration

V5 settings live under **`proxy.forge`**, not at the pool JSON root. These are
field names/types, not a complete runnable configuration:

| Field | Type / purpose |
| --- | --- |
| `enabled` | `true` for FORGE |
| `externalChainPolicy` | Exactly `"registry-v5"` |
| `multiChain` | `true` |
| `registryBlock` | Numeric H matching the node and signers |
| `registryOperationFile` | Optional absolute path to public assembled operation JSON |
| `treasuryDestinations` | Public `{ "script": "HEX", "address": "NATIVE_ADDRESS" }` entries |
| `treasuryScripts` | Optional additional raw public script hex values; SOQ also needs its public address |
| `digibyte`, `dogecoin` | Native endpoint objects for configured core sources |
| `additionalAux` | SOQ and configured compatible extras, with `symbol`, `chainId`, `genesis`, `payoutAddress`, `endpoint` |
| `templateSigners`, `templateQuorum` | Approved public work signer endpoints/addresses and quorum |
| `extranonce1Size`, `extranonce2Size` | `4`, `4` for this job builder |
| `accountingJournalDir` | Dedicated durable journal directory |

Leave the earlier `optionalSoqBlock`-only proposal disabled. The legacy setup
generator and mainnet gate below the root README describe V4; they do not generate
a V5 production configuration or approve an activation height.

`digibyte`, `dogecoin`, and `additionalAux[].endpoint` use these exact fields:
`name`, `url`, `timeout`, `username`, `password`, `payoutScript`. Native RPC must
remain authenticated and private; auxiliary endpoints use explicit loopback IPs
and ports. Keep credentials in local private configuration, never public examples.

An illustrative SOQ auxiliary entry is:

```json
{
  "symbol": "SOQ",
  "chainId": 21329,
  "genesis": "0d828600816cbd7c23789660b53f90cb6ec7ff85540698e13845eb2d2f0486a8",
  "payoutAddress": "APPROVED_PUBLIC_SOQ_ADDRESS",
  "endpoint": {
    "name": "SOQ",
    "url": "http://127.0.0.1:33389",
    "timeout": "3s",
    "username": "LOCAL_RPC_USER",
    "password": "LOCAL_RPC_PASSWORD",
    "payoutScript": "APPROVED_PUBLIC_NATIVE_SCRIPT_HEX"
  }
}
```

This is an **unusable placeholder**, not a funded-wallet configuration. Replace
address/script/credentials using reviewed local data. Native mainnet and regtest
genesis hashes differ; never point staging software at a mainnet wallet.

For rotation, provision the new public mapping before its height:

```json
{
  "treasuryDestinations": [
    { "script": "CURRENT_PUBLIC_SCRIPT_HEX", "address": "CURRENT_NATIVE_ADDRESS" },
    { "script": "NEXT_PUBLIC_SCRIPT_HEX", "address": "NEXT_NATIVE_ADDRESS" }
  ]
}
```

These entries belong inside `proxy.forge`. The effective registry SHA-256 script
hash selects the mapping. Provision the same destination in every participating
work signer. Missing mappings cannot redirect rewards; the source is omitted or
its work is refused. SOQ requires the native node to validate its witness-v1
address and exact script.

## Work signer configuration

A V5 mining signer uses these exact top-level fields:

- `externalChainPolicy: "registry-v5"` and matching numeric `registryBlock`.
- `registryChainId`: exact RGN chain ID, e.g. 70088 for production **only after**
  the coordinated release, or 7008902/7008914 for the appropriate isolated lab.
- `registryRpc`: `{ "url", "username", "password", "genesis" }` for its trusted
  authenticated loopback RGN connection. `genesis` is the RGN genesis hex without
  `0x`, not a native genesis.
- `multi`: configured native entries with `symbol`, `chainId`, `payoutScript`,
  optional `payoutAddress`, and native `url`, `username`, `password`, `genesis`.
- Optional `treasuryDestinations`: public current/next `{ "script", "address" }`.
- Existing `listen`, `signerAddress`, and `keyFile` for the **mining** signer key.

The signer retrieves the registry for the work header's exact parent from its
trusted RGN node, verifies membership and administrator approvals, checks native
identity/health/tips, rebuilds candidates/coinbases and validates full reward
claims. Native overclaim probes must be rejected. RGN nodes do not perform these
native RPC checks themselves or run full native-chain consensus.

## Offline administrator workflow

Build the utility with the reviewed Go environment used by this source:

```bash
go build -o forge-registry-admin ./cmd/forge-registry-admin
```

The following workflow creates **public files only**. First save a successful
`eth_forgeRegistry` result for an independently verified exact parent hash from
your trusted upgraded node. A plain result object or complete JSON-RPC 2.0
response is supported. The node must already have the agreed staging or future
registry configuration; current unscheduled mainnet returns “not scheduled”.

```bash
curl --fail --silent --show-error http://127.0.0.1:18545 \
  -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"eth_forgeRegistry","params":["0xEXACT_PARENT_HASH"]}' \
  > registry-state.json
```

Use the actual private node endpoint and complete verified hash. Independently
compare RGN chain ID, genesis, parent hash and registry root. Offline file parsing
cannot establish that a malicious saved state came from the canonical network.

### Prepare and inspect a change

`change.json` uses the public `RegistryChange` field names. Unused fields are
omitted/zero. `Sequence` can be omitted: `prepare` fills the next sequence from
the saved parent state. Example **template** for a delayed SOQ treasury rotation:

```json
{
  "Kind": 2,
  "Slot": 3,
  "ActivationHeight": 200,
  "Treasury": "0xREPLACE_WITH_EXACT_SHA256_OF_REVIEWED_NATIVE_SCRIPT"
}
```

Height 200 is an isolated-lab illustration, not an RGN mainnet activation. Use an
appropriate future height satisfying the network's notice/rate limits. Compute
`Treasury` from raw native script bytes, not address text or an RGN address. All
hash placeholders must be replaced before the strict tool accepts the file.

```bash
./forge-registry-admin prepare \
  --state registry-state.json --change change.json \
  --chain-id 7008902 --genesis 0xVERIFIED_RGN_LAB_GENESIS \
  --out request.json

./forge-registry-admin inspect --request request.json
```

For a different isolated network use its actual chain ID/genesis. The tool pins
them explicitly and rejects a mismatching saved state. Review the kind, slot,
native identity, treasury hash, effective height, current/new keys, quorum,
parent root and approval digest. The preview inclusion height is not signed;
the activation deadline and parent registry state are signed.

The other request forms are:

| `Kind` | Required fields |
| --- | --- |
| `1` register extra | `ActivationHeight`; `Coin` with `Slot` 4–16, unique `ChainID`, native `Genesis`, `Profile: 1`, `EnabledFrom` equal to activation, and approved `Treasury` hash |
| `2` rotate treasury | Existing `Slot`, `ActivationHeight`, new `Treasury` hash |
| `3` disable extra | Extra `Slot` 4–16 and `ActivationHeight`; core slots are forbidden |
| `4` rotate admins | `ActivationHeight`, sorted distinct `Keys` public addresses and `Quorum` |

Registrations cannot claim a new algorithm/profile, change a core identity or
erase an active/pending entry. To reuse an optional slot, its previous disable
height must already have taken effect. A still-pending treasury/admin rotation
cannot be overwritten.

### Sign on separate administrator machines

Each administrator checks the reviewed digest and signs locally. The key file
contains only that administrator's 32-byte hex key, protected from other users
(`chmod 600` on Unix). Do not pass private keys on the command line, use a mining
key, upload the key file or add it to source control.

```bash
./forge-registry-admin sign --request request.json \
  --key-file /OFFLINE/PRIVATE/admin.key \
  --approve-digest 0xEXACT_REVIEWED_APPROVAL_DIGEST \
  --out admin-a.signature.json
```

Repeat independently for the exact current quorum. Only the public signature
files are shared. Duplicate or unauthorized signatures, including mining signer
keys, cannot satisfy quorum. The tool never prints or serializes the private key.

For administrator rotation the **incoming quorum** additionally signs its
distinct `newAdministratorAcceptanceDigest` from `inspect`:

```bash
./forge-registry-admin sign --request request.json --accept \
  --key-file /OFFLINE/PRIVATE/new-admin.key \
  --approve-digest 0xEXACT_REVIEWED_ACCEPTANCE_DIGEST \
  --out new-admin-a.acceptance.json
```

This prevents rotating authority to keys whose possession was never demonstrated.
Once the rotation is effective, retired keys cannot approve subsequent changes.

### Assemble and include

```bash
./forge-registry-admin assemble --request request.json \
  --signature admin-a.signature.json \
  --signature admin-b.signature.json \
  --signature admin-c.signature.json \
  --out approved-operation.json
```

For administrator rotation include the exact incoming quorum as additional
repeated `--signature` flags. The utility recovers/sorts signers and validates
the resulting operation; it does not broadcast, mine or contact RPC. Output
paths must be new files; existing files are never overwritten. Do not hand-edit
signature encodings in the assembled operation JSON.

Copy only the public `approved-operation.json` to the pool's configured
`registryOperationFile`. The pool recomputes the result root for the actual block
height while retaining the approved change/signatures. Inclusion happens only in
a mined valid V5 block, not merely by saving the file. Verify canonical inclusion,
checkpoint, effective height and reward destination independently.

A mined operation changes the parent registry root/sequence. Other old-root
approvals become stale and need review against the new state. An approval can
also miss its notice deadline or cross an administrator authority change. A stale,
malformed or unapproved file does not authorize a change; existing valid mining
continues and the operator must prepare a corrected public request.

## Availability, rewards and recovery

Keep healthy DGB/DOGE sources available while testing SOQ/extras. Source failures
must remove that source and rebuild eligible work; healthy established work must
not depend on an optional integration succeeding. If all core sources, signing
quorum or hashpower are unavailable, block production can pause. Recovery restores
valid services rather than relaxing verification. No design guarantees uninterrupted
production under every failure.

Monitor native synchronization/peers/tips, proposal validity, signing latency,
Stratum accepted/rejected shares, RGN canonical blocks, native canonical rewards,
treasury maturity, durable journal backlog, Redis health, payment receipts and
disk space. Configure restart policies, bounded logs and disk alerts. Preserve
journals/wallets and test restoration; do not delete unresolved submissions merely
to make a health indicator green.

Lost native replies remain durable and are reconciled against the native chain.
Accepted submission observations are not spendable treasury balances. A native
win cannot open an RGN payout round, and an outcome update must not credit a share
again. Keep the old wallet keys for existing funds after a receiving-address
rotation; the rotation itself transfers no balances.

## Limits and qualification

Consensus limits are 16 registered slots, six native sources per job, five admin
keys, one operation per block, a 32-leaf registry with five-hash membership paths,
a 32-leaf canonical AuxPoW tree and 16 KiB for the complete proof. Installed
auxiliary IDs must be at most 65535 and have distinct residues modulo 32 from
RGN/core/other registered auxiliaries. Native RPC/proof/profile compatibility is
required in addition to Scrypt. Operator runtime configuration is bounded too;
consult the final validated configuration for enabled endpoint counts.

Unit/race and offline-tool tests passed. Private-CPU/native-regtest tests
demonstrate actual blocks, transfers, peer connections, restart/catch-up, fresh
synchronization, treasury rotations and a compatible extra. Additional real RGN
P2P tests between staging servers 177 and 63 passed synchronization, mining and
transfer agreement, plus restart/catch-up, through controller-managed SSH forwards.
Compiled pool/signer processes mined canonical private RGN blocks over actual
Stratum and canonical DGB, DOGE, SOQ and compatible-extra regtest blocks. Pool
restart, SOQ outage/recovery, all-native outage/pause/recovery and mining-quorum
loss/expiry/recovery checks passed.

The pool test's identity bridge overrides only the native chain-name label and
DOGE's out-of-range historical checkpoint. Native genesis, peers, templates,
full-block proposals and submissions remain real. Private RGN difficulty and
native regtests do not certify mainnet reward income, canonical production
difficulty or production activation. Read the final qualification report for
artifact identities and exact scope; an incomplete gate remains incomplete.

Bounded validation stress passed 10,000 unique headers at the maximum 16-coin,
six-source registry, with five administrators and 156 treasury operations. A
fixed 256-header reader retained about 1.34 to 1.38 MB of Go heap after GC, with a
4.40 MB sampled peak. This is validation retention, not a full database/network
stress test or a guarantee about native-daemon memory. Same-run development
benchmarks measured about 0.50 ms for V5/two-source full verification and 1.68 ms
for a maximum administrator rotation; both deliberately fail only final PoW and
do not establish successful mining. See the protocol guide and final report for
allocation, fixture and storage limitations.

Before production scheduling, approve administrator custody/public keys and the
bootstrap/profile checksums, complete final artifact and staging tests, obtain
validator/exchange/pool/signer readiness, and announce a future height. Before H,
an unqualified candidate can be withdrawn. After V5 blocks exist, reverting
validators to V4 is not a safe rollback; pools can omit optional sources while
nodes retain V5 validation. The release retains V4 below block 1,121,800; V5 is required from that height. Publishing files alone does not update running nodes.
