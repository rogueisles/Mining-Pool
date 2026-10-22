# Integrating FORGE V4 into an existing Scrypt pool

This guide is for developers who keep their existing pool engine. For an operator
who wants the reference backend, start with the root README. No engine-specific
plugin is supplied. Confirm the operator's software/version before claiming
compatibility; a public coin list does not identify an engine's implementation.

## Integration contract

RGN is an EVM account chain with a custom Scrypt proof path. It is not a
Bitcoin-family wallet daemon that can be added solely with a conventional
`getblocktemplate`/`submitblock` coin definition. Its auxiliary work interface
also differs from a generic Dogecoin `createauxblock` adapter.

The node and shared consensus configuration are published in
[Rogue-Network](https://github.com/rogueisles/Rogue-Network), pinned for this
release to `18367e0aea25899af7fe8d608f0a19fa420740a9`. Chain/network ID is 70088.
The genesis is
`0xdc4d38c0ef41d3fdf50642fdf5fc99e221eadd09e73bbe616724f55a136270ab`.

An engine needs all of the following:

1. A dedicated RGN work issuer configured with the pool beneficiary.
2. Native mainnet candidate construction with approved treasury scripts and
   the full native subsidy plus fees of included transactions.
3. Public signer requests and local verification of returned signatures.
4. Exact V4 certificate binding into the parent coinbase before job distribution.
5. Correct extranonce, byte order, Merkle branches and Scrypt target handling.
6. Separate share, RGN-block and participating native-block target checks.
7. Durable submission/recovery and canonical RGN miner-reward accounting.
8. An independent RGN transaction/payout path using chain ID 70088.

## Read-only preparation versus issuing work

Check `net_version`, genesis, consensus configuration, `eth_syncing`, peers and
canonical height before work issuance. The node configuration can be read over
private IPC through `admin_nodeInfo`; do not expose that administrative module
publicly. Compare the whole config against the bundled JSON using integers with
arbitrary precision. Some issuance values exceed JavaScript's safe integer range.

`eth_createAuxBlock` is stateful: it starts work creation. A generic monitoring
probe must not call it before activation. The supplied local adapter enforces
arming, exact network configuration, synchronization, peers, beneficiary and
candidate height before allowing it.

Eligibility starts at candidate height **1,100,100**, which can be requested when
the canonical head is 1,100,099. Old rules/rewards remain through 1,100,099.
Handle a head change during preparation by discarding stale work and rebuilding.

## RGN node work RPC

Request on your private issuer:

```json
{"jsonrpc":"2.0","id":1,"method":"eth_createAuxBlock","params":[]}
```

The result includes `proofVersion`, `workHash`, `target`, `height`, `timestamp`,
`templateMaxValidity`, `auxChainId`, `dogeAuxChainId`, `previousBlockHash`,
`difficulty` and `minerReward`. Numeric RPC quantities are hexadecimal strings.
For this mainnet release require V4 (`proofVersion` = `0x4`). Use the returned
work hash and target; do not substitute a native target or an unrelated RGN head.

Submit a fully encoded proof:

```json
{"jsonrpc":"2.0","id":2,"method":"eth_submitAuxBlock","params":["0xRGN_WORK_HASH","0xENCODED_V4_PROOF"]}
```

The strings above are schematic, not a runnable fixture. Reuse the source codec
and test vectors; concatenating JSON or substituting Dogecoin AuxPoW is incorrect.
An accepted response must still be reconciled with the canonical RGN chain.

## Public signer client

The exact four signer addresses and endpoints are in `network/public-profile.json`.
The quorum is two distinct authorized signers. Pool operators configure those
public values only; they do not generate approval identities or replace the
consensus signer list.

`proxy/forge_signer.go` implements HTTPS requests, bounded timeouts, no redirects,
no environment-proxy inheritance and response validation. Multi-chain signing
and collection are used from `proxy/forge_multi_runtime.go` (and related FORGE files).
Reuse `forgepool.MultiSignRequest`, the signing digest and certificate codec;
do not independently invent the signer JSON schema or signing domain.

The V4 work domain is `RGN-FORGE-MULTI-TEMPLATE-V4-WORK`. V3 domains retained in
source are historical and must not be used for V4 work. Collect the required
distinct signatures, validate their recovered authorized addresses and serialize
them in the ordering the codec expects. Do not count duplicate signatures twice.

Bind the **exact** final certificate into the parent coinbase before exposing
Stratum coinbase fragments. A miner's extranonce changes only the permitted
region; it must not mutate the approved body or certificate. Replacing a
certificate on an already-issued job invalidates that job's commitment.

Signers validate native candidates using their native validation access. Full
claim checking requires acceptance of the original proposal and rejection of
the same proposal with a one-base-unit overclaim. This avoids requiring every
signer to have an identical mempool. The deliberate overclaim probe is never
mined or submitted as a native block. This logic is in `forgesigner/` and
`forgepool/treasury_claim.go` for independent review.

## Native participation and treasury

The `dgb-doge` profile permits participation masks DGB (1), DOGE (2), or both (3).
A zero mask is invalid. Other historical auxiliary-chain support in the code is
not enabled by this mainnet profile. Do not add unrelated native coins to a
FORGE proof without protocol support and separate qualification.

| Native candidate | Exact treasury output script (hex) |
| --- | --- |
| DigiByte | `0014706267a5255e3666936bcb9a777667599f387546` |
| Dogecoin | `76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac` |

The corresponding script hashes are in the shared RGN consensus configuration.
The config generator copies the exact scripts automatically. The historical
`ltcPayoutScriptHash` field name in that configuration carries the DGB parent
script commitment in this profile; do not rename it.

A treasury candidate claims the full native subsidy plus fees for its included
transactions. Routing only a percentage, using a pool's normal native payout
address, or leaving fees outside the approved commitment is not this profile.
The signed V4 path supplies native validation assurance; ordinary RGN nodes do
not need native daemons to verify their RGN proof and certificate.

There is no requirement to find a native block for each RGN block. A share can
reach the RGN target without reaching DGB's or DOGE's target. When a native
target is reached, submit the corresponding full native payload through that
chain's backend. A local submission response is not proof of final native
treasury income: track canonical acceptance, confirmations and reorgs.

## Work, Stratum and submission lifecycle

Use `proxy/forge_runtime.go`, `proxy/forge_multi_runtime.go` and related files as the
executable specification. `forgepool/multi_protocol.go`, `certificate.go`,
`multi_aux.go`, `scrypt.go`, `bitcoin.go` and Dogecoin helpers implement the
binary commitments. Test names under those directories cover boundary cases.

Keep a record per issued job of work hash, exact certificate, native bodies,
participation, targets, validity, extranonce layout and issued share difficulty.
An old share must be evaluated using its issued job's difficulty, not the
session's newly retargeted difficulty. The reference pool also caps difficulty
for coverage of eligible block targets; do not hide valid blocks behind a
share threshold larger than the relevant network target.

RGN initial network difficulty 21 and the sample share difficulty 50,000 are
different values for different purposes. Do not copy the share setting into
node consensus. Preserve Scrypt target and hash byte-order conventions.

On expiry, new native head, RGN head change or loss of eligible work, rebuild
and refresh jobs. Fewer than two available authorized approvals prevents new
V4 certificates. Already-issued jobs remain usable only within their full
validity and chain conditions; do not extend expiry to conceal an outage.

Persist uncertain submissions with the original proof bytes and certificate.
On timeout or restart reconcile the exact candidate/hash with canonical state.
Do not generate a different certificate for a pending submission or credit a
candidate twice. Native submission and RGN submission have separate outcomes.

## Miner rewards, fees and payouts

Call `eth_getBlockReward` for the canonical block and use the miner allocation,
not the gross subsidy. Check beneficiary and canonical block hash. Special
treasury credits, staking and P2E are not pool revenue. The RPC's normal reward
fields are not a shortcut for transaction-fee calculation; the reference
unlocker retains separate fee handling.

Preserve integer arithmetic and accounting units. The pool's balance threshold
is in Shannon (10^9 wei): `threshold=1000000000` represents one RGN. Its API/ledger
units are not interchangeable with native DGB/DOGE base units.

Share settlement, maturity, fees, minimum payout and payout cadence are operator
policy. The reference example uses proportional round shares, 0% fee, maturity
32 / immature 16, minimum one RGN and a one-hour payout interval. Do not present
those settings as requirements for a third-party pool's business model.

RGN pays an EVM address. A native coin wallet's `sendtoaddress` cannot pay RGN.
Use an isolated signer or your engine's reviewed chain-70088 transaction path.
Never send us a pool private key or ask approved template signers to hold it.

## Qualification before listing

Use isolated fixtures and the test suite first. Qualify your actual engine with:

- Exact chain/configuration and beneficiary checks; no pre-fork work issuance.
- DGB/DOGE candidate construction, full treasury claim and template rejection cases.
- Two distinct approvals, malformed/duplicate/wrong signer rejection and expiry.
- Certificate/extranonce mutation rejection and correct proof byte ordering.
- RGN-only target solutions versus native-target solutions; no-native rejection.
- Share retargeting, duplicate/stale submissions and durable accounting recovery.
- Native or signer outages, RGN head changes and uncertain submission reconciliation.
- Canonical RGN miner allocation, maturity/reorg handling, small payout and restart recovery.

Live post-fork qualification must demonstrate actual canonical RGN block
acceptance and correct rewards. Do not manufacture production work, rent
hashpower or move operator funds merely to run a documentation check.

## Operator handoff information

Provide this repository, the pinned node release, public signer profile,
treasury rules and test instructions. Ask the operator for its engine/version,
RGN beneficiary and preferred integration approach. Public addresses are enough
for coordination; credentials and wallet keys stay with the operator.

No code here silently changes an existing pool's DGB/DOGE jobs, payouts or
configuration. Integration into that engine is an explicit operator deployment.
