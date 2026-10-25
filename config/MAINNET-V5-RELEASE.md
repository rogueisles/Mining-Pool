# Mainnet V5 release profile

The prepared v4.1.0 profile schedules FORGE V5 at **RGN block 1,121,800**.
Its SHA256 is:

```text
87c66819311b112adce09855f9c2c38490fb32a193ea35ca7c466892319aefd8
```

The [original deterministic generator review](mainnet-v5/REVIEW.txt) records the public
bootstrap, administrator quorum, preserved native treasury hashes and observations
used when this profile was prepared. Its “offline preparation” heading and
“remaining production gates” list describe the generator’s limited role at that
time. They are retained unchanged as provenance; they are not a live deployment
status report. The generator neither deploys services nor verifies wallet custody.

Use the [release qualification report](https://github.com/rogueisles/Rogue-Network/blob/main/docs/RELEASE-QUALIFICATION-4.1.0.md),
the repository README and the published release checksums for the final test
scope and installation instructions. Preparation or publication of this profile
does not establish that any running validator, exchange or pool has installed it.

Every validating node and participating mining integration must install the same
approved schedule before the activation height. The estimated time is around
27 October 2026, 13:35 UTC; block height determines activation. Verify the
current canonical head and sufficient rollout notice before scheduling services.
Keep the original genesis, chain database, wallet custody and protected RPC policy.
