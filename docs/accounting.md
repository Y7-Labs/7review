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
Schema v13 adds immutable batch membership seals and their job references.
Schema v14 adds immutable snapshot observation receipts, three scope links and
the current observation reference on owned jobs.
Schema v15 adds typed tool-child links binding each child to its exact parent
reduction and committed observation receipt/digest.
Schema v16 adds authenticated command receipts, a separate job control version,
and a cancellation disposition reference. Schema v17 adds verified-unused local
release proofs, their scope audit links and the `not_executed` action disposition.
Schema v18 adds an immutable predecessor-to-successor intake link, distinguishes
cancelled and superseded release authority, and adds the terminal `superseded`
inbox disposition. Schema v19 adds the current exact publication-authorization
pointer on a terminal validated job; it grants no delivery, merge or memory
effect. Schema v20 adds immutable publication-operation payloads and lifecycle
state; migration does not infer operations from existing authorizations. Schema
v21 adds bounded pre-send leases, monotonic fences and a durable sending marker.
Schema v22 adds an immutable provider receipt and completion timestamp bound to
the exact fenced operation. It refuses unreceipted legacy `delivered` rows rather
than inferring an external effect. Schema v23 adds durable lookup eligibility;
uncertain reconciliation reuses the monotonic worker fence and lease. Worker
runtime now requires v24; run explicit migration before restart. Schema v24 adds
immutable authenticated terminal-finding dispositions and a separate current-head
projection. It does not rewrite findings or infer dispositions during migration.
The canonical finding read revalidates terminal lineage and returns one bounded
exact finding, digest, control version and optional attributed current decision;
reviewer, operator and repository participant access remains repository-scoped.
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
The v12-to-v13 rehearsal preserves fenced sending requests/quotes/holds and
outcome attribution across deferred DDL failure and idempotent retry. It never
infers seals or recovery rights for existing unsealed batches.
The v13-to-v14 rehearsal rolls back both observation tables, their index and the
job reference on deferred commit failure, then retries idempotently. Existing
requests, outcomes, reductions, holds and ownership remain unchanged. Migration
does not infer observations, charges or continuation rights from old tool plans.
The v14-to-v15 rehearsal preserves accepted observation bytes, ownership, model
holds and observation scope links across deferred migration failure and retry.
Migration creates no child and grants no new sending or continuation rights.
The v15-to-v16 rehearsal preserves populated sending evidence, fencing leases,
ownership, attempts and scope holds through deferred DDL failure and retry.
Migration creates no command receipt, cancellation, refund or publication consent.
The v16-to-v17 rehearsal preserves populated cancellation receipts, prepared and
sending actions, leases, original scope rows and job versions through deferred
DDL failure and retry. Migration creates no release proof or credit, even for an
already-cancelled prepared action. Runtime refuses an unmigrated v16 store.
The v17-to-v18 rehearsal preserves immutable v17 cancellation proof bytes across
deferred DDL failure and idempotent retry. It does not infer successor links or
supersession credit from historical inactive attempts. Runtime refuses an
unmigrated v17 store.
The v18-to-v19 rehearsal preserves terminal reduction, lineage, inbox and job
bytes across deferred DDL failure and idempotent retry. It creates no operator
command or publication authority. Runtime refuses an unmigrated v18 store.
The v19-to-v20 rehearsal preserves existing authorization and terminal evidence
across deferred DDL failure and idempotent retry. It creates no publication
operation; the materializer re-verifies the exact receipt and terminal lineage
after migration. Runtime refuses an unmigrated v19 store.
The v20-to-v21 rehearsal preserves pending operation bytes across deferred DDL
failure and idempotent retry. It invents no worker, fence, lease or sending marker;
the migrated operation remains claimable. Runtime refuses an unmigrated v20 store.
The v21-to-v22 rehearsal preserves operation and job authority across deferred DDL
failure and idempotent retry. It invents no delivery receipt or completion time;
runtime refuses an unmigrated v21 store and migration rejects an unproven legacy
`delivered` state.
The v22-to-v23 rehearsal preserves an existing uncertain operation across deferred
DDL failure and idempotent retry, then makes it immediately eligible for one
fenced lookup owner. Runtime refuses an unmigrated v22 store.
The v23-to-v24 rehearsal preserves terminal assessment and job authority across
deferred DDL failure and idempotent retry, invents no disposition, and proves the
migrated terminal finding can subsequently receive an exact disposition. Runtime
refuses an unmigrated v23 store.

