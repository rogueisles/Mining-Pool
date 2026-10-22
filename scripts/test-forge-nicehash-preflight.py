#!/usr/bin/env python3
"""Offline regression checks: no nodes, provider calls or rental purchases."""
import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
path = pathlib.Path(__file__).with_name("forge-nicehash-preflight.py")
spec = importlib.util.spec_from_file_location("preflight", path)
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)


class PreflightTests(unittest.TestCase):
    def setUp(self):
        self.config = {"proxy": {"enabled": True, "difficulty": 1, "blockRefreshInterval": "1h",
                                 "forge": {"enabled": True}, "stratum": {"enabled": True, "listen": "0.0.0.0:3333"}},
                       "redis": {"password": "test-secret-not-for-output"}}
        self.api = {"miningAlgorithms": [{"name": "Scrypt", "algo": 0, "min_diff_initial": 50000, "min_diff_working": 50000}]}

    def test_provider_and_network_bounds(self):
        minimum = p.requirements(self.api)
        self.assertEqual(minimum, 50000)
        cfg = p.profile(self.config, minimum)
        self.assertEqual(p.check(cfg, minimum)["difficulty"], 50000)
        self.assertEqual(self.config["proxy"]["difficulty"], 1)
        for diff in (1, 49999, 65537, True, "50000"):
            cfg["proxy"]["difficulty"] = diff
            with self.assertRaises(ValueError): p.check(cfg, minimum)
        cfg["proxy"]["difficulty"] = 65536
        p.check(cfg, minimum)
        self.api["miningAlgorithms"][0]["min_diff_working"] = 100000
        with self.assertRaises(ValueError): p.profile(self.config, p.requirements(self.api))

    def test_bad_requirements_and_refresh(self):
        for value in (0, -1, None, True, "50000", float("nan"), float("inf")):
            self.api["miningAlgorithms"][0]["min_diff_working"] = value
            with self.assertRaises(ValueError): p.requirements(self.api)
        with self.assertRaises(ValueError): p.decode(b'{"proxy":{},"proxy":{}}')
        with self.assertRaises(ValueError): p.decode(b'{"proxy":{},"Proxy":{}}')
        cfg = p.profile(self.config, 50000)
        for refresh in ("1h", "0s", "3s", "garbage"):
            cfg["proxy"]["blockRefreshInterval"] = refresh
            with self.assertRaises(ValueError): p.check(cfg, 50000)

    def test_exclusive_private_output_and_no_secret_disclosure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cfg, api, out = (root / n for n in ("in.json", "api.json", "out.json"))
            cfg.write_text(json.dumps(self.config)); api.write_text(json.dumps(self.api))
            cmd = [sys.executable, str(path), str(cfg), "--requirements", str(api), "--write-profile", str(out)]
            run = subprocess.run(cmd, capture_output=True, text=True)
            self.assertEqual(run.returncode, 0, run.stderr)
            self.assertIn("OFFLINE", run.stdout)
            self.assertNotIn("test-secret", run.stdout+run.stderr)
            self.assertEqual(out.stat().st_mode & 0o777, 0o600)
            original = out.read_bytes()
            again = subprocess.run(cmd, capture_output=True, text=True)
            self.assertNotEqual(again.returncode, 0)
            self.assertEqual(out.read_bytes(), original)
            self.assertEqual(json.loads(cfg.read_text()), self.config)


if __name__ == "__main__":
    unittest.main()
