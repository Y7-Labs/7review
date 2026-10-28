# Coordinated Accounting Progress

This is an implementation boundary, not a completed ceilings or release claim.
The normal service and CI paths still fail closed for bounded model dispatch.

## Implemented Boundary

`agent/accounting.Authority` implements the orchestrator budget contract against
PostgreSQL. Trusted intake must explicitly register a verified canonical attempt,
execution context, policy limits and operator binding. Workers cannot register
themselves implicitly by submitting a dispatch. Closing an attempt fences new
dispatch but does not release its old holds.

Trusted host configuration supplies the authority boundary and project ID. Project
scope is shared across repositories assigned to that project. Change keys include
provider, host and repository. Attempts have lifetime limits; shared scopes use
fixed half-open periods assigned by the database clock after scope locks.

One transaction commits immutable request bytes, the qualified upper amount,
three scope holds and a sending marker. The synchronous first implementation has
no prepared queue, lease reclaim or automatic resend. Returning a replay receipt
does not authorize provider I/O. Provider calls run outside SQL transactions.
Unknown usage and recorded responses without usage retain full holds, including
across renewal periods. SQL failures never create an in-process allowance.

Migrations are explicit and versioned. Every accounting transaction checks schema
compatibility and forces synchronous commit. Migration requires `fsync` and
`full_page_writes` enabled. Use a dedicated restricted database/schema; request and
result artifacts contain sensitive review context. Keep migration privileges out
of ordinary worker credentials. Backup/restore and deployment failover still need
qualification before coordinated release.

## Token And Price Qualification

There are no production route defaults or hardcoded live prices. Trusted host
code supplies immutable `RouteBound` records for exact provider/model pairs,
with validity, currency, version and evidence reference. The first supported
input mode reserves a documented provider-enforced billable input ceiling in
full. It is not a character-count estimate or an exact tokenizer implementation.

Qualification must guarantee that `max_tokens` bounds all billable output,
including reasoning, and include all fixed charges. Native tools are denied
unless separately qualified. The catalog must not claim such guarantees for an
unverified proxy, alias or dynamically routed model. A reference string alone is
not provider qualification: operators must establish the underlying contract.
Unknown routes fail closed. Integer micro-prices round upward; overflow denies.
The ledger preserves the complete qualified contract and refuses same-version
price rewrites. Configured active-time holds are enforced by dispatch deadlines.

## Verification

Run the isolated real-PostgreSQL suite, including race detection:

```sh
scripts/test-accounting-postgres.sh -race
```

The script creates a localhost-only disposable container and removes only that
container afterward. Tests use independent temporary schemas. Ordinary `go test`
skips database cases when `SEVENREVIEW_TEST_POSTGRES` is unset; that skip is not
durability evidence. Tests exercise last-unit races at every scope, cross-repo
project admission, rollback, process death after commit, replay refusal, unknown
holds, database outage, incompatible schema and actual guarded provider dispatch.

## Still Required

- Verified final-usage reconciliation, origin-period settlement and over-bound
  integrity handling.
- Canonical service/CI intake, durable currentness and action ownership wired
  end to end; prospective authority reconfiguration currently fails closed.
- Live provider route qualification and attributable usage receipts.
- Tool/effect resource accounting, bounded artifact retention, operational
  recovery, backup/restore and the remaining G01-G03/G04 acceptance cases.

Scoped conflicts are intentionally not part of this work.

Design references: [PostgreSQL row locks](https://www.postgresql.org/docs/17/explicit-locking.html),
[PostgreSQL WAL durability](https://www.postgresql.org/docs/current/runtime-config-wal.html),
and [provider token-count estimates](https://platform.claude.com/docs/en/build-with-claude/token-counting).