The service maintenance loop materializes authority, quarantines expired sending
effects, performs read-only reconciliation of uncertain operations, and then
claims at most one pending publication. A provider call occurs only after the
fenced `sending` marker commits. Provider or completion errors remain sending and
can only become uncertain; they are never republished automatically. Missing
remote effects are durably deferred for 30 seconds, and duplicate service workers
cannot hold the same live reconciliation lease.

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
an empty deny-all route catalog unless trusted host qualification is supplied,
and no receipt verifier by default. Enabling intake alone cannot spend on any
production model. Real-provider qualification remains explicit and operator-owned.

### Authenticated Operator Access

Canonical operator access is separate from the legacy shared service token.
`REVIEW_API_TOKEN` does not implicitly identify a human or grant repository roles.
Set `REVIEW_OPERATOR_GRANTS_JSON` in trusted host configuration, with secrets
referenced by environment variable rather than embedded in the JSON:

```json
{"schema_version":1,"principals":[{"id":"alice","token_env":"ALICE_OPERATOR_TOKEN","grants":[{"boundary_id":"service","project_id":"team","provider":"github","host":"github.com","repository_id":"org/repo","roles":["operator"]}]}]}
```

Generate a high-entropy secret (for example, `openssl rand -hex 32`), set it as
`ALICE_OPERATOR_TOKEN`, and restart the service. Never commit the secret. The
boundary and project must exactly match accounting host configuration; provider,
host and repository ID must exactly match the attested change identity. No
wildcards, inferred host aliases or implicit role inheritance are accepted.
Known roles are `operator`, `reviewer`, `publisher`, `participant` and
`memory_curator`; each endpoint still requires its specific role. One local
principal may explicitly receive several roles. Credentials must not be shared
between principals. Duplicate/unknown fields, duplicate grants, missing secrets
and ambiguous credential mappings fail configuration loading. The document is
bounded to 64 KiB and is not repository policy.

Native GitHub/GitLab comment controls use a second trusted mapping,
`REVIEW_SCM_OPERATOR_TOKEN_ENVS_JSON`, from immutable provider numeric user IDs
(`github:<id>` or `gitlab:<id>`) to the same credential environment names. The
webhook signature/token authenticates the provider delivery; it does not grant an
operator role. The mapped secret is authenticated by this directory, then the
ledger atomically verifies the exact provider, trusted host, repository and change
number before exposing the finding subject. Bot usernames are mention and
self-loop identities only and are never authorization identities.

`GET /operator/attempt?attempt_id=<exact-attempt-id>` requires `operator` or
`reviewer` for that repository. Supply the mapped principal's credential in
`Authorization: Bearer <secret>` (or `X-7review-Token`). Identical values in both
headers are supported; repeated or conflicting headers are rejected. The actor
comes only from this mapping; request bodies and extra query fields, including
actor/repository claims, are rejected. Missing configuration or unmapped
credentials return 401; an authenticated actor without the repository role
receives 403. A legacy run alias does not resolve as an attempt ID.

The response contains only durable attempt/run IDs, analysis revision, inbox
state, job phase, currentness, active flag, deadline and `control_version`. It verifies stored
binding and aggregate integrity and never falls back to memory/legacy drafts.
It exposes no claim tokens, prompts or credentials. Historical/expired attempts
remain inspectable with the same repository authority. This projection is not
a dispatch permit or validated assessment. The control version is separate from
immutable admission identity: terminal/control dispositions advance it; ordinary
worker stages and ownership rotations do not. Duplicate delivery aliases do not
become the attempt's control target; reads and commands select the original job.
After cancellation, aliases project that same cancelled state, disappear from
active queue counts and cannot be freshly claimed or completed by stale owners.
`released_unused_actions` and `retained_model_reservations` distinguish verified
unused actions from still-unsettled model reservations. These are counts, not
spending totals; known actual model usage and snapshot charges are separate.
The read fails closed if a reported release proof cannot be verified.

Leave the mapping unset to disable this canonical endpoint. The immutable
directory is shared by the HTTP and accounting boundaries; an actor issued by
another directory is rejected. Credential/grant changes require restart. Use
HTTPS or a trusted local transport; do not expose bearer credentials over public
plain HTTP. This does not upgrade legacy approval routes into canonical authority.

### Cancellation Commands

The CLI and terminal chat use the same canonical endpoints and validated receipts:

