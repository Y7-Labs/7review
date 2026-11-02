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

One transaction prepares immutable request bytes, the qualified upper amount,
three scope holds and lease storage. A separate fenced claim and sending commit
must precede provider I/O. Returning a preparation/replay receipt never authorizes
I/O. Provider calls run outside SQL transactions; there is no automatic resend.
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
Schemas v8/v9 add prepared-action leases, fenced outcome provenance and confined
recovery ownership. Schema v10 adds immutable local reduction artifacts and exact
input links. Schema v11 adds parent-to-child links and a deferred child-grant FK.
Schema v12 adds the terminal reduction reference on owned jobs.
Worker runtime now requires v12; run explicit migration before restart.
Schema v7 commits complete accepted aggregates on top of v6 action-result
recovery, v5 owned jobs, v4 inboxes and v3 generation fencing. Holds are not reset.
A missing or
partial ledger is an error, not permission to initialize an empty replacement.
The real-PostgreSQL v8-to-v9 rehearsal includes a deferred commit failure, DDL and
provenance rollback, idempotent retry, unchanged requests/quotes/holds/tokens and
deadlines, and live prepared/fenced/unfenced sending receipts. Runtime rejects v8
instead of migrating it. This is a disposable-schema rehearsal, not an upgrade
of an operator's deployed database or proof of backup/failover readiness.
The v9-to-v10 rehearsal also injects commit failure, verifies that both new tables
roll back, retries idempotently and reduces an existing v9 response without
rewriting its request, quote, outcome or holds. No deployed store was upgraded.
The v10-to-v11 rehearsal proves atomic DDL/constraint rollback and retry, and that
a dangling child restriction cannot commit. No temporary unrestricted grant is
needed to create a child and reserve its three scope holds in one transaction.
The v11-to-v12 rehearsal injects deferred commit failure, verifies column/version
rollback, retries idempotently and completes a pre-existing reduction afterward.

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

Canonical GitHub/GitLab review requests first acquire trusted SCM/base policy and
prepare the initial Source/skill/rule plan, without model or publication effects.
One transaction then commits inbox, pending job, attested attempt/work intent,
generation pointer, intake receipt, prepared state and initial acceptance event
before HTTP 202. Responses include `durable: true`, stable `run_id`/`attempt_id`
and acceptance status. Preparation is bounded by a 30-second HTTP intake context;
its time counts toward the original execution deadline. PostgreSQL microsecond
normalization never extends that deadline.
Delivery replay returns that same ID and never extends its original deadline;
changed raw or normalized payloads are rejected and durably audited. Requests
without a delivery ID (including explicit manual requests) are fresh intents,
not idempotent retries. Ignored/non-reviewable events do not claim durable work.
Known delivery replays resolve the committed aggregate before new SCM reads or
current trigger selection, including after a lost 202 or expired retry deadline.
Corrupt/missing checkpoints cannot be acknowledged or reconstructed implicitly.

Workers poll pending rows, so a crash between acceptance and queue notification
cannot lose accepted work. A committed exclusive claim precedes pipeline entry.
Workers load the checksummed accepted Source/continuation, not newer SCM state;
they do not re-admit the request. Initial admission ownership is transaction-local
and revoked before commit. The model sending transaction checks the worker owner and commits
the job effect-started marker with the action and all holds. Duplicate-only jobs
link to the original receipt but cannot authorize additional spending.
The production authority requires owned jobs for service admission/dispatch;
the low-level registration seam cannot bypass it. Explicit ephemeral CI retains
its separate pipeline/job identity. Legacy service rerun/tool paths fail closed
until they return a new durable intake receipt; use explicit review intake for
a fresh service request instead of calling the old rerun execution path.
Unclaimed pending jobs can execute from their frozen aggregate. Claimed jobs are
never replayed as a whole. Eligible prepared actions have the separate confined
recovery path below. Otherwise a crash, panic or shutdown after claim leaves the row running
until its original deadline, then becomes `uncertain`. Expired pending work becomes
`expired`. Neither state releases budget holds or authorizes redispatch.

