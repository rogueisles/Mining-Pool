#!/usr/bin/env python3
"""Owned six-node gate; no existing datadirs, keys, configs or RPC URLs accepted.

Usage: test-forge-integrated.py GO REDIS_SERVER EGEM LTC_BIN_DIR DOGE_BIN_DIR NEW_REPORT
All paths must be absolute. OGG uses the unchanged canonical difficulty-one
target. External chains use their official regtest rules. This gate deliberately
mines one ordinary OGG share that also wins LTC and DOGE regtest blocks.
It does not claim genuine OGG block acceptance or production readiness.
"""
import base64
import hashlib
import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time
import urllib.request

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location(
    "readiness", pathlib.Path(__file__).with_name("test-forge-ogg-readiness.py"))
readiness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(readiness)


class ExternalNode:
    def __init__(self, bindir, coin, root, report, name):
        self.bindir, self.coin = bindir, coin
        self.root, self.report, self.name = root / name, report, name
        self.rpc_port, self.p2p_port = readiness.port(), readiness.port()
        self.process, self.log = None, None
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    @property
    def url(self):
        return f"http://127.0.0.1:{self.rpc_port}"

    def start(self):
        self.root.mkdir()
        base = ("regtest=1\nserver=1\nlisten=1\ndnsseed=0\ndiscover=0\nupnp=0\n"
                "rpcuser=forge-regtest\nrpcpassword=disposable-test-only\n")
        section = "[regtest]\n" if self.coin == "litecoin" else ""
        config = base + section + (f"bind=127.0.0.1\nport={self.p2p_port}\nconnect=0\n"
                                  f"rpcbind=127.0.0.1\nrpcallowip=127.0.0.1\nrpcport={self.rpc_port}\n")
        (self.root / (self.coin + ".conf")).write_text(config)
        self.log = (self.report / (self.name + ".log")).open("wb")
        self.process = subprocess.Popen(
            [str(self.bindir / (self.coin + "d")), "-datadir="+str(self.root), "-printtoconsole"],
            stdout=self.log, stderr=subprocess.STDOUT)
        readiness.eventually(lambda: self.rpc("getblockchaininfo")["chain"] == "regtest",
                             self.name + " regtest RPC")
        assert self.rpc("getblockcount") == 0

    def rpc(self, method, params=None):
        body = json.dumps({"id": 1, "method": method, "params": params or []}).encode()
        auth = base64.b64encode(b"forge-regtest:disposable-test-only").decode()
        req = urllib.request.Request(self.url, body, {"Content-Type": "application/json",
                                                     "Authorization": "Basic "+auth})
        with self.http.open(req, timeout=5) as res:
            data = json.load(res)
        if data.get("error"):
            raise RuntimeError(f"{self.name}/{method}: {data['error']}")
        return data["result"]

    def stop(self):
        readiness.Node.stop(self)


