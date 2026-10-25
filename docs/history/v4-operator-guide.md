## Current V4 mainnet reference — legacy operator guide

The remainder preserves the existing DGB/DOGE setup and historical V4 integration
guide. Its generator/gate do not configure V5. Do not use it to schedule V5 or
replace live services during candidate testing.

Open-source mining backend and integration guide for **Rogue Network (RGN)**. This
source is based on the implementation used at [pool.rogueisles.com](https://pool.rogueisles.com).
It is provided so an independent pool can run the reference implementation or port
the FORGE V4 logic into its own pool engine.

**Original FORGE activation: block 1,100,100.** Historical mining rules and rewards apply
through block 1,100,099. The upgrade activates by height, not by calendar time.
The future V5 height is separate and remains unscheduled.

## What an operator needs

| Component | Who provides it? |
| --- | --- |
| Updated RGN mainnet node and shared consensus configuration | Run your own; official binary/Docker image is available |
| Compatible FORGE V4 mining software | Use this reference pool or integrate its code into your engine |
| DGB Scrypt / DOGE native validation and template access | Your own private native RPC backends; both are configured in this reference setup |
| Approved template signers | Rogue Network operates the four public services; the client requests approvals automatically |
| Pool beneficiary, miner accounting and RGN payout signing | Your own public wallet address, ledger and private payment arrangement |

You do **not** need approved signer keys, an approved signer server, access to our
native RPC credentials, or a manual signature from the Rogue Network team.
Ordinary RGN node operators and exchanges need neither native daemons nor signer
services. Those additional components belong to mining operators.

Installing the RGN binary alone does **not** add FORGE to an unchanged Miningcore,
NOMP or custom pool. The job builder, certificate binding, proof submission and
RGN reward accounting must understand FORGE V4. This repository supplies that
reference logic; it is not a universal config-only plugin for every pool engine.

## How mining and treasury payments work

1. Your dedicated RGN node supplies an RGN work hash and target for the next block.
2. The pool constructs eligible native candidates and the combined Scrypt job.
3. Its client automatically requests approvals from the public signer services.
   Two distinct authorized signatures are required. The exact V4 certificate is
   bound into the parent coinbase **before** Stratum work is issued.
4. Miners submit ordinary Scrypt work through the compatible Stratum service.
5. The pool checks shares and submits an RGN proof when the RGN target is met.
   Native-target solutions are submitted to the relevant native backend as well.
6. RGN consensus validates the proof and certificate. Native chains separately
   determine whether a submitted native block is accepted and earns a reward.

**No DGB and no DOGE participation means no valid FORGE RGN block.** Participation
means an eligible native candidate represented in the signed proof; merely
running an idle daemon is insufficient. A miner does **not** have to find an
actual DGB/DOGE block for each RGN block. RGN has its own target, with a 12-second
target interval; actual discovery intervals vary.

The protocol permits DGB, DOGE, or both. This release's setup recipe configures
both, matching the reference deployment. The backend requires both endpoint
fields at initialization; it can build eligible work from available participation.
A one-backend-only installation needs explicit operator qualification; do not
delete endpoint fields and assume the resulting configuration is supported.

### Treasury routing is supplied automatically

The bundled profile and config generator supply the approved native payout
scripts and public signer list. An operator does **not** type treasury addresses
or decide their allocation.

| Reward | Destination / handling |
| --- | --- |
| Normal RGN subsidy, miner allocation | Pool beneficiary for the RGN block it found; distributed using the pool's miner ledger |
| Other RGN subsidy allocations and scheduled treasury credits | Applied by RGN consensus; not pool miner income |
| Accepted participating DGB/DOGE block, full subsidy plus included transaction fees | Approved native treasury script in that candidate, subject to native maturity and reorg rules |

Normal RGN subsidy allocation is 40% miners, 30% existing staking, 20% P2E and 10%
treasury. The normal subsidy changes over time. Read the canonical miner reward
from `eth_getBlockReward` rather than assigning the whole gross subsidy to miners.
Transaction fees retain the node's inherited handling; the unlocker accounts for
fees separately. Special treasury issuance is never miner income.

RGN treasury allocation is performed by RGN consensus. Native treasury routing
requires the native candidate's correct outputs and the approved signing path;
RGN consensus does not send a separate DGB/DOGE wallet transfer afterward.

**Existing native pool operators:** RGN-eligible candidates commit their native
rewards to treasury. Do not assume you can retain the same DGB/DOGE rewards for
your existing users and also claim RGN on those unchanged jobs. Preserve existing
jobs and accounting while you qualify a dedicated RGN integration. Advertising
DOGE or DGB support does not establish FORGE compatibility.

## What is included

| Path | Contents |
| --- | --- |
| `forgepool/`, `proxy/`, `rpc/` | Job construction, Scrypt targets, Stratum, public signer client and proof submission |
| `storage/`, `payouts/`, `api/` | Durable accounting, reward unlocker, payout processor and statistics API |
| `forgesigner/`, `cmd/forge-live-signer/` | Approval service implementation and tests, for protocol review; operators use the existing approved services |
| `network/` | Exact mainnet configuration and public signer/treasury profile |
| `operator/` | Private configuration generator, local work gate and regression tests |
| `optional/private-payout/` | Separate Unix-socket payout transport patch and private wallet-service source |
| `website/` | Optional reference website source; see its README for API/health requirements |
| `docs/INTEGRATION.md` | Guide for integrating into an existing/custom pool |
| `docs/OPERATIONS.md` | Launch checks, accounting, outages and recovery |
| `evidence/` | Source identity and scoped local qualification reports |

The historical qualified V4 reference had reproducible Linux build hash
`1af46493df5cd540d9a0adf532af207a477f415a05d10a099c7e59b02e1e8fdb`.
This is not a checksum for a V5 candidate binary; use the final V5 release manifest.
Operator helpers and the public wallet-address configuration are additions and
are identified separately in the publication manifest.

No live credentials, signing keys, wallet exports, production ledger, SSH tunnel
identities, private runtime files or server backups are included. Fixture keys
in tests are deliberately public test data and must never receive funds.

## Reference setup: prepare without starting mining

The following commands target Linux x86-64. Use a dedicated RGN node and Redis
ledger. Do not run another process on an existing node database or reuse a
testnet accounting database for mainnet.

### 1. Install and synchronize the official RGN node

Use [Rogue-Network v3.0.0](https://github.com/rogueisles/Rogue-Network/releases/tag/v3.0.0)
or `roguenetwork/rogue-network:3.0.0`. Its source commit is
`18367e0aea25899af7fe8d608f0a19fa420740a9`. Apply the supplied shared configuration,
not the repository's historical genesis file by itself.

Set the dedicated node's beneficiary to **your pool wallet**: native binary
`--etherbase 0xYourPublicRGNAddress`, or Docker `-e WALLET=0xYourPublicRGNAddress`.
This selects the recipient; it does not start a complete miner. No wallet private
key is needed to receive mined rewards.

Keep the node's IPC available to the local work gate. The native package normally
places it under the chosen datadir as `egem.ipc`; the Docker image uses
`/var/lib/rgn/egem.ipc` inside its persistent volume. Arrange access for the gate's
service user. Do not make the socket world-accessible or expose administrative
RPC modules on public HTTP. A sidecar needs access to the volume and compatible
UID/group permissions; the node Docker image alone does not contain this pool.

Confirm genesis, exact consensus config, peers, canonical height and synchronization.
`eth_syncing=false` at height zero does not prove a newly started node has caught up.

### 2. Prepare native backends and Redis

Use standard DigiByte and Dogecoin mainnet nodes with private RPC access. The
reference deployment was exercised with DigiByte **9.26.7** and Dogecoin **1.14.9**.
Another version needs proposal/template compatibility checks. This repository
does not change those chains or their native binaries.

The generator defaults to local DGB RPC port `33522`, DOGE port `32555`, Redis
`6379`. Change **your private generated endpoints** if your ports differ. Remote
backends need a protected private connection; do not expose native RPC publicly.

Reference pruning settings were DGB `prune=2300`, `dbcache=128`, `maxconnections=8`;
DOGE `prune=4096`, `txindex=0`, `dbcache=768`, `maxconnections=12`. Prune values
are native daemon settings, not total disk limits: chainstate, wallet and other
data require additional space. Retain operational disk headroom and monitor it.
Do not delete active databases or raise pruning assumptions to reclaim space.

Use a dedicated, private Redis instance with authentication and persistent storage.
Configure its persistence and backups before accepting shares. The application
does not provision Redis for you. Never flush an existing ledger to fix a fault.

### 3. Build and test the pool

The reference toolchain is **Go 1.16.15**, GCC/CGO and Redis 7.2.7 for tests. The
code preserves its inherited module path for compatibility. Building under a
new GitHub repository name does not require renaming imports.

```bash
export GO111MODULE=on CGO_ENABLED=1 GOMAXPROCS=2
export CGO_CFLAGS='-O2 -g0' CGO_CXXFLAGS='-O2 -g0'
go mod download
go mod verify
mkdir -p build
go build -mod=readonly -trimpath -ldflags=-buildid= -o build/rgn-pool .
sha256sum build/rgn-pool
python3 operator/test_operator.py
python3 scripts/test-rogue-source.py \
  --go "$(command -v go)" \
  --redis-server "$(command -v redis-server)" \
  --report-dir /tmp/rgn-pool-test-unique-directory
```

The test report directory must be new. The test runner starts its own disposable
Redis; it does not use your live ledger. Historical native-chain integration
scripts in `scripts/` have separate fixtures and assumptions. The command above
is the documented isolated regression suite.

### 4. Generate your private configuration

Use Python 3.9 or newer on the pool host. Replace the wallet and IPC path below
with your own. The output directory must be new and outside the source tree.

```bash
python3 operator/make_config.py \
  --wallet 0xYourPublicRGNAddress \
  --ipc /absolute/path/to/your/rgn-data/egem.ipc \
  --output /absolute/private/path/rgn-pool-config
```

Enter your own native RPC usernames, hidden passwords and Redis password locally.
The generator automatically supplies quorum 2-of-4, all public signer endpoints,
approved treasury scripts and activation height. It prints no passwords.
Generated files are private and must not be committed to GitHub.

It creates `mining.json`, `unlocker.json`, `api.json`, `payouts-disabled.json`,
`gate.json` and the durable `accounting/` directory. The gate starts **unarmed**;
Stratum binds to localhost; payouts are disabled. The unlocker uses `forgeBlock`
1,100,100, maturity 32 and immature depth 16. These are example pool policies;
they are not a change to consensus. The example pool fee is 0%.

### 5. Check and start the local gate

In the following examples `RGN_PRIVATE` is the directory you just generated.

```bash
export RGN_PRIVATE=/absolute/private/path/rgn-pool-config
python3 operator/work_gate.py --config "$RGN_PRIVATE/gate.json" --check
python3 operator/work_gate.py --config "$RGN_PRIVATE/gate.json" --serve
```

Run the service in its own terminal initially. `--check` uses read-only calls;
it never requests auxiliary work. The gate listens only on `127.0.0.1:38096`.
Do not put it behind a public reverse proxy.

**Do not probe readiness with `eth_createAuxBlock`.** It starts work creation in
the node. The gate refuses work issuance and proof submission before the first
eligible candidate height, even when armed. Through head 1,100,098 it waits;
at head 1,100,099 the next candidate may be the first FORGE block, 1,100,100.

### 6. Qualify the pool, then arm mining

Start the reference pool against the local gate in another terminal:

```bash
./build/rgn-pool "$RGN_PRIVATE/mining.json"
```

Before the gate is armed, work requests are refused. Keep public miners away
while qualifying the deployment. To arm after reviewing the identity and wallet:

```bash
python3 operator/work_gate.py --config "$RGN_PRIVATE/gate.json" --arm
```

Arming permits **only eligible fork-height work**; it does not bypass the height
gate or produce a pre-fork Scrypt block. The core automatically requests native
templates and public approvals as work becomes eligible. If no eligible native
candidate or quorum is available, it must pause work rather than invent approvals.

The RGN node, both native backends and the signers may see different mempools or
brief head changes. Treat stale candidates as refresh/retry events. Do not relax
treasury, full-claim, signature, expiry or work-binding checks to avoid a pause.

After isolated share, submission and recovery checks, set the generated
`proxy.stratum.listen` to the intended interface and restart only your pool
service. Publish your own Stratum hostname/port. Miners use:

```text
Algorithm: Scrypt
URL: stratum+tcp://YOUR_POOL_HOST:3333
Username: 0xMinerPublicRGNAddress.worker
Password: x
```

Serve mining TCP directly or through a compatible TCP service. An ordinary
Cloudflare HTTP proxy setting for a website does not proxy Stratum automatically.
Prove your actual ASIC/rental client works before advertising compatibility.

### 7. Accounting, statistics and payments

Use the same Redis instance/database/coin namespace across processes:

```bash
./build/rgn-pool "$RGN_PRIVATE/unlocker.json"
./build/rgn-pool "$RGN_PRIVATE/api.json"
```

Run each as its own supervised service after initial qualification. The API binds
to localhost `8080`; front it with your own HTTPS website/proxy if needed. Exactly
one unlocker and one payout worker should own a given ledger.

`payouts-disabled.json` deliberately does **not** start payments. Mining approval
signers do not sign your RGN payout transactions. You control a separate pool
wallet and payment path. See [the private payout guide](optional/private-payout/README.md)
for the Unix transport patch, wallet-service source, hourly interval, funding,
recovery and enablement checks. Never unlock a wallet on public RPC.

## Existing pool engines / Mining-Dutch

See [docs/INTEGRATION.md](docs/INTEGRATION.md). Use this implementation as the
reference for the job and proof lifecycle. An existing pool can reuse its own
frontend, ledger, payout policies and Stratum sessions, but must implement the
RGN-specific protocol and treasury commitments correctly.

This release does not claim an implemented Mining-Dutch, Miningcore or NOMP
plugin or a completed integration at any third-party pool. The operator's
software/version and job model must be confirmed before giving engine-specific
instructions. No pool registration or manual per-job approval step is part of
the reference client.

## Verification status and limits

The mining core has isolated race/regression tests, native proposal tests and a
two-build reproducibility record. The public-tree build is checked against the
qualified core hash. Helper tests cover the first eligible block, pre-fork work
refusal, unarmed state, wrong wallet/configuration and private configuration.
See `evidence/` for exact scopes and dates.

These records do not prove future mainnet activation, discovery of a DGB/DOGE
treasury block, economic returns, third-party ASIC compatibility, or an operator's
deployment. Confirm canonical post-fork block acceptance, reward allocation,
native routing, payouts and restart recovery on the actual deployment.

## Source and license

GPLv3 `LICENSE` and upstream attribution are retained. `NOTICE` describes source
origins. The historical upstream README is archived under `docs/`; its old
Ethereum/ProgPoW installation instructions are not this release's setup guide.
Legacy protocol domains and ledger strings are retained intentionally. A cosmetic
rename of those identifiers can break proof compatibility or accounting.

The optional private wallet service depends on the pinned RGN node's crypto
implementation; its dependency licenses and notices also apply. The SOQ protocol extension is consensus-affecting and requires a coordinated future
activation. Publishing or building this unscheduled candidate does not activate it.