```sh
export REVIEW_OPERATOR_TOKEN="$ALICE_OPERATOR_TOKEN"
7review attempt <exact-attempt-id> --server http://localhost:8080 --json
7review cancel-attempt <exact-attempt-id> --command-id <stable-command-id> --expected-version <observed-version> --revision <exact-analysis-revision> --reason "operator requested stop" --server http://localhost:8080 --json
7review authorize-publication <exact-attempt-id> --command-id <stable-command-id> --expected-version <observed-version> --revision <exact-analysis-revision> --assessment-digest <validated-assessment-sha256> --artifact-digest <exact-draft-sha256> --provider github --host github.com --repository-id <owner/repo> --change-number <number> --artifact-class assessment --reason "reviewed exact assessment" --server http://localhost:8080 --json
```

Use the exact `attempt_id` returned by canonical intake. `7review review` now
prints it separately from the run ID when the service returns durable admission.
Legacy run aliases are not resolved by these controls. Inspect the attempt first;
cancellation requires all command/version/revision/reason fields and never fetches
or silently substitutes a newer target. Duplicate/unknown flags, missing values,
extra positionals and ambiguous numeric versions are rejected before network I/O.
Use assignment syntax for a value beginning with a hyphen.

The credential must already be mapped by the server. There is no fallback to
`REVIEW_API_TOKEN`, token flag, request-body actor or implicit role. The client
requires HTTPS for remote hosts; HTTP is accepted only for literal loopback
addresses or `localhost`. Redirects are never followed. Responses are bounded,
closed-contract validated and checked against the submitted command before
success is printed. Arbitrary server error bodies and transport details are not
echoed, so credential reflection cannot become terminal output.

A 409 exposes only a validated current-attempt reference; the client does not
follow that reference, retry or retarget. A transport failure, unsupported success
response or 5xx may follow a committed operator command. Retry the identical
command ID and payload with the same mapped principal after inspection, not a
newly generated command. A cancellation receipt reports the original disposition;
query the attempt again for current release/retained counters.

In chat/TUI the commands are `/attempt <exact-attempt-id>`,
`/cancel-attempt <exact-attempt-id> --command-id ... --expected-version ... --revision ... --reason "..."`,
and `/authorize-publication` with the same exact fields as the CLI.
They do not use the selected legacy run, and cannot override the terminal's server
using command flags. Interrupting a console HTTP request does not assert backend
rollback: the console waits for the request outcome and preserves the unconfirmed
result. Late events from older model streams cannot overwrite the operator
request or its receipt. Existing dashboard/legacy commands still use their
separate `REVIEW_API_TOKEN` where configured.

Send `POST /operator/commands` with the mapped credential and
`Content-Type: application/json`. The implemented command kinds are
`cancel_attempt` and `authorize_publication`; each closed envelope is bounded to
8 KiB. Publication authorization records disclosure authority only: it does not
enqueue or perform provider delivery, merge, notification, or memory activation.

Cancellation envelope:

```json
{"schema_version":1,"command_id":"cancel-unique-id","attempt_id":"exact-attempt-id","expected_version":1,"revision":"exact-analysis-revision","kind":"cancel_attempt","payload":{"reason":"operator requested stop"}}
```

Use the exact attempt ID, analysis revision and control version obtained from the
scoped attempt endpoint. A reviewer/operator role for that exact repository is
required. There is no request-body actor field, query override or run-alias
resolution. An authenticated actor without the role gets 403. Stale revision,
control version or currentness returns 409 with a scoped current-attempt link;
the service does not silently retarget the command. Missing/incompatible storage
or corrupt durable evidence fails closed, never as a successful cancellation.
Audit reason/revision text must be bounded valid UTF-8 without NUL or replacement
characters; malformed surrogate escapes are rejected instead of silently decoded.

One transaction commits the actor-bound command and immutable checksummed receipt,
advances the control version, disables the original job's dispatch, revokes its
claim, marks its inbox cancelled and closes the attempt. It locks in the same
job/head/attempt order as dispatch. A stale worker cannot begin or reclaim a
prepared action, publish a model terminal disposition or finish that cancelled
inbox. Cancellation of expired/recovered work does not extend its deadline.

Reuse the same command ID and exact payload after a timeout/lost response. The ID
is unique within the host boundary/project and bound to the authenticated actor;
same input returns the original receipt, including after process restart.
Different payload or actor conflicts. Every retry must still authenticate and
pass repository authorization. Client death during COMMIT is not proof of
rollback; exact-command replay reconciles either outcome.

