> Production schedule: block **1,121,800**. See the approved profile and current operator guide.

# Flexible FORGE V5 protocol

Version 4.1.0 supplies the approved profile scheduling V5 at block 1,121,800.
V4 remains in force below that height. Source publication and profile preparation
do not install that schedule on running services. Read the release qualification
report for the exact tested artifacts and the separate deployment reports for
actual validator, exchange and pool readiness.

Read [the complete V5 operator guide](FORGE-V5-OPERATORS.md) for exact settings,
offline administrator commands, production readiness gates and test scope.
V5 introduces the separately height-gated **12500 ms (12.5-second) target**;
historical 12-second validation and per-block RGN economics remain unchanged.

## Changes

V5 preserves RGN issuance and the working DGB/DOGE flow, adds SOQ as a third
established source, and permits compatible additional Bitcoin-family Scrypt
AuxPoW coins through an authenticated bounded registry. At least one registered
core source (DGB, DOGE, SOQ) is still required; a bonus coin alone cannot carry
RGN issuance. Every included source must pay its approved treasury destination.

Historical V3/V4 proof bytes are unchanged. V5 is a distinct proof envelope and
mined certificate domain. Its signing digest binds the complete registry
envelope, including administrator approvals, to the exact RGN work leaf. The
auxiliary tree continues to contain the actual RGN work hash.

## Separate administrative authority

The pool and mining signer never hold registry administrator private keys.
Administrator approvals are prepared offline and supplied to the pool as a
public operation file. Administrators and mining signers must be disjoint.

The registry holds **16 simultaneous entries: three immutable core identities
and 13 additional coins**. An additional coin can be disabled by an approved,
future-dated operation. Once that disable becomes effective, administrators can
register a compatible replacement in its slot, with fresh approval and the full
activation notice. The replacement must satisfy the same identity, profile and
auxiliary-tree collision checks. Historical registry roots and block validation
remain unchanged. Operators should omit a temporarily broken optional coin
locally while repairing it; permanent registry changes are unnecessary for an
ordinary RPC or pool failure.

SOQ is a permanent core identity and cannot be retired through the registry.
Its pool integration can be disabled locally, repaired and enabled again when
it produces work matching the installed rules.

A registry change is authorized by the current administrator quorum, bound to
RGN chain identity, registry ancestry, epoch and sequence, and takes effect only
after the protocol's minimum notice. Administrator-key rotation also needs a
separate acceptance signature from the proposed new administrator quorum.

The public operation file can remain in place while mining proceeds; its result
root is recalculated for the actual inclusion height. An already applied or
stale operation is omitted. A corrupt/unapproved operation never authorizes a
change: work falls back to the existing committed state.

## Runtime configuration

The pool requires explicit `externalChainPolicy: "registry-v5"`, `multiChain:
true`, and a `registryBlock` matching its own RGN node. Before that height it
continues its existing DGB/DOGE path and does not request optional registered
work. Never populate an activation height in mainnet configuration until the
coordinated release is approved.

`registryOperationFile` is an optional absolute path to a public administrator
operation JSON document. The runtime reads at most 64 KiB. It cannot contain
private keys and does not cause an RPC transaction by itself; an authorized
operation is carried in a mined V5 block.

`treasuryDestinations` accepts at most 32 public `{ "script": "HEX", "address":
"NATIVE_ADDRESS" }` entries, so operators can provision the next public treasury
before its activation. The effective registered SHA-256 script hash determines
which entry is selected. SOQ additionally requires the native node to validate
its witness-v1 address and exact script.

Native endpoint identity and genesis must match the registered coin. The
pool/signer retain authenticated loopback RPC, native synchronization and peer
checks, independent tip checks, proposal acceptance and a one-base-unit
coinbase overclaim rejection. A local public address setting cannot override
the registry.

A V5 mining signer additionally needs:

- `registryBlock`: the coordinated activation height.
- `registryChainId`: the exact RGN network chain ID.
- `registryRpc`: an authenticated loopback RGN endpoint and expected RGN genesis.
- `multi`: only the bounded native endpoints it actually validates.
- Optional `treasuryDestinations` with current/next public script/address pairs.

The signer independently obtains registry context for the RGN header's exact
parent. It validates administrator operations and membership, rebuilds the
native candidates and complete parent body, and checks full native reward claims
before signing. Pool-supplied context alone is insufficient.

## Availability and accounting

Healthy sources can be selected independently. If an optional SOQ/bonus source
cannot obtain signing quorum, the pool retries core-only work, preferring its
existing DGB/DOGE path when available. If an administrator operation fails,
existing-state work is retried. If every core source is unavailable, work pauses
rather than bypassing validation.

Additional native wins have bounded registered submission records separate from
RGN share credit and payment rounds. Lost replies remain in the durable journal;
recovery checks canonical native acceptance before updating observations and
does not duplicate miner credit. Canonically observed submissions avoid another
send; uncertain submissions can retry the same immutable payload within their
validity window. Noncanonical observations are rechecked, not resubmitted.

Treasury rotation affects future mining outputs. It does not transfer existing
wallet balances or change the validity of historical blocks.

## Resource limits

- At most 16 simultaneous registry entries: three immutable core identities
  and 13 additional coins. A disabled additional slot can be replaced after its
  disable becomes effective, with fresh approval and activation notice.
- At most six native sources in a proof and five auxiliary headers plus the DGB
  parent, or six auxiliary headers when the parent is synthetic.
- At most five administrator keys and one administrator operation per block.
- Registry membership depth five and auxiliary tree size at most 32.
- The existing 16 KiB total proof limit is retained.
- Registry RPC replies and public operation JSON are limited to 64 KiB.
- Mining signer requests remain bounded by the existing 16 MiB transaction
  evidence limit; actual RGN proofs retain the much smaller 16 KiB limit.

Nodes verify proofs and committed registry records. They do not run or contact
all native blockchains to validate historical RGN blocks. Concrete verification,
propagation, memory and synchronization measurements belong in the qualification
report; these bounds do not substitute for those measurements.

## Testing

`forgepool`, `rpc`, `proxy`, `storage`, and `cmd/forge-live-signer` contain V5 and
legacy regression tests. Registry primitive fixtures are mirrored from the node
module to verify wire and policy parity. Test-only private regtest adapters are
kept in `*_test.go` and never compiled into shipping binaries.

`TestRegistryV5RealPoolSignerStratumAndRecovery` is opt-in. It requires an owned
private-CPU RGN network (chain 7008902), native regtest daemons, test-only signer
keys and independently built pool/signer executables. It refuses mainnet and
checks real native genesis and peers. Its transport bridge changes only the
chain-name health label and DOGE's out-of-range historical identity checkpoint;
mainnet identity guards have separate regression tests. Native templates,
full-block proposals, mining, submissions and canonical receipts remain real.

The staging report must distinguish these private-network acceptance tests from
native mainnet wins and must not claim a mainnet SOQ reward from a regtest result.