def main():
    if not __debug__:
        raise SystemExit("run without Python optimization; checks are mandatory")
    if len(sys.argv) != 7:
        raise SystemExit(__doc__)
    go, redis, ogg, ltc, doge, report = map(pathlib.Path, sys.argv[1:])
    if not all(p.is_absolute() for p in (go, redis, ogg, ltc, doge, report)):
        raise SystemExit("absolute paths required")
    binaries = [go, redis, ogg, ltc / "litecoind", doge / "dogecoind"]
    if not all(p.is_file() and os.access(p, os.X_OK) for p in binaries):
        raise SystemExit("executable binaries required")
    version = subprocess.check_output([str(go), "version"], text=True).strip()
    if not version.startswith("go version go1.16.15 "):
        raise SystemExit("Go 1.16.15 required")
    report.mkdir()
    pool = pathlib.Path(__file__).resolve().parent.parent
    env = os.environ.copy()
    env["GOFLAGS"] = ""
    results = {"status": "FAIL", "scope": "real full-pool LTC/DOGE wins with real OGG issued work",
               "oggMinedAcceptance": "OPEN", "goVersion": version,
               "binarySHA256": {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in binaries}}
    with (report / "build.log").open("wb") as log:
        subprocess.run([str(go), "build", "-race", "-o", str(report / "pool-race"), "."],
                       cwd=pool, env=env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=180)
    results["poolBinarySHA256"] = hashlib.sha256((report / "pool-race").read_bytes()).hexdigest()
    genesis = report / "DEV-genesis.json"
    genesis.write_text(json.dumps(readiness.dev_genesis(), indent=2)+"\n")
    with tempfile.TemporaryDirectory(prefix="forge-integrated-") as tmp:
        root = pathlib.Path(tmp)
        nodes = []
        try:
            pairs = {}
            for coin, bindir in (("litecoin", ltc), ("dogecoin", doge)):
                a, b = [ExternalNode(bindir, coin, root, report, coin+suffix) for suffix in ("-pool", "-signer")]
                nodes.extend([a, b])
                a.start()
                b.start()
                a.rpc("addnode", [f"127.0.0.1:{b.p2p_port}", "onetry"])
                readiness.eventually(lambda: a.rpc("getconnectioncount") > 0 and b.rpc("getconnectioncount") > 0,
                                     coin+" peer handshake")
                if coin == "litecoin":
                    a.rpc("createwallet", ["DEV-phase6d"])
                    address = a.rpc("getnewaddress", ["", "bech32"])
                    a.rpc("generatetoaddress", [110, address])
                else:
                    a.rpc("generate", [40])
                readiness.eventually(lambda: a.rpc("getbestblockhash") == b.rpc("getbestblockhash"),
                                     coin+" signer synchronization")
                # Fast regtest generation can put MTP/template time ahead of
                # wall time. Wait for real time, never relax signer freshness.
                rules = ["segwit", "mweb"] if coin == "litecoin" else []
                readiness.eventually(lambda: a.rpc("getblocktemplate", [{"rules": rules}])["curtime"] <= int(time.time()),
                                     coin+" template no longer in the future", 60)
                pairs[coin] = a, b
            a, b = [readiness.Node(str(ogg), root, report, name) for name in ("ogg-pool", "ogg-peer")]
            nodes.extend([a, b])
            for n in (a, b):
                n.initialize(genesis)
                n.start()
            enode = b.rpc("admin_nodeInfo")["enode"].split("@", 1)[0]+f"@127.0.0.1:{b.p2p_port}"
            assert a.rpc("admin_addPeer", [enode]) is True
            readiness.eventually(lambda: int(a.rpc("net_peerCount"), 16) > 0 and int(b.rpc("net_peerCount"), 16) > 0,
                                 "OGG mutual peer handshake")
            readiness.eventually(lambda: a.rpc("eth_createAuxBlock"), "OGG issued work")
            env.update(FORGE_PHASE6_POOL_BINARY=str(report / "pool-race"),
                       FORGE_PHASE6_REDIS_SERVER=str(redis), FORGE_INTEGRATED_REPORT=str(report),
                       FORGE_INTEGRATED_OGG_RPC=f"http://127.0.0.1:{a.rpc_port}",
                       FORGE_LTC_TEST_RPC=pairs["litecoin"][0].url,
                       FORGE_LTC_SIGNER_RPC=pairs["litecoin"][1].url,
                       FORGE_DOGE_TEST_RPC=pairs["dogecoin"][0].url,
                       FORGE_DOGE_SIGNER_RPC=pairs["dogecoin"][1].url)
            print("Six owned nodes ready; running full pool and independent DEV signer", flush=True)
            with (report / "integration.log").open("wb") as log:
                subprocess.run([str(go), "test", "-vet=off", "-count=1", "-timeout", "6m", "-v",
                                "-run", "^TestForgeIntegratedRealChains$", "./proxy"], cwd=pool,
                               env=env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=380)
            assert "--- PASS: TestForgeIntegratedRealChains" in (report / "integration.log").read_text()
            external = {}
            accounting = json.loads((report / "accounting.json").read_text())
            for coin, pair in pairs.items():
                x, y = pair
                readiness.eventually(lambda: x.rpc("getbestblockhash") == y.rpc("getbestblockhash"),
                                     coin+" mined-block propagation")
                expected = 111 if coin == "litecoin" else 41
                assert x.rpc("getblockcount") == y.rpc("getblockcount") == expected
                short = "ltc" if coin == "litecoin" else "doge"
                assert accounting["external"][short]["blockHash"].removeprefix("0x").lower() == x.rpc("getbestblockhash").lower()
                external[coin] = {"height": expected, "hash": x.rpc("getbestblockhash"), "peerAgrees": True}
            assert a.rpc("eth_blockNumber") == b.rpc("eth_blockNumber") == "0x0"
            assert a.rpc("eth_getBlockByNumber", ["latest", False])["stateRoot"] == b.rpc("eth_getBlockByNumber", ["latest", False])["stateRoot"]
            assert "invalid FORGE Scrypt proof-of-work" in (report / "ogg-pool-1.log").read_text()
            results.update(status="PASS_INTEGRATION_WITHOUT_OGG_BLOCK", external=external,
                           acceptedPostForgeBlocks=0, oggTargetRejection=True,
                           accounting=accounting)
            print("PASS: full-pool TCP share, independent signer, LTC and DOGE blocks accepted and propagated, exactly one credit", flush=True)
            print("PASS: real OGG proof reached unchanged Scrypt target check and was rejected; OGG mining remains OPEN", flush=True)
        except BaseException as exc:
            results["error"] = str(exc)
            raise
        finally:
            for n in reversed(nodes):
                n.stop()
            (report / "results.json").write_text(json.dumps(results, indent=2)+"\n")


if __name__ == "__main__":
    main()
