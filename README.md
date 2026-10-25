# Rogue Network Mining Pool

Scrypt pool, work signer and offline registry tools for RGN FORGE merged mining
with DGB, DOGE and optional SOQ.

**FORGE V5 activates at block 1,121,800 — estimated 27 October 2026, 13:35 UTC.**

All RGN nodes, including exchange nodes, must upgrade before block **1,121,800**.

## Install the Linux tools

```sh
curl -fLO https://github.com/rogueisles/Mining-Pool/releases/download/v4.1.0/rgn-pool-4.1.0-linux-amd64.tar.gz
curl -fLO https://github.com/rogueisles/Mining-Pool/releases/download/v4.1.0/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf rgn-pool-4.1.0-linux-amd64.tar.gz
cd rgn-pool-4.1.0-linux-amd64
sha256sum -c PACKAGE-SHA256SUMS
sudo install -m 755 rgn-pool rgn-forge-signer forge-registry-admin /usr/local/bin/
```

Use Ubuntu 22.04 or compatible. Configure an RGN node, native RPC backends,
Redis and the authorized work-signing endpoints using the
[operator guide](docs/FORGE-V5-OPERATORS.md). Keep runtime credentials in private
configuration files. Run the signer and pool as separate services:

```sh
rgn-forge-signer -check /etc/rgn-template-signer/signer.json
rgn-forge-signer /etc/rgn-template-signer/signer.json
```

```sh
rgn-pool /etc/rgn-pool/mining.json
```

## Upgrade an existing pool

Download, verify and extract the archive before installing binaries. Update the existing runtime
configuration for V5, retaining the Redis namespace, accounting journal,
beneficiary and payment settings. Use your installed service names and binary
paths; this example uses `/usr/local/bin`:

```sh
sudo systemctl stop rgn-pool.service
sudo install -m 755 rgn-pool /usr/local/bin/rgn-pool
sudo systemctl start rgn-pool.service
```

Roll work signers one at a time while keeping the signing quorum available:

```sh
sudo systemctl stop rgn-template-signer.service
sudo install -m 755 rgn-forge-signer /usr/local/bin/rgn-forge-signer
sudo systemctl start rgn-template-signer.service
```

Update the work gate and readiness checks with the services. Confirm accepted
shares and normal accounting after each change. Full configuration fields and
service integration are documented in the [operator guide](docs/FORGE-V5-OPERATORS.md).

## Build from source

Use Go 1.16.15, GCC 11 and CGO on Ubuntu 22.04.

```sh
git clone --branch v4.1.0 https://github.com/rogueisles/Mining-Pool.git
cd Mining-Pool
export GO111MODULE=on CGO_ENABLED=1
go build -mod=readonly -trimpath -o rgn-pool .
go build -mod=readonly -trimpath -o rgn-forge-signer ./cmd/forge-live-signer
go build -mod=readonly -trimpath -o forge-registry-admin ./cmd/forge-registry-admin
```

## Changes in 4.1.0

- Optional SOQ merged-mining integration alongside DGB and DOGE.
- Registry support for compatible future coins and treasury rotation.
- Automatic V4-to-V5 job selection at block 1,121,800.

The RGN target becomes 12.5 seconds at activation; rewards per block remain
unchanged. Each job requires an eligible core source. Native rewards depend on
winning that chain's target, remaining canonical and reaching maturity.

## Reference pool

```text
Algorithm: Scrypt
URL:       stratum+tcp://stratum.rogueisles.com:3333
Worker:    YOUR_PUBLIC_RGN_ADDRESS.worker1
Password:  x
```

- [Operator guide](docs/FORGE-V5-OPERATORS.md)
- [Node source](https://github.com/rogueisles/Rogue-Network)
- [Node downloads](https://github.com/rogueisles/Rogue-Node/releases/tag/v4.1.0)
- [Pool website](https://pool.rogueisles.com/)

License and upstream attribution are retained in `LICENSE` and `NOTICE`.
