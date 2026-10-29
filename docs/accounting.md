# Coordinated Accounting Progress

This is an implementation boundary, not a completed ceilings or release claim.
The normal service and CI paths still fail closed for bounded model dispatch.

## Implemented Boundary

`agent/accounting.Authority` implements the orchestrator budget contract against
PostgreSQL. Canonical intake commits a verified attempt, execution context, policy
limits and operator binding before dispatch. Workers cannot register themselves
implicitly by submitting a model request. Closing or superseding an attempt fences
new dispatch but does not release its old holds.

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

Final settlement is a separate trusted reconciliation operation. It requires an
explicit receipt verifier; no default implementation trusts worker flags. The
transaction replaces a hold once, retaining the original period at every scope.
Late settlement cannot reopen an attempt. Authenticated but unmatched receipts
are quarantined. Over-bound actual usage is retained as actual, records an
integrity incident and blocks further spending in the affected scopes across
renewals. Known active-time overruns trigger the same block without waiting for
token usage. Incident corrections still require a future authorized adjustment
workflow; ordinary receipts cannot clear them.

Migrations are explicit and versioned. Every accounting transaction checks schema
compatibility and forces synchronous commit. Migration requires `fsync` and
`full_page_writes` enabled. Use a dedicated restricted database/schema; request and
result artifacts contain sensitive review context. Keep migration privileges out
of ordinary worker credentials. Backup/restore and deployment failover still need
qualification before coordinated release.
Schema v5 adds transactional acceptance jobs and owned attempt linkage to the
v4 durable HTTP inbox and v3 intake receipts/generation fencing,
without clearing holds. A missing or
partial ledger is an error, not permission to initialize an empty replacement.

## Runtime Intake

Set all of `ACCOUNTING_DATABASE_URL`, `ACCOUNTING_BOUNDARY_ID` and
`ACCOUNTING_PROJECT_ID` in trusted host configuration, or leave all unset. Partial
configuration, unavailable storage and incompatible schema fail explicitly, never
fall back to a fresh local allowance. Database errors do not echo the DSN.
Trusted `POLICY_RUNTIME_CEILINGS_JSON` remains required.

```sh
go run ./cmd/7review accounting migrate
```

Migration is explicit; startup never initializes replacement tables. The service
requires `POLICY_V2_MODE=enforce` for this runtime and uses the configured SCM API
host, not a webhook/report URL. It runs artifact-only, with legacy publication and
optional ungoverned memory/context side effects disabled. The runtime factory has
an empty qualified-route catalog and no receipt verifier: enabling intake alone
cannot spend on any production model. Real-provider qualification remains required.

### HTTP Acceptance and Recovery

Canonical GitHub/GitLab review requests commit a PostgreSQL inbox row before HTTP
202. Responses include `durable: true`, a stable `run_id` and acceptance status.
Delivery replay returns that same ID and never extends its original deadline;
changed raw or normalized payloads are rejected and durably audited. Requests
without a delivery ID (including explicit manual requests) are fresh intents,
not idempotent retries. Ignored/non-reviewable events do not claim durable work.

Workers poll pending rows, so a crash between acceptance and queue notification
cannot lose accepted work. A committed exclusive claim precedes pipeline entry.
Only unclaimed pending work is recoverable automatically. A crash, panic or
shutdown after claim is not permission to execute again: the row remains running
until its original deadline, then becomes `uncertain`. Expired pending work becomes
`expired`. Neither state releases budget holds or authorizes redispatch.

`GET /inbox?run_id=<id>` uses the existing API-token middleware and returns only
run ID, state and original deadline, including when no local run artifact exists.
Configure `API_TOKEN` to protect this operational endpoint. `completed` means the
pipeline returned successfully, not provider settlement or publication approval.
Database/claim/completion failures never fall back to the volatile queue.
Readiness checks the canonical inbox schema/storage separately from the legacy
closure queue and reports pending, running and uncertain counts for the trusted
project. Storage readiness is not live-model qualification or review completion.

