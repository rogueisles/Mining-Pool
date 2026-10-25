# Source provenance

Version 4.1.0 combines the recorded public Mining-Pool source base with the later
native-latency/accounting repairs and the FORGE V5 registry integration.
`RELEASE-SOURCE.json` records the verified public repository base commits and
their observation time; those hashes identify the starting sources rather than
the final release commit. The retained production repairs include parallel signer
quorum, fast DGB checks, durable share acknowledgements and canonical-maturity
reconciliation. They are not replaced by the older published mining core.

Public setup helpers, website source, example configuration and the separate
private-payout reference are preserved. Legacy examples remain DGB/DOGE-only and
do not generate a V5 schedule. Use the current operator guide and approved
v4.1.0 profile for this release. No live ledger, payment key or RPC password is
distributed. Legacy protocol domains and ledger identifiers remain intentional.

Current binary and implementation-file hashes are supplied by the release
qualification/build reports. Files under `evidence/` and `docs/history/` describe
the earlier published release and are not current artifact hashes or fresh
qualification. Source comparisons preserve the qualified implementation apart
from line-ending normalization; release documentation/profile packaging is
identified separately.

Native regression tests use disposable chains. Real native mainnet preflight
checks validate unsolved proposals without submitting mined blocks. These checks
do not establish a canonical mainnet SOQ reward. Post-activation propagation,
native reward maturity and live payouts require subsequent observation.
Preparing or publishing source and profiles does not install them on running
validators. Separate deployment reports record changes to production services.