The cancellation receipt reports `reservation_disposition: retained` for that
transaction. It remains immutable if maintenance later proves unused capacity.
Lease and positive sending evidence are preserved. Sending may have won the race;
cancellation cannot retract that provider request or infer zero usage. Late
attributable responses and qualified final usage settlement remain recordable,
but cannot revive dispatch, continuation or terminal model publication.

### Verified Unused Release

Before prepared-action recovery or fresh inbox claiming, bounded maintenance
examines cancelled prepared actions. It does not replay jobs or invoke providers.
Each release requires the original actor-bound cancellation audit, closed attempt
and revoked job claim, exact immutable action/checkpoint/binding and frozen route
qualification, coherent lease epoch with no sending marker, complete three-scope
reservation provenance, and no outcome, settlement, integrity incident,
quarantined usage claim or indexed external result.

A transaction commits one checksummed local proof and three scope audit links,
marks that action `not_executed`, and advances its job control version. Original
reservation rows, periods, request/quote bytes and lease evidence remain intact.
Budget reads treat the exact reserved upper amount as unused only after verifying
the proof and its attribution; there is no forged zero-usage provider receipt or
worker `UsageKnown` assertion. Missing/damaged proof, scope provenance or lease
evidence blocks spending rather than silently removing a charge. Both original
and release-audit scope links participate in discovery and incident fencing.

Duplicate workers, restart and exact-action replay cannot credit twice. A failed
commit exposes no proof or effective release. Unknown commit acknowledgment is
reconciled by the same immutable action ID. Cleanup uses frozen evidence, not a
new live provider catalog, current operator credentials or a renewed deadline;
the cancellation already authorized stopping that work. Historical cleanup may
outlive currentness. It grants no dispatch, continuation or publication rights.

Sending, uncertain, recorded and settled work is not released by this path.
Other model reservations and committed snapshot charges are unchanged. A later
qualified usage receipt contradicting a not-executed proof is quarantined and
records an integrity incident that blocks all three scopes across periods; it
does not overwrite the proof or become an ordinary model settlement. If the
release journal is unavailable, the worker does not fall through to new dispatch.

This covers authenticated cancellation's proven-unused preparations, not arbitrary
refunds or incident corrections. Superseded-only work, other canonical human
commands and broader operational/provider qualification
remain open. It is not a completed control lifecycle or alpha gate.

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

Schema v13 commits a bounded ordered membership seal and job binding in the same
transaction. It binds each original immutable intent and the common checkpoint
group. Exact retries verify that seal; a complete but unsealed legacy set is not
silently adopted. Further reservations outside the sealed set are denied, except
the exact confined formatter child justified by a checked parent reduction.
Every claim/Begin rechecks all sealed slots, including frozen source, validation
and original job epoch. Missing/duplicated slots, changed artifacts or a detached
job binding fail closed. Terminal projection also rechecks the seal. Normal
current-epoch replacement during reclaim does not rewrite immutable intents.

Recovery can now move the action-confined cursor through this sealed set in slot
order. First takeover revokes the original live job token; subsequent slots retain
that recovery epoch while action fences revoke expired workers. It checks every
slot: all prepared leases must be unowned/expired; known results must retain intact
response and sending-fence provenance; sending, uncertain, foreign or unsealed
progress blocks further dispatch. No new action or whole-job takeover is granted.
The automatic scanner exposes only the first unfinished eligible slot.

Mixed live/recovery results may finish only when the original token is proven by
the seal, and the final cursor's sending lease binds the current recovery epoch.
No arbitrary stale token is accepted. Finalization retains all unknown-usage holds.
Process exits at claim/sending/recorded boundaries and competing recovery workers
exercise these transitions; deferred claim commit failure preserves the original
live token, null cursor and fencing state.
Action-only recovery tokens cannot call `FinishInbox` to finish or fail the whole
job; terminal completion must use the checked reduction proof instead.
The same restriction applies immediately when a batch seal commits, before any
takeover: a lost acknowledgement or invalid returned receipt cannot let the old
live frame strand an intact stored plan by marking the inbox failed/completed.

