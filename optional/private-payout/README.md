# Private RGN payouts: optional reference source

This component is a **payment wallet**, not an approved FORGE template signer.
Pool operators use Rogue Network's public template approvals, but remain
responsible for signing their own miner-payment transactions.

The mining core in the repository root is unchanged. The optional transport
patch creates a separate payout-worker build so mining does not need an unlocked
wallet or a Unix wallet dependency. Do not point the unpatched root binary at
`unix://...`; its original RPC transport does not implement that scheme.

The reference wallet source is derived from the service used by the reference
pool. One public-source change removes that deployment's hardcoded beneficiary
and accepts a valid nonzero public address from its owner-controlled config.
The encrypted keystore must match the configured address. Existing fixtures and
a public-profile test accompany the source; other policy restrictions remain.

## What the service permits

- Unix socket only; no public TCP wallet listener.
- Chain/network 70088 and the exact RGN genesis.
- Native-coin transfers to nonzero external accounts, no contract execution/data.
- Fixed gas 21,000 and gas price 1,000,000,000 wei.
- One unresolved signed intent at a time, durably saved before broadcast.
- Restricted balance/receipt/peer reads and one fixed readiness challenge.
- Broadcasting controlled independently by `paymentsEnabled`.

It does not implement arbitrary signing, arbitrary transaction gas policies,
contract-wallet payouts or an automatic recovery command for every failure.
Pools needing those features must supply and qualify their own payment service.

## Build the separate worker

From the repository root, with the reference Go 1.16.15 environment configured:

```bash
python3 optional/private-payout/build_worker.py \
  --go "$(command -v go)" --output /absolute/new/rgn-payout-worker-build
```

This verifies the qualified core source, copies it to a new external directory,
applies only the Unix transport change, runs RPC/payout tests and builds
`rgn-payout-worker`. It does not start services, connect to your ledger or send
payments. The text-form `payer.patch` is also supplied for code review.

## Build the reference wallet service

Use a clean checkout of the **node** repository at the pinned commit:

```bash
git clone https://github.com/rogueisles/Rogue-Network.git /absolute/new/rgn-node-source
git -C /absolute/new/rgn-node-source checkout --detach 18367e0aea25899af7fe8d608f0a19fa420740a9
python3 optional/private-payout/build_signer.py \
  --go "$(command -v go)" \
  --node-source /absolute/new/rgn-node-source \
  --output /absolute/new/rgn-wallet-build
```

The builder copies only tracked source into a disposable GOPATH, uses the pinned
node's vendored crypto, runs race tests and builds the wallet binary. It does not
modify your node checkout. Preserve node/third-party licenses when distributing
this derived binary. Source files use `.go.txt` to avoid importing node-specific
dependencies into the root pool module; the builder restores `.go` names.

## Private configuration and credentials

The intentionally fixed service paths are:

```json
{
  "socket": "/run/rgn-pool-payments/signer.sock",
  "state": "/var/lib/rgn-pool-payout-signer/journal.json",
  "upstream": "http://127.0.0.1:18545",
  "address": "0xYOUR_PUBLIC_POOL_ADDRESS",
  "paymentsEnabled": false
}
```

This is a template; replace the address with a valid nonzero public address.
Run against your own dedicated local RGN node. If your node uses a different
port, arrange a private loopback endpoint at 18545 or review/change/test the
service's profile validation. Do not loosen it to accept arbitrary remote RPC.

The config must be a root-owned regular file, mode 0600 or 0644, without hardlinks
or symlinks. The service's state directory must be mode 0700, owned by the service
UID. Journals are mode 0600 and survive restart. Create a private runtime directory
for the socket, restrict its group to the intended payout worker and remove only
a confirmed stale socket after a clean stop; the service refuses an existing one.

Supply systemd `LoadCredential=wallet:/PRIVATE/PATH/ENCRYPTED_KEYSTORE` and
`LoadCredential=password:/PRIVATE/PATH/PASSWORD_FILE`. Files must be private
regular files with the ownership/modes checked in source. Systemd sets
`CREDENTIALS_DIRECTORY`; this service reads the credentials from there. Never
put a key, seed, password or credentials in Git, container images, command-line
arguments, public environment files or a website.

The setup intentionally does not import a production private key automatically.
Use a dedicated funded pool account and provision its encrypted keystore locally.
No miner or Rogue Network operator needs to receive that key.

Start with `rgn-private-payout --check /PRIVATE/PATH/wallet-config.json`, using
the same isolated credentials arrangement as the service. It verifies loading
and chain identity without sending a transaction. `--serve` starts the Unix service.
These are service-owner operations, not commands to paste with placeholders.

## Hourly policy and controlled enablement

The generated `payouts-disabled.json` already selects the Unix socket and includes
`interval="1h"`, threshold 1,000,000,000 Shannon (= one RGN), gas 21,000 and gas
price 1 Gwei. Enable payments only after all of these checks:

1. Node is canonical/synchronized with peers; activation and intended mining policy
   are satisfied. The wallet service itself has no fork-height payment gate.
2. Redis ledger and accounting journals are persistent and correctly reconciled.
3. Pool wallet has native RGN for payment plus gas; never count scheduled treasury
   issuance or another contract's balance as spendable pool funds.
4. Controlled, explicitly authorized small payment passes canonical receipt checks
   and restart recovery. Tests in this repo use fake backends, not live funds.
5. No unresolved wallet intent or pending/locked ledger payment remains.

Then enable `paymentsEnabled` in the private wallet config and `payouts.enabled`
in a separate worker config, keeping all other roles disabled in that worker.
Run exactly one Unix-capable payout worker for this ledger. Use your own supervised
service and activation/health gating. The repository does not silently turn on
payments because a public node's height changes.

The worker processes eligible payments immediately on startup, then at its
configured interval. `1h` does not mean an exact wall-clock schedule or payment
of balances below the minimum. Review the root README and operations guide.

## Recovery

An uncertain send keeps the signed transaction and nonce in the durable journal.
Do not delete that file, sign another payment for the same debt, clear the Redis
lock or assume a timeout means no payment happened. Reconcile the saved hash,
canonical receipt, transaction nonce and ledger state. The service refuses a new
send while the previous intent is unresolved. Rebroadcasting, if needed, must
use the same signed bytes after reviewing chain state.

The source tests cover durable-before-send behavior, ambiguous outcomes,
restart/pending refusal, canonical receipt settlement, rejected arbitrary signing,
wrong-chain identity and private file handling. Local test evidence is scoped to
the exact sources listed; it is not proof of a third-party wallet deployment.