`GET /inbox?run_id=<id>` uses the existing API-token middleware and returns only
run ID, state, original deadline, attempt/job linkage and action recovery counts,
including when no local run artifact exists. It never returns response bodies.
Configure `API_TOKEN` to protect this operational endpoint. `completed` means the
pipeline returned successfully, not provider settlement or publication approval.
Database/claim/completion failures never fall back to the volatile queue.
Readiness checks the canonical inbox schema/storage separately from the legacy
closure queue and reports pending, running and uncertain counts for the trusted
project. Storage readiness is not live-model qualification or review completion.

Committed model responses can now be indexed locally, once logically, into
`recorded_unvalidated` action results. Bounded recovery workers perform no model
I/O, finding approval, job completion or financial settlement. Response hashes
and originating action/job links are retained; unknown/corrupt outcomes are not
promoted. Recovery storage failure blocks new inbox claims for that poll.

This conservative recovery does not replay a claimed job wholesale. Validated-result continuation,
authenticated operator recovery, durable final run artifacts and the complete
mutable assessment/action state machine remain unimplemented. Before-202 initial
aggregate atomicity is not completion of those separate recovery/release gates.
Pre-v7 nonterminal jobs without checkpoints become `legacy_unknown`; they are
not automatically executed or silently upgraded from current SCM. Their original
result attribution and unresolved holds remain available for conservative recovery.
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

Important boundary: only the opt-in canonical runtime has the before-202 aggregate
contract. Legacy mode still uses the in-process queue. Full action continuation,
provider revocation/currentness and governed native delivery remain open work.

## Prepared Actions and Leases

Configured PostgreSQL model dispatch follows `PrepareAction`, `ClaimPrepared`,
`BeginPrepared`, then provider I/O and fenced `RecordOutcome`. Preparation is
idempotent by exact action/request identity and never repeats reservations.
Claims use database time, an expected epoch, a monotonically increasing fence and
a 1 ms to 5 minute lease (normal dispatch requests 30 seconds). Leases are capped
by the original execution and qualification deadlines, never renewed implicitly.

Begin rechecks current attempt/job authority, scope integrity, existing holds,
budget incidents, route/price qualification, worker/fence and lease. The sending
marker and sending fence commit together. The lease gates beginning; once sending
commits it cannot be reclaimed, and inference retains its qualified active-time,
execution and price deadlines rather than a new implicit 30-second timeout.
A replay is non-executable. Late attributable responses may be recorded after
lease expiry, but only under the fence issued to the sending owner. Prepared
actions cannot have outcomes or final usage before sending. Missing lease data
on a fenced action is an error, not a downgrade to legacy authorization.

`PreparedRecoveryCandidates` is bounded host-only discovery, not dispatch
authority. `ReclaimPrepared` may adopt an unclaimed action or one whose lease has
expired, only while it remains prepared and the original service job is current,
within deadline and without external progress. Reclaim rotates the service job
token and confines the new token to that action. Old workers cannot begin or
schedule another ID; the recovery owner cannot rerun the whole pipeline either.
Reclaim returns the original immutable input for a qualified recovery adapter.

The existing bounded inbox worker pool now scans up to eight prepared actions
per iteration. Reclaim, immutable-request verification and fenced Begin precede
every recovered call. Recovery uses the stored provider/model and explicit
`complete` or `stream` invocation, never current role routing or a fallback.
Streaming is captured locally; native schemas pass unchanged through Complete,
under the existing route's native-tool qualification. Neither path executes a
tool or publishes output. Legacy intents without an invocation mode fail closed,
as do unavailable original providers/APIs, changed bounds and malformed receipts.
Process-death, duplicate-worker, stale-owner and interrupted-commit tests use
controlled providers. The production catalog remains empty, not implicitly qualified.