This conservative recovery intentionally sacrifices automatic progress after a
claimed crash. Action-level resume, authenticated operator recovery, durable
result storage and full inbox-to-attempt/event atomicity remain unimplemented.
Do not requeue uncertain rows manually or delete them to obtain fresh allowance.
Inbox fault tests include deferred commit failures and abrupt subprocess exit
after acceptance/claim commit; replay preserves the same original receipt.

Service workers observe the current generation before SCM acquisition. Admission
compares that generation atomically; a late acquisition cannot move the pointer
backward. Explicit reruns have distinct linked attempts, while automatic events
for an unchanged snapshot/policy reuse the existing attempt. Delivery replay
preserves original authority/deadline; changed payloads under the same delivery
ID are rejected and audited. Authenticated raw webhook digests include fields
that normalization does not retain. Run records no longer overwrite a change-key
record in this path. Canonical receipts are checkpointed before model dispatch.

CI always binds ephemeral identity before inference, including in failure
artifacts. Provider host comes from trusted CI/server configuration (`--provider-host`
for an explicit local override), not the PR URL. With no shared runtime, the
receipt is only job-local and makes no shared-accounting claim. An explicitly
configured shared runtime can checkpoint CI accounting, but CI stays ephemeral,
does not advance the service tracking pointer and does not acquire service/native
publication ownership. Untrusted forks do not connect to the shared runtime.
Replayed shared CI work is not inferred again; without retrieval of the original
outcome it ends incomplete rather than claiming a pass.
Hosted GitHub intake rejects missing PR identity or a target repository differing
from the runner repository. Missing source-project provenance is untrusted, not a
same-project assumption. Hosted GitLab uses numeric `CI_PROJECT_ID`, matching the
service rather than the renamable `CI_PROJECT_PATH`. Existing path-keyed policy or
accounting records need deliberate migration/alias reconciliation before enabling
live coordinated spending; historical holds are not rewritten automatically.

Important boundary: HTTP enqueue acknowledgement is still the legacy in-process
queue, not a durable inbox receipt. Worker intake durability does not qualify
HTTP crash recovery. Full action ownership/recovery, provider revocation/currentness
and governed native delivery remain separate open work.

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
Qualification is rechecked after lock waits. Permits expire at the earlier
execution or qualification deadline, so a stalled sender cannot use stale price
authority. Receipt identity conflicts are serialized even across project scopes.

## Verification

Run the isolated real-PostgreSQL suite, including race detection:

```sh
scripts/test-accounting-postgres.sh -race
# Also exercise service/CI construction and the actual CLI path:
scripts/test-accounting-postgres.sh -race ./agent/app ./agent/ci ./agent/pipeline ./cmd/7review
```

The script creates a localhost-only disposable container and removes only that
container afterward. Tests use independent temporary schemas. Ordinary `go test`
skips database cases when `SEVENREVIEW_TEST_POSTGRES` is unset; that skip is not
durability evidence. Tests exercise last-unit races at every scope, cross-repo
project admission, rollback, process death after commit, replay refusal, unknown
holds, database outage, incompatible schema and actual guarded provider dispatch.

## Still Required

- Production receipt-verifier adapters and authenticated operator settlement
  commands; library-level reconciliation tests use an explicit test verifier.
- Full action-level recovery/ownership and provider currentness
  end to end; prospective authority reconfiguration currently fails closed.
- Complete stable-provider-ID/alias migration and rename qualification, including
  historical path-keyed records; new identity checks are not proof of that exit.
- Live provider route qualification and attributable usage receipts.
- Tool/effect resource accounting, bounded artifact retention, operational
  recovery, backup/restore and the remaining G01-G03/G04 acceptance cases.

Scoped conflicts are intentionally not part of this work.

Design references: [PostgreSQL row locks](https://www.postgresql.org/docs/17/explicit-locking.html),
[PostgreSQL WAL durability](https://www.postgresql.org/docs/current/runtime-config-wal.html),
and [provider token-count estimates](https://platform.claude.com/docs/en/build-with-claude/token-counting).