Canonical durable-service `CompleteParallel` calls with multiple batches now use
this API before any provider call. The planner checks that the ordered partition
exactly matches the frozen diff, and pins provider/model/output bounds from the
checkpoint's routing snapshot. It uses the same host/policy ceiling preflight as
individual dispatch. Every request is built before the atomic prepare operation;
changed live source, unavailable atomic authority, invalid receipts or failed
commit fail closed without individual-call fallback. Tool-schema JSON numbers
retain their exact value through argument cloning.
Prompt builders cannot mutate the validated source or ordered partition.

After a checked commit the live frame returns the durable-handoff result. Inbox
workers execute the sealed slots through the existing sequential recovery cursor;
the old frame neither sends calls nor finishes the job. Recovery does not reselect
routes from later configuration. Paused-after-commit overlap, restart with no SCM
or policy reader, formatter repair and the normal HTTP-to-worker journey are
covered with controlled providers and PostgreSQL. Single-batch and ephemeral
execution keep their existing paths. The legacy parallel dispatcher remains only
on the latter paths; canonical multi-batch I/O is action-confined and sequential.

This is not complete general continuation: tool-request results persist as
`needs_tools`, without fabricated observations, additional model calls, terminal
draft or publication. Authenticated operator controls remain open. Unsealed
legacy siblings remain ineligible after
external progress.

Snapshot observation sets now expose a bounded `SnapshotUsage` calculation.
Every ordered logical proposal evaluation counts as one `ToolCalls`, including
denied, failed and duplicate proposals. `ObservationBytes` is the sum of the
JSON-encoded observation records, including attribution/status/reason overhead
and JSON escaping, not just result text. The calculation returns no partial
usage when any member fails shape, scope or attribution checks. The six-tool
snapshot vocabulary is shared with the renderer; foreign-run/unsupported inputs
and policy/skill restrictions cannot be bypassed by reporting a successful read.
Truncation must retain its canonical incomplete envelope and cannot be `ok`.

The calculation alone is not independent provenance verification, a durable
receipt, capacity admission or a continuation grant.
Model/token/money/active-time dimensions remain zero because no provider bill or
active-time measurement is inferred from snapshot data. General local CPU and
external tool accounting remain unqualified.

`CommitModelToolObservations` now performs the durable acceptance against a
verified stored pending parent. It verifies the complete job lineage, current
job/head/attempt, positive sending fences and all three original model holds,
then uses the existing attempt/change/project locks. Tool-call capacity is checked
before pure rendering; exact encoded-byte capacity is checked before acceptance.
One transaction stores the immutable private receipt and all scope links, rotates
the job token and confines it to the already-recorded parent. The public artifact
contains no job token or dispatch permit. No model hold is settled or released.
Recovered sealed groups must retain their action cursor: a missing cursor cannot
be guessed from recorded results. Intact original, recovered and mixed-epoch
groups retain their proven provenance.

Receipt identity is deterministic per parent/renderer version. Duplicate workers,
restarts and lost acknowledgements converge on the same stored data, without
re-rendering accepted observations or reacquiring job authority. Historical reads
verify parent evidence, sending epochs, frozen authority and scope attribution;
they do not reopen a closed/expired/superseded attempt. Pure rendering interrupted
before acceptance may repeat, but its transaction commits no charge or owner
change. Charges count accepted logical evaluations, not physical local CPU retries.

Every subsequent model or observation capacity check includes the stored local
charges. Missing/moved links cannot hide a receipt from its hashed scope IDs;
changed receipt digests, amounts or link attribution fail closed. Prepared claim
and sending checks also assign the current shared period using fresh database
time after route locking. Known snapshot charges count only in their recorded
origin periods; unknown model holds continue across renewals. Snapshot accounting
time is read inside the locked transaction after rendering, rather than retaining
a pre-render period.

Automatic observation/child creation and canonical live tool handoff are now
wired through the protocol below. This is not complete external tool/CPU
accounting, authenticated operator control, cancellation/refund or operational
qualification. Explicit schema v15 is required; ceilings and alpha remain unfinished.

### Guarded Tool Children

`ModelToolContinuationV1` builds a reasoner proposal only from a checked parent
and its exact observation set. It retains the ordered raw responses, validation,
next cursor, prior tool history and frozen reasoner route/output ceiling. It
appends the current requests and observations once. Native definitions come from
one closed compiled snapshot registry, intersected with frozen policy and skills;
the authority checks those exact definitions rather than trusting proposed tools.
The builder returns neither action identity, job ownership nor a sending permit.

