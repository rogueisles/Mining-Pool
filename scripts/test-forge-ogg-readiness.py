#!/usr/bin/env python3
"""Fresh loopback OGG nodes: P2P/RPC readiness, NOT mined-block acceptance.

Usage: python3 scripts/test-forge-ogg-readiness.py /absolute/egem /absolute/new-report
No existing datadir, wallet, RPC endpoint or genesis is accepted.
"""
import hashlib
import json
import pathlib
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def eventually(fn, description, seconds=30):
    until = time.monotonic() + seconds
    last = None
    while time.monotonic() < until:
        try:
            value = fn()
            if value:
                return value
        except (OSError, ValueError, RuntimeError) as exc:
            last = exc
        time.sleep(0.2)
    raise RuntimeError(f"timeout: {description}: {last}")


class Node:
    def __init__(self, binary, root, report, name):
        self.binary, self.root, self.report, self.name = binary, root / name, report, name
        self.rpc_port, self.p2p_port = port(), port()
        self.process, self.log, self.generation = None, None, 0
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def initialize(self, genesis):
        with (self.report / f"{self.name}-init.log").open("wb") as log:
            subprocess.run([self.binary, "--datadir", str(self.root), "init", str(genesis)],
                           stdout=log, stderr=subprocess.STDOUT, check=True, timeout=30)

    def start(self):
        self.generation += 1
        self.log = (self.report / f"{self.name}-{self.generation}.log").open("wb")
        args = [self.binary, "--datadir", str(self.root), "--networkid", "7008901",
                "--nodiscover", "--nat", "none", "--netrestrict", "127.0.0.0/8",
                "--port", str(self.p2p_port), "--maxpeers", "4", "--ipcdisable",
                "--rpc", "--rpcaddr", "127.0.0.1", "--rpcport", str(self.rpc_port),
                "--rpcapi", "eth,net,web3,admin", "--syncmode", "full", "--cache", "64",
                "--minerthreads", "0", "--etherbase", DEV_ADDRESS]
        self.process = subprocess.Popen(args, stdout=self.log, stderr=subprocess.STDOUT)
        eventually(lambda: self.rpc("net_version") == "7008901", f"{self.name} RPC")

    def stop(self):
        if self.process is not None:
            self.process.terminate()
            try:
                self.process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
            self.process = None
        if self.log is not None:
            self.log.close()
            self.log = None

    def rpc(self, method, params=None, expect_error=False):
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or []}).encode()
        req = urllib.request.Request(f"http://127.0.0.1:{self.rpc_port}", body,
                                     {"Content-Type": "application/json"})
        with self.http.open(req, timeout=3) as res:
            data = json.load(res)
        if expect_error:
            if not data.get("error"):
                raise RuntimeError(f"{method} unexpectedly accepted")
            return data["error"]
        if data.get("error"):
            raise RuntimeError(f"{method}: {data['error']}")
        return data["result"]


# Public DEV-only address (private scalar 1); never used on an operational chain.
DEV_ADDRESS = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"


def dev_genesis():
    """Public, isolated fixture shared by the readiness and full-pool gates."""
    issued = 700_000_000 * 10**18
    return {
        "config": {"chainId": 7008901, "homesteadBlock": 0, "eip150Block": 0,
                   "eip155Block": 0, "eip158Block": 0, "byzantiumBlock": 0,
                   "constantinopleBlock": 0, "progpow": {"period": 10}, "forgeBlock": 1,
                   "forge": {"auxChainId": 0x4F47, "dogeAuxChainId": 0x62,
                             "targetBlockTime": 12, "preForgeGrossIssued": issued,
                             "initialDifficulty": 1, "minimumDifficulty": 1,
                             "templateSigners": [DEV_ADDRESS], "templateQuorum": 1,
                             "ltcPayoutScriptHash": "0x"+hashlib.sha256(bytes.fromhex("51")).hexdigest(),
                             "dogePayoutScriptHash": "0x"+hashlib.sha256(bytes.fromhex("52")).hexdigest(),
                             "templateMaxValidity": 300}},
        "nonce": "0x0000000000000000", "timestamp": hex(int(time.time())-60),
        "extraData": "0x"+b"FORGE isolated readiness".hex(), "gasLimit": "0x7a1200",
        "difficulty": "0x1", "alloc": {DEV_ADDRESS: {"balance": str(issued)}}}