Sending/uncertain/result actions are never reclaimed. Responses are indexed as
`recorded_unvalidated`; the local reducer below may produce a separate validated
artifact. Neither indexing nor reduction finishes a job or clears its confined
token. Prepared cancellation/refund, lease renewal, authenticated operator recovery
and atomic next-action continuation remain open. Ephemeral runtime recovery
requires acknowledged service handoff; a CI retry cannot masquerade as durable
service ownership. Unused and unknown holds are retained until a qualified
settlement or future verified not-executed operation, not released on expiry.

### Immutable Model Continuation Input

Pipeline actions now bind an optional version-1 model checkpoint into the full
immutable action digest. It contains the detached pre-call `Source`, prior raw
responses and a typed batch/tool-round/findings-repair/coverage-repair cursor.
Batch slots share one frozen pre-batch input, and live responses merge by batch
index rather than completion order. Serial mode processes every batch too.
Default validator version/confidence settings and reasoner/formatter route chains
with output limits are frozen; custom validators have
no qualified deterministic validation contract. No current profile is substituted
for a stored validation setting during recovery.

Checkpoints are bounded at 16 MiB and complete action records at 24 MiB. Unknown
versions, cursor contradictions and changed attempt/execution/policy/SCM bindings
fail before holds are created. Claim/Begin recheck the stored checkpoint, and
reclaim returns its exact original bytes. Legacy missing checkpoints are not
invented from current files and grant no validated-continuation authority.

The checkpoint is input to the versioned local reducer below, not an unrestricted
scheduling grant. Persisting it never clears the recovery restriction.

### Durable Local Reduction

`ModelReducerV1` uses only recorded requests, responses and frozen checkpoints.
It validates all input digests, complete batch slots and the pinned default
validator contract. Responses reduce by batch index, not arrival order. Each
batch is parsed separately; malformed or missing slots cannot be hidden by an
earlier valid JSON candidate, and coverage missing from a slot remains incomplete.

The output is `validated`, `needs_tools`, `needs_findings_repair`,
`needs_coverage_repair` or `blocked`. Tool and formatter cursors are proposals,
never executed by the reducer. Exhausted tools and invalid repaired output cannot
become successful empty reviews. A validated artifact contains a local draft,
finding validation/rejection audit and no final report or approval. Current
profiles are not substituted for stored confidence settings. Missing diff evidence
or unqualified custom validators fail closed.

Inbox workers index responses, then commit bounded reduction artifacts and their
exact input links in one transaction. Job-row locking and unique links prevent
duplicate reducers from applying a group twice. Commit failure/panic/process death
before commit retries only local computation; a lost receipt after commit reuses
the artifact without repeating computation or provider I/O. Journal/outcome swaps,
rewritten evidence or malformed adapter receipts cannot commit. Artifact lookup is
scoped to the trusted boundary/project and verifies its stored digest and attribution.
The current bounds are 128 input actions, 64 MiB aggregate input and 32 MiB output;
incomplete groups wait rather than emitting partial validated artifacts.

`/inbox` exposes validated-artifact, pending-continuation and blocked-reduction
counts, not model response bodies. Its existing raw `recorded_unvalidated` count
still reports the raw journal; a separate artifact is the validation record.
Artifacts can retain attributed historical evidence after a deadline; lookup alone
does not authorize current use, publication or more spending. Reduction by itself
schedules no model/tool action, widens no recovery grant and releases no hold.
Formatter parent-to-child advancement is described below. Durable job completion
and the complete tool/live-pipeline continuation path remain open.

### Confined Formatter Advancement

New checkpoints include versioned host routing for reasoner and formatter roles.
Dispatch must match its role's frozen route and output limit. Optional routing
fields preserve old checkpoint/group identities; old artifacts never acquire a
formatter route from current configuration. No new model/provider qualification
is implied, and the production catalog remains empty.

`ModelContinuationV1` builds only the typed findings/coverage formatter request
from the parent artifact, pinned route and exact prior context. It supplies no
action ID, job token, reservation or send permission. Tools require committed
observations and are not synthesized as empty successful tool results.