`AdvanceModelToolContinuation` checks the current committed observation epoch,
parent lineage, job/head/attempt and operator binding. One transaction creates
the deterministic child, reserves its model upper bounds at all three scopes,
records the parent/observation proof and rotates the job into a child-only epoch.
Budget or commit failure leaves the accepted observations and their charges
intact, without a partial child or ownership change. Duplicate creators and
lost commit acknowledgements return the same preparation receipt without
reacquiring ownership or reserving again. Historical receipts grant no I/O.

Prepared tool children use the same bounded recovery scanner, fenced claim and
Begin protocol as formatter children. Every claim and Begin rechecks the exact
current observation cursor and complete model ancestry, including unrelated
already-reduced siblings. Sending actions remain non-retryable. A corrupt older
model outcome also blocks a later formatter repair at both preparation and
Begin; checking only its immediate model parent was insufficient.

Lineage verification understands both typed edge kinds without recursively
replaying the pipeline. All three qualified tool rounds preserve their ordered
history and can reach checked terminal completion. Sealed batch membership stays
immutable; a checked tool child is an explicit nonmember continuation, not a new
batch slot. Local terminal completion still grants no approval/publication or
refund and retains every unknown model hold. The qualified local workflow below
is not alpha readiness or live-provider qualification.

### Automatic Tool Continuation

Bounded inbox workers now scan current qualified `needs_tools` parents, accept
snapshot observations and prepare their exact reasoner children before returning
to fenced prepared-action recovery. The scan checks namespace, active/current
attempt, original deadline, current action/observation epoch and frozen reasoner
routing. It never replays `RunAccepted` or reloads SCM/policy/profile state for a
claimed job. Restart after observation acceptance resumes child creation without
charging or rendering the accepted set again. Pending counters exclude both
formatter and tool parents whose child link has committed.

Canonical live single-batch tool proposals hand off before the legacy tool loop
can append observations or send another model request. Live/recovery overlap
uses the same idempotent observation and child operations. Multi-batch tool plans
follow the worker-only path after sealed initial preparation. Ephemeral/noncanonical
execution keeps its existing path and does not claim this service guarantee.

Once a canonical model result reference exists, handoff/indexing/advancement
errors abandon the old frame with a deferred-continuation signal; they do not
send it through ordinary failed/completed job termination. `FinishInbox` now
refuses ordinary completion whenever any model reservation exists, including an
unsent committed preparation with a lost acknowledgement. Terminal model proof
or a future authenticated disposition is required. Jobs with no model reservation
can still finish before any effect. Historical legacy terminal rows remain
non-dispatchable. Neither this barrier nor any failure refunds a hold.

Known capacity exhaustion is typed separately from storage/integrity failures.
Pending tool scans and pre-send prepared claim/Begin checks defer such waits,
rotating only the existing job scheduling timestamp so bounded oldest-first
scans can progress past them. Other inboxes and continuations remain runnable
within their own ceilings. No scheduling hint clears or extends a token, lease,
state, deadline, scope origin or reservation. Already committed fenced claims
retain their existing leases and normal expiry rules. Failed scheduling storage
propagates rather than enabling a local allowance. Errors after sending, including provider errors
that resemble quota exhaustion, never use this deferral path. Waits remain
bounded by worker batches and the original execution deadline; authenticated
waiting/disposition controls and richer resource-state reporting remain open.

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

## Snapshot Tool Observations

The six current reasoner tools read only captured `Source` data; they do not fetch
fresh SCM data or call the public operator-tool executor. `ModelToolObserverV1`
renders a detached, deterministic `ModelToolObservations` set for an already
verified `needs_tools` parent. It binds run/attempt, parent group/input digests and
the complete ordered normalized request list. It rechecks response digests and
re-parses the current group's recorded responses before rendering, including
rejecting older partial plans that silently omitted an over-bound proposal.

Missing/invalid parent identity, cursor, validation, diff, input references or
interruption return no partial set. Snapshot policy and skill permissions remain
required. Tool input permits only an optional `run` string matching the active
snapshot; foreign runs, wrong types and unsupported fields produce denials, not
successful reads of a different active context. Missing data is an error, distinct
from a captured empty diff. Large results have status `truncated`, with bounded,
valid JSON containing original byte length and a UTF-8-safe incomplete preview.
They are not counted as successful tool evidence.

