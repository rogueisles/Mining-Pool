# FORGE V5 release qualification — 4.1.0

Release binary/profile qualification completed 25 October 2026. The initial
coordinated activation is scheduled for block **1,121,800**. These results qualify
the supplied artifacts within the stated scope; publication does not itself
upgrade a running validator or prove that outside operators have updated.

## Artifact identity

Node Linux AMD64 SHA256:

```text
338ada631c9c01aa5343503852de94faf7d43ff13840e3a315c3cbd5e4dc5783
```

Approved scheduled profile SHA256:

```text
87c66819311b112adce09855f9c2c38490fb32a193ea35ca7c466892319aefd8
```

The packaged image and complete-source Docker build contain that exact node
binary. All 2,036 node and 185 pool implementation files independently match the
qualified candidate after line-ending normalization. Production pool/signer/admin
binaries remain byte-identical to their previously qualified staging binaries.
The bundled mainnet profile preserves genesis, history, signer quorum, initial
DGB/DOGE treasury commitments and rewards per block.

Builds use Go 1.16.15, Ubuntu 22.04, GCC 11 and CGO. Exact node reproduction requires
canonical /node, output/release/egem and a fresh Go cache: Go 1.16 can retain absolute
CGO source paths from another checkout. The diagnosed cache difference was resolved
by a clean rebuild; no implementation or consensus change was made to fix it.

## Passed checks

| Check | Evidence and limit |
| --- | --- |
| Node regression | Eight node packages and targeted core historical/activation/transfer/reorg tests pass on the exact release source. |
| Pool regression | Six pool packages, including signer/admin tooling and storage, pass; storage tests use disposable Redis in an isolated private network namespace. Earlier eight-package race tests remain part of candidate qualification. |
| Final binary peers | A fresh peer on a second server synchronized from genesis; peers agreed on a mined transfer at block 36 and restarted/caught up through block 37. Actual work advertised V5 and 12,500 ms. |
| Final binary native acceptance | The final-node helper CPU solver mined private RGN work plus accepted DGB/DOGE/SOQ regtest blocks. This last repeat used the helper solver, not a separate Stratum client; staging destinations were the approved test registry, including prior test rotations. |
| Compiled pool and independent miner | Separate candidate qualification used the same released pool/signer binaries and a separate CPU Stratum client to mine two canonical private RGN blocks across restart, with exactly two durable credits and eight canonical native regtest blocks. |
| Native maturity | Earlier compiled-process tests verified all eight native coinbase outputs at their approved test treasuries with at least 300 confirmations. These are test coins. |
| Core subsets and recovery | All seven nonempty DGB/DOGE/SOQ subsets, optional-source failure, all-core pause/recovery, work-quorum expiry/recovery, fresh sync, restart and durable accounting passed staged tests. |
| Registry administration | Delayed future compatible coin registration/mining, slot reuse, DGB/DOGE/SOQ treasury rotations and old/new administrator possession transitions passed private-chain tests. |
| Native mainnet proposals | Real DGB/DOGE/SOQ identity/health and unsolved full-block proposal checks accepted correct bodies and rejected reward overclaims. The supplied SOQ address/script was checked independently. No solved mainnet block or transaction was submitted by that check. |
| Docker | Non-root UID10001, exact binary/profile pins, restricted HTTP modules, health check, graceful exit, invalid-setting refusal, foreign-genesis refusal and persistent identity across restart/image replacement all pass without mainnet networking. |
| Docker upgrade | A volume initialized by the unchanged V4-profile image successfully opened under the scheduled release, preserving identity and original genesis. This is an isolated height-zero compatibility test, not replay of mainnet history. |

## Performance and bounds

The earlier 10,000-header maximum-registry stress retained approximately 53 KB of
additional Go heap. Same-run full verification benchmarks were approximately
0.429ms for V4/two sources,0.482ms for V5/two,0.497ms for V5/three,0.500ms for six,
and1.675ms for maximum administrator rotation. Fixtures deliberately fail only
final PoW and do not themselves demonstrate successful mining.

The final small private-chain node repeat peaked around31–33MiB RSS locally and
30MiB on the fresh peer. These are small-lab measurements, not estimates for
existing mainnet databases/native full nodes. Cross-server timings include
controller SSH/RPC/poll overhead and are not raw wire propagation measurements.

Protocol limits are 16 registry slots, six native sources per job, five administrator
keys, one operation per block, fixed 32-leaf/depth-five witnesses and 16 KiB complete
proof data. A three-source registry envelope is 877 bytes; at an ideal 12.5 seconds,
that envelope alone is approximately 2.2 GB raw per year before database overhead.

## Unperformed checks and deployment responsibilities

A complete replay of all mainnet history/database on a copy has **not** been
performed. Synthetic configuration tests, private cross-peer mining and final
scheduled-profile startup are separate checks. Production rollout must use a
guarded canary, verify its stored head/state, then compare newly validated blocks
with unchanged peers before proceeding one validator at a time.

This report does not prove a mined mainnet SOQ reward, future income, independent
external security audit, readiness of outside validators/exchanges or universal
availability. Native rewards require actual canonical wins and maturity. A
treasury rotation does not transfer existing funds. No private production key was
needed for builds/profile generation. If all core sources, work quorum or effective
hashpower fail, mining can pause. Once V5 blocks exist, retain V5 validation.

Build-qualification and Docker-validation JSON accompany the release assets.
Original offline profile REVIEW files are dated generation evidence; current
release instructions explain the approved schedule and deployment duties.
