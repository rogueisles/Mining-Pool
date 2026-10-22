#!/usr/bin/env python3
"""Check a dedicated FORGE Scrypt test-pool profile against NiceHash requirements.

Reads the public API by default. --requirements is an OFFLINE snapshot, not a
live provider check. --write-profile creates a new mode-0600 config; never edits
the input, starts services, contacts configured nodes, or purchases hashpower.
Passing this check is NOT NiceHash pool verification or mined-block acceptance.
"""
import argparse
import json
import math
import os
import pathlib
import re
import sys
import urllib.request

API = "https://api2.nicehash.com/main/api/v2/public/buy/info"
LIMIT = 1024 * 1024


def strict_object(pairs):
    obj = {}
    seen = set()
    for key, value in pairs:
        # Go's config decoder also matches keys case-insensitively.
        if key.casefold() in seen:
            raise ValueError("duplicate JSON field")
        seen.add(key.casefold())
        obj[key] = value
    return obj


def decode(blob):
    if len(blob) > LIMIT:
        raise ValueError("JSON exceeds size bound")
    return json.loads(blob, object_pairs_hook=strict_object)


def requirements(data):
    rows = [x for x in data.get("miningAlgorithms", []) if x.get("name") == "Scrypt"]
    if len(rows) != 1 or rows[0].get("algo") != 0:
        raise ValueError("missing or ambiguous Scrypt requirements")
    values = [rows[0].get(k) for k in ("min_diff_initial", "min_diff_working")]
    if any(type(x) not in (int, float) or x <= 0 or x > 2**53-1 or not math.isfinite(x) for x in values):
        raise ValueError("invalid provider difficulty requirements")
    return math.ceil(max(values))


def duration_seconds(value):
    # Deliberately accepts the simple units used by this test profile only.
    m = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)(ms|s|m)", str(value))
    if not m:
        raise ValueError("use a positive duration in ms, s or m")
    return float(m[1]) * {"ms": .001, "s": 1, "m": 60}[m[2]]


def check(config, minimum):
    proxy = config.get("proxy", {})
    forge = proxy.get("forge", {})
    stratum = proxy.get("stratum", {})
    if not all(x is True for x in (proxy.get("enabled"), forge.get("enabled"), stratum.get("enabled"))):
        raise ValueError("proxy, FORGE and Stratum must already be enabled")
    diff = proxy.get("difficulty")
    if type(diff) is not int or diff < minimum:
        raise ValueError("configured difficulty is below the provider minimum or is not an integer")
    # Network diff1 equals Scrypt Stratum diff65536. A miner told a harder
    # share target will withhold some valid diff1 OGG solutions entirely.
    if diff > 65536:
        raise ValueError("test profile difficulty exceeds the diff1 OGG coverage bound; review network settings")
    for field, upper in (("extranonce1Size", 8), ("extranonce2Size", 16)):
        size = forge.get(field, 0)
        if type(size) is not int:
            raise ValueError("extranonce size must be an integer")
        size = 4 if size == 0 else size
        if type(size) is not int or not 4 <= size <= upper:
            raise ValueError("test profile requires at least four extranonce bytes within pool bounds")
    refresh = duration_seconds(proxy.get("blockRefreshInterval"))
    validity = forge.get("templateValiditySec", 60) or 60
    if not 0 < refresh <= 2 or type(validity) is not int or not 60 <= validity <= 300:
        raise ValueError("test profile requires refresh <=2s and template validity 60..300s")
    backlog = forge.get("jobBacklog", 8) or 8
    if type(backlog) is not int or not 8 <= backlog <= 64:
        raise ValueError("job backlog outside test profile bounds")
    if stratum.get("tls", False) is not False:
        raise ValueError("this profile checks plain Stratum TCP only; TLS needs separate provider verification")
    if not isinstance(stratum.get("listen"), str) or not stratum["listen"]:
        raise ValueError("Stratum listen address is required")
    return {"difficulty": diff, "providerMinimum": minimum,
            "oggDiffOneSolutionsCovered": True, "refreshSeconds": refresh}


def profile(config, minimum):
    # Require a complete existing FORGE config before making a separate copy.
    result = json.loads(json.dumps(config))
    proxy = result["proxy"]
    forge = proxy["forge"]
    proxy["difficulty"] = minimum
    proxy["blockRefreshInterval"] = "1s"
    forge.update(extranonce1Size=4, extranonce2Size=4, jobBacklog=64, templateValiditySec=120)
    check(result, minimum)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=pathlib.Path)
    parser.add_argument("--requirements", type=pathlib.Path)
    parser.add_argument("--write-profile", type=pathlib.Path)
    args = parser.parse_args()
    try:
        config = decode(args.config.read_bytes())
        if args.requirements:
            data = decode(args.requirements.read_bytes())
            source = "OFFLINE snapshot; current API check still required"
        else:
            with urllib.request.urlopen(API, timeout=10) as response:
                data = decode(response.read(LIMIT+1))
            source = "public NiceHash API"
        minimum = requirements(data)
        if args.write_profile:
            config = profile(config, minimum)
        result = check(config, minimum)
        if args.write_profile:
            # Exclusive creation, no overwrite or replacement of live config.
            fd = os.open(str(args.write_profile), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w") as out:
                json.dump(config, out, indent=2)
                out.write("\n")
        print(json.dumps({"status": "PASS_CONFIG_PREFLIGHT_ONLY", "source": source, **result}))
        print("OPEN: public reachability, NiceHash pool verification, accepted rented shares/OGG blocks and accounting")
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        # Configs may contain passwords; never echo them or arbitrary exceptions.
        print("FAIL: configuration/requirements check or file operation failed; no service was started", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