def main():
    # Assertions below are acceptance gates, never optional checks.
    if not __debug__:
        raise SystemExit("run without Python optimization; acceptance checks are required")
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    binary, report = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    if not binary.is_absolute() or not report.is_absolute() or not binary.is_file():
        raise SystemExit("absolute binary and new report paths required")
    report.mkdir()  # Refuse an existing report.
    issued = 700_000_000 * 10**18
    genesis = dev_genesis()
    path = report / "DEV-genesis.json"
    path.write_text(json.dumps(genesis, indent=2)+"\n")
    results = {"scope": "P2P and issued-work readiness only; mined OGG acceptance OPEN",
               "binarySHA256": hashlib.sha256(binary.read_bytes()).hexdigest(),
               "genesisSHA256": hashlib.sha256(path.read_bytes()).hexdigest()}
    with tempfile.TemporaryDirectory(prefix="forge-ogg-readiness-") as temp:
        nodes = [Node(str(binary), pathlib.Path(temp), report, name) for name in ("a", "b")]
        a, b = nodes
        try:
            for n in nodes:
                n.initialize(path)
                n.start()
            genesis_a = a.rpc("eth_getBlockByNumber", ["0x0", False])
            genesis_b = b.rpc("eth_getBlockByNumber", ["0x0", False])
            assert genesis_a["hash"] == genesis_b["hash"]
            assert genesis_a["stateRoot"] == genesis_b["stateRoot"]
            for n in nodes:
                assert int(n.rpc("eth_getBalance", [DEV_ADDRESS, "latest"]), 16) == issued
            info = b.rpc("admin_nodeInfo")
            enode = info["enode"].split("@",1)[0]+f"@127.0.0.1:{b.p2p_port}"
            assert a.rpc("admin_addPeer", [enode]) is True
            eventually(lambda: int(a.rpc("net_peerCount"),16) >= 1 and int(b.rpc("net_peerCount"),16) >= 1,
                       "mutual peer handshake")
            work = eventually(lambda: a.rpc("eth_createAuxBlock"), "issued work")
            expected_target = "0x00000000ffff"+"0"*52
            assert work["target"] == expected_target
            assert int(work["height"],16) == 1 and int(work["difficulty"],16) == 1
            assert work["previousBlockHash"] == genesis_a["hash"]
            reward_a = a.rpc("eth_getBlockReward", ["0x1"])
            reward_b = b.rpc("eth_getBlockReward", ["0x1"])
            assert reward_a == reward_b and reward_a["miner"] == work["minerReward"]
            a.rpc("eth_getWork", expect_error=True)
            assert a.rpc("eth_submitAuxBlock", [work["workHash"], "0x00"]) is False
            assert a.rpc("eth_submitAuxBlock", ["0x"+"ab"*32, "0x00"]) is False
            later = eventually(lambda: (lambda w: w if int(w["timestamp"],16)>int(work["timestamp"],16) else None)(a.rpc("eth_createAuxBlock")),
                               "quiet-chain candidate refresh", 15)
            assert later["workHash"] != work["workHash"]
            b.stop()
            b.start()
            assert b.rpc("eth_getBlockByNumber", ["0x0", False])["hash"] == genesis_a["hash"]
            a.rpc("admin_addPeer", [enode])
            eventually(lambda: int(b.rpc("net_peerCount"),16) >= 1, "peer reconnect after restart")
            assert a.rpc("eth_blockNumber") == b.rpc("eth_blockNumber") == "0x0"
            results.update(status="PASS_READINESS_ONLY", genesisHash=genesis_a["hash"],
                           stateRoot=genesis_a["stateRoot"], target=expected_target,
                           work=work, refreshedWork=later, reward=reward_a,
                           peerReconnect=True, acceptedPostForgeBlocks=0)
            print("PASS: two real OGG nodes, matching genesis/state/balance, P2P handshake/reconnect, issued work, canonical reward RPC, refresh, invalid-work rejection", flush=True)
            print("OPEN: genuine target-winning OGG submission, mined-block propagation, reorg and payout acceptance", flush=True)
        except BaseException as exc:
            results.update(status="FAIL", error=str(exc))
            raise
        finally:
            for n in nodes:
                n.stop()
            (report/"results.json").write_text(json.dumps(results, indent=2)+"\n")


if __name__ == "__main__":
    main()