`AdvanceModelContinuation` rechecks the current job, full parent input proof,
the parent's sending job epoch, source identity, generation, limits and route qualification, then commits exactly
one deterministic prepared child, all holds, parent link and rotated job token.
The new token is restricted to that child before commit. The deferred FK allows
the exact pointer to be set before child insertion, but forbids a missing child at
commit. Failure rolls back token, pointer, action, lease and holds together.
Parent replay returns the existing descriptor, never a new paid action.

Inbox workers automatically advance eligible formatter artifacts. Recovery can
reclaim the linked child after external progress only with its intact parent proof
and no unresolved/unreduced sibling effects. Every later claim/Begin rechecks that
proof and original deadlines; sending children never become retryable. Process
death after prepare/claim/sending, competing workers and changed proofs/config
are exercised against PostgreSQL and controlled providers. Consumed parent plans
are excluded from `/inbox` pending-continuation counts.

Canonical live formatter calls now use the same durable transition as recovery.
After a response is durably recorded, the live context retains a detached transient
action/request/response reference. When findings or coverage repair is needed,
the live path indexes/reduces that parent, verifies the matching stored artifact
and advances through the shared builder. It does not issue a separate formatter
call or substitute a changed role configuration. If recovery already advanced,
the same child descriptor is reused.

The live frame then hands off rather than pretending it still owns the job.
The inbox worker must not finish or fail the job with the revoked token, and the
frame stays nonterminal without approval/publication. A barrier-controlled race
lets recovery advance/execute first, then releases the old live frame; only one
child, two total calls and the original holds are retained. Invalid references,
parent phase/digests/run/deadline or missing authority fail closed without legacy
reservation fallback. Ephemeral/legacy execution retains its separate path.

### Atomic Batch Preparation Foundation

The internal `PrepareModelBatch` API reserves one complete ordered initial batch
set and all its holds in a single transaction, bounded to 128 slots/64 MiB. It
checks identical frozen group inputs and job ownership, rejects partial legacy
sets, and returns receipts only after commit. A later-slot route/capacity denial
or deferred commit failure rolls back the whole set. Competing callers and exact
restart retries preserve one set. Sending actions cannot be reset by retry.

This API is not yet wired into `CompleteParallel`; live batching still prepares
individual actions. Sealed membership and group-confined recovery ownership must
be implemented before integration. It does not loosen action-only reclaim or
make prepared siblings after external progress eligible for broad job takeover.

### Terminal Model Projection

Bounded inbox workers can now commit a terminal job reference for validated or
blocked model reductions. The transaction locks job/inbox authority, checks the
current active attempt and original deadline, current sending fences/job epoch,
and the exact terminal input group. A bounded lineage walk rechecks every parent
artifact, original response and frozen formatter edge. Every job action must
belong to that proof; even an unrelated already-reduced sibling blocks completion.
The same commit finishes the inbox/job and disables dispatch. A validated draft
is reported as drafted, a blocked result as failed. Neither grants approval,
publishes a final report, settles unknown usage nor releases existing holds.

Competing finishers converge on the same committed reference; failed commits
return no projection. Process interruption before commit with backend rollback,
and process exit immediately after commit, both recover through local completion
without a new action or provider replay. A dead client alone does not prove that
PostgreSQL rolled back its pending COMMIT. Already-finished legacy jobs may acquire
a matching terminal reference as read-only attribution, not renewed authority.

HTTP `/run` and `/runs` prefer verified PostgreSQL terminal projections over stale
in-memory frames. Listing is bounded to 128 durable results and 64 MiB of source
artifacts. Storage or proof errors return 503, not a stale memory substitute.
These are read projections, not run-store rehydration or authenticated operator
mutation controls. Historical reads verify lineage without reopening an attempt.

This does not complete general continuation. Unlinked prepared work after
external progress still fails closed. Prepared batch backlogs need the same
cursor/manifest ownership rather than broad job replay. Tool observations and
continuations remain open, alongside authenticated controls and safe
cancellation/refund. The terminal projection replaces handed-off HTTP views with
the authoritative stored result, never resumes the old frame. A formatter
advancement itself still does not finish, approve or publish the review.

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
- Live recovery-adapter qualification, prepared cancellation, validated continuation and provider currentness
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