V1 data bounds are five requests, 128-byte names, 1024-byte reasons and 4096-byte
inputs; result text is at most 4096 bytes, each encoded observation at most 12 KiB,
and the full observation set at most 128 KiB. The model parser rejects malformed
or over-bound plans instead of dropping blank names or truncating the list.

These are data bounds, not financial/resource qualification. The adapter does not
persist observations, reserve/settle tool usage, read current permission state,
advance the job, issue model calls, approve or publish. Its caller must verify
durable parent proof, currentness and accounting before committing observations
and authorizing a child. The canonical worker still leaves tool plans pending.
This adapter does not re-qualify existing stored terminal assessments.
Durable observation accounting/commit, guarded reasoner continuation, authenticated
operator controls and safe cancellation/refund remain open. Schema stays v13.

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

The service accepts these records only through the trusted host variable
`ACCOUNTING_ROUTE_BOUNDS_JSON`. It is a closed schema-v1 document; duplicate or
unknown fields, duplicate provider/model pairs, invalid time windows, unbounded
output and unsafe numeric bounds fail startup. Setting it without the complete
accounting database/boundary/project authority also fails startup. For example:

```json
{"schema_version":1,"routes":[{"provider":"openai_compat","model":"pinned-model-id","version":"provider-contract-2026-11","currency":"USD","evidence_ref":"operator:contract-42","valid_from":"2026-11-01T00:00:00Z","valid_until":"2026-12-01T00:00:00Z","input_hard_limit":100000,"output_hard_limit":4096,"input_micro_per_million":1000,"output_micro_per_million":2000,"fixed_micro":0,"all_output_bounded":true,"max_active_ms":30000,"native_tools_qualified":false}]}
```

This is authority to reserve and send within that exact contract, not evidence of
actual usage. Until a trusted receipt verifier is configured, provider outcomes
remain unknown and retain the full reservation. Operators must not qualify model
aliases, proxies with mutable routing, or contracts where `max_tokens` does not
bound every billable output token.

### Authenticated Usage Settlement

An authenticated repository operator can reconcile one exact model action after
obtaining final usage from an external bill or other qualified evidence:

```sh
export REVIEW_OPERATOR_TOKEN="$ALICE_OPERATOR_TOKEN"
7review actions <exact-attempt-id> --server http://localhost:8080 --json
7review settle-usage <exact-action-id> --command-id <stable-command-id> --input-tokens <final-input> --output-tokens <final-output> --money-micro <final-cost> --active-ms <final-active-time> --evidence-id <stable-evidence-id> --reason "reconciled provider invoice" --server http://localhost:8080 --json
```

The command supplies only final usage, evidence identity and reason. Actor,
repository, provider, model, request digest, route version and currency come from
the authenticated principal and durable action. The ledger requires the
repository `operator` role and rejects participants, foreign repositories and
unknown actions before changing accounting state. Exact command replay is
idempotent; reusing a command ID with changed bytes conflicts. The CLI rejects
missing, duplicate, negative, non-canonical or overflowing amounts before network
I/O, and validates the returned command digest, actor-derived evidence reference
and exact usage before printing success. Terminal use is `/settle-usage` with the
same fields; it cannot substitute the selected run or override the pinned server.

Usage at or below the reserved upper bound settles the action and releases only
the proven difference. Usage above any bound records the settlement and a durable
integrity incident, then blocks further spending; it is not silently clipped.
This control does not cancel an in-flight provider request, prove that a prepared
action was unsent, or create a refund. Those release paths require their own
durable evidence. It is also not a production receipt-verifier adapter: the
operator remains responsible for qualifying the external evidence.

`GET /operator/actions?attempt_id=<exact-attempt-id>`, the `actions` CLI command,
and `/actions` in the TUI expose the discovery path. They require the exact
repository `operator` role; reviewer and participant roles are insufficient. The
closed response is capped at 256 actions and contains lifecycle state,
provider/model/role/step, route version/currency, reserved upper usage, optional
validated actual usage/receipt/evidence, integrity status and creation time. It
revalidates the canonical attempt binding, request and route digests, exact upper
bound, final receipt and any `not_executed` release proof before returning the
whole list. Overflow or corruption fails closed instead of returning a partial
view. Payloads, prompts, request digests, job/claim tokens, workers, leases and
fences are never exposed. This projection is status only and grants no dispatch,
settlement, cancellation or refund authority.

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

- Production receipt-verifier adapters and live provider evidence qualification;
  authenticated exact-action discovery and settlement exist, but they do not
  qualify external evidence automatically.
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
