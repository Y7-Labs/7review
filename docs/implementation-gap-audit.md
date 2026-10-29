# Target Implementation Gap Audit

Date: 2026-10-28.
Audited revision: `462f97c` (clean checkout before this report).
Authority: accepted R01-R23, ENG-D1-D13, SPEC contracts and ROADMAP exit gates.
Scope: implementation and verification audit, not a new product design.

This is the point-in-time baseline. Subsequent verified slices are tracked in
[Policy Execution Progress](policy-execution-progress.md); they do not close
an entire work package merely by changing one part of its implementation.

## Finding

The running Docker server is the legacy review baseline with selected target
foundations. It is not the accepted adaptive review system. Passing its existing
tests, opening the TUI or obtaining healthy containers does not close the target
requirements.

The principal missing chain is:

```text
immutable attempt + durable intake
  -> transactional action/budget authority
  -> triage and governed adaptive scheduler
  -> scoped questions and revision-aware continuation
  -> immutable assessment and policy gate
  -> independently authorized, reconciled publication
  -> separately governed learning
```

There is no Drive/document-management component in this target. A web dashboard
is not a completion prerequisite; CLI/TUI and native SCM interfaces already belong
to the design. More channels or a replacement workflow framework are not required.

## Audit Method And Evidence

Read the actual entry points and call sites, not only type definitions or status
documents. Inspect tests against their assertions, not only names. Separate
service execution from the ephemeral CI runner.

Fresh `GOCACHE=/tmp/7review-go-cache go test -json ./...` passed: 432 top-level
tests, 15 packages with passing tests, zero failures; the LLM interface package
has no tests. These are baseline/partial-target receipts, not a product completion
percentage. Raw execution output was recorded at
`/tmp/7review-gap-audit-tests.jsonl`.

Nine explicitly named `TestScenario_...` functions cover eight distinct scenario
IDs: S06, S22, S26, S45, S48, S49, S50, S52. Their scope is described below.
52 scenario IDs have no explicitly named scenario function. Existing untagged
tests cover some related behavior and must be reused; absence of a name alone is
not proof that all behavior is absent.

No explicit ENG-P/S/A/R/L/B/C/DUR/F case identifiers were found in Go tests.
The pure transition-table test and other untagged tests provide foundations,
but not execution of the 50 engineering scenario contracts.

The audit did not run paid models, publish a report, alter credentials or start
new deployments. Previous local Docker/sidecar/formatter checks remain narrower
evidence recorded in [Local Alpha](local-alpha.md).

## What Exists And Should Be Reused

| Implemented foundation | Actual scope | Evidence |
| --- | --- | --- |
| Canonical review data and target domain types | Source ownership, cloning, identity/state/check/assessment/gate validation; target state reducer not used by the service pipeline | [domain](../agent/review/domain.go), [context constructors](../agent/review/context.go), domain tests |
| Snapshot and test-proof provenance | SCM attestations, revision-bound test readiness, frozen Git base/head/merge comparisons | [intake](../agent/review/intake.go), [frozen Git](../agent/tools/local_git.go), [attestation](../agent/review/scm_attestation.go) |
| Trusted policy V2 | Strict JSON/YAML, base binding, deterministic pack composition, delegated replacement, capability/ceiling validation, trigger selection and explain | [policy compiler](../agent/policy/compile.go), [SCM admission](../agent/pipeline/trusted_policy.go), [policy CLI](../cmd/7review/policy_command.go) |
| Deterministic quality gate | Coverage/violation precedence, advisory/blocking exits and baseline compatibility | [gate](../agent/policy/gate.go), [CI bridge](../agent/pipeline/ci_gate.go) |
| Artifact-only CI runner | Frozen analysis tree, verified quality import, fork model isolation, bounded execution, atomic output artifacts | [runner](../agent/ci/runner.go), [export](../agent/ci/export.go), CI tests |
| Existing review workflow | SCM enrichment, corpus selection, skill loading, model/provider fallbacks, bounded read-only tool rounds, finding validation, draft/final report publication | [pipeline](../agent/pipeline/pipeline.go), tools/orchestrator packages |
| Operator surfaces | Authenticated API, CLI, interactive TUI, streaming chat and run inspection | [CLI](../cmd/7review/main.go), [TUI](../cmd/7review/tui.go), [run API](../agent/app/runs.go) |
| Channels and local packaging | Authenticated Telegram/Twilio callbacks, SimpleX transport, in-process callback deduplication, isolated Compose deployment | channel/app tests, [local guide](local-alpha.md) |

## Missing Work Packages

Each package below has an observable exit. Package boundaries are implementation
choices; the contracts and dependencies are not.

### G01: Immutable Attempts And Full Identity

Status: domain types exist; service execution is not migrated.
Requirements: R01, R02, R14, R18; SPEC 1/5/15/22.

- Replace change-key run ownership with unique attempt and work-intent IDs.
- Include provider host and stable repository identity in the change key.
- Persist generation, predecessor and version; keep terminal attempts immutable.
- Maintain the current automatic pointer over head/manifest, base, policy digest
  and generation; historical work must have a separate intent.
- Coalesce new automatic revisions, fence old work and retain historical results.
- Deduplicate delivery by provider/host/repository/ID and reject same-ID,
  different-payload digests, rather than treating all repeated IDs as identical.

Evidence: `requestRunID` in [support](../agent/pipeline/support.go) returns
`ProjectID + "!" + ChangeID`; both stores' `Start` reuse/overwrite that key.
`Pipeline.Run` calls legacy `review.NewContext`. `CanTransitionAttempt` is not
called by service execution. Existing callback deduplication is in-memory only.

Exit: S12/S13/S24/S25/S28/S39/S47 and ENG-R/L variants assert immutable history,
correct currentness and rejection of stale/ambiguous updates.

### G02: Durable Intake, Jobs And Action Journal

Status: missing coordinated implementation.
Requirements: R10, R13, R14, R17; SPEC 10/14/15.

- Add the approved PostgreSQL authority/journal, migrations and startup wiring.
- Atomically commit inbox receipt, attempt/event state and pending jobs before
  service acknowledgement.
- Implement prepare/claim/begin-send/record-receipt/reduce/reconcile operations,
  immutable request payload references, leases, fencing tokens and dependency epochs.
- Persist uncertain remote execution and quarantine mismatched receipts.
- Recover prepared work and replay recorded local reductions without repeating
  remote inference; ownership expiry must not authorize blind resend.
- Classify disk/database outages and corruption, preserve partial evidence,
  and expose recovery actions.
- Implement actual graceful intake/worker shutdown and a documented restart path.

Evidence: [workers](../agent/app/workers.go) use an in-memory channel;
[enqueueReview](../agent/app/routes.go) acknowledges queued closures, not a durable
inbox transaction. [FileRunStore](../agent/pipeline/support.go) uses JSON
write/rename and separate updates; there is no shared transactional journal or
PostgreSQL driver/runtime service. Persisted run JSON is not durable queued work.

Exit: real database/process-crash tests S15-S17/S35/S43 and all ENG-DUR cases.
Kill processes at each specified boundary; verify receipts and actual action counts.

### G03: Enforced Hierarchical Budgets

Status: limit schema exists; accounting/dispatch enforcement is missing.
Requirements: R05, R10, R14; SPEC 6/10; ENG-D6-D11.

- Enforce model/tool calls, input/output/reasoning tokens, money, active time and
  observation bytes at attempt/change/project scope.
- Reserve input plus maximum billable output/tool charges atomically before every
  primary, fallback, repair and newly billable retry.
- Add bounded provider/model pricing, actual usage receipts and idempotent settlement.
- Implement fixed-period origin attribution and unresolved holds across renewal,
  cancellation, lease expiry and replacement work.
- Reject authority outages, missing price bounds and over-admission; record
  over-bound actual usage as an integrity incident.
- Wire operator ceilings into service/CI admission and enforce them at execution,
  not merely while parsing configuration.

Evidence update (2026-10-28): trusted host ceilings now reach service/CI admission
and bind effective identity. [Dispatch guard](../agent/orchestrator/budget.go)
requires reservation before bounded model I/O, with terminal denial and unknown
usage retention. The [PostgreSQL authority](../agent/accounting/postgres.go) now
has real-DB concurrency, rollback, worker-death, receipt settlement and integrity
tests. Conservative exact-route token/price bounds have no live-model defaults;
receipt verifier adapters are not implemented. Canonical worker/CI intake now
connects through opt-in runtime settings, with durable generation fencing and
pre-dispatch identity checkpoints. Default production routes remain fail-closed.
Durable HTTP acceptance and unclaimed pending-work recovery now have PostgreSQL
fault/replay coverage. Claimed crashes are never automatically retried. Full
action recovery/ownership/provider currentness,
non-model resource accounting, prospective reconfiguration,
operational recovery and the complete real-DB release exit remain open.
The pipeline still uses a constant three read-tool rounds, not the target budget
controller. See [red/green ledger](policy-execution-progress.md), slices 16-30.

Exit: S21/S31/S42, ENG-B01-B06/C01-C05/F02, with concurrent real-DB admission and
faults around usage settlement.

### G04: Policy Methods Must Control Execution

Status: compiler/admission are implemented; method execution is partial.
Requirements: R03, R04, R07, R10, R17, R23; SPEC 3/20.

- Resolve registered builtin/repository method IDs and load referenced method,
  rule and pack contents from the attested base, with path/symlink confinement.
- Make effective methods, capability intersection, protected checks, independent
  review and publication constraints govern the actual action plan.
- Preserve scoped conflicts and unresolved obligations while permitting unrelated
  checks; implement the accepted replacement/precedence laws end to end.
- Resolve the specification's inheritance/reference representation explicitly:
  prose requires inheritance expansion, but current MethodPackV2 has no inheritance
  fields. Do not invent an incompatible schema silently.
- Complete schema boundary fixtures and unavailable-capability/resource preview.

Evidence: [projectTrustedPolicy](../agent/pipeline/trusted_policy.go) records method
IDs and flags. [mergeEffectivePolicyActivation](../agent/pipeline/pipeline.go)
adds required checks to a synthetic activation but does not load policy method
content. Existing skills are selected using the separate loader/profile.
No scheduler consumes `IndependentReview`; model tools are restricted by skill
surfaces rather than an action-level effective-policy capability decision.

Exit: S05/S08/S22/S33/S36/S41 plus ENG-F05/F06, including hostile head methods,
conflicting peers, required checks and explicit replacement.

### G05: Triage And Adaptive Investigation Controller

Status: missing target controller; legacy batched review/tool loop exists.
Requirements: R05-R11, R19; SPEC 4-7/21; ENG-D1-D5/D9.

- Perform cheap risk/readiness triage before semantic retrieval/model review.
- Build check/hypothesis-scoped actions with expected gain, immutable inputs and bounds.
- Implement pure eligibility, protected priority allocation, stable ordering and
  action-specific result reduction.
- Track evidence-linked progress and semantic novelty; rephrasing/confidence
  changes must not count as progress.
- Suspend stagnant lines, continue independent work and resume only after relevant
  new evidence and fresh eligibility checks.
- Execute a genuinely independent high-risk omission pass; distinguish it from
  candidate verification and record actual resolved models/diversity gaps.
- Produce partial/complete assessments, explicit stop reasons and unknown coverage;
  zero findings must not imply completion.
- Schedule waits without occupying a worker; enforce deadlines/cancellation.

Evidence: [Pipeline.Run/runReview](../agent/pipeline/pipeline.go) select corpus,
recall memory, reduce context, batch model review and run up to three tool rounds.
There is no runtime per-line progress, stagnation, priority reservation or scoped
dependency controller. The target transition helper is tested as a pure type rule.

Exit: S01-S04/S07-S12/S19-S23/S31/S38, ENG-P/S/A/R/L and F01/F04, with fake clocks,
recorded observations and deterministic concurrency barriers.

### G06: Scoped Human Commands And Authorization

Status: legacy operator/channel commands exist; target command protocol is missing.
Requirements: R11, R12, R17, R19; SPEC 8/15/I04.

- Map authenticated actors to repository roles; a shared operator token is not
  an attributable identity/permission system.
- Implement command ID, expected version, snapshot and subject binding with
  idempotent receipts and conflict responses.
- Add persistent scoped questions, intended recipient, deadlines and answer/dispute
  operations that resume only affected work.
- Separate cancellation, automatic pause/resume, full/incremental investigation,
  publication authorization and memory activation.
- Route editorial changes and factual disputes differently; preserve original
  findings/evidence and attributed dispositions.
- Apply the same protocol to CLI/TUI, HTTP, channels and native SCM conversations.

Evidence: [auth](../agent/app/auth.go) checks a global bearer token.
[approve handlers](../agent/app/publish_handlers.go) and channel commands bind
run ID/text, not an immutable assessment digest/version/role.
`ReviseDraft` rewrites a report; `RerunReview` calls the complete pipeline again.
Question findings/chat transcripts are not durable dependency questions.

Exit: S11/S13/S14/S26/S31/S56/S57 and ENG-R06 with authorization/race/replay tests.

### G07: Independent Publication, Grants And Reconciliation

Status: comment adapters/markers exist; target effects contract is missing.
Requirements: R12-R14/R17; SPEC 9/21/24.

- Create immutable delivery operations keyed by destination, attempt, assessment
  version, payload digest and pipeline/change context.
- Add separate revocable artifact-class grants and manual authorizations.
- Implement pending-authorization/pending/sending/delivered/uncertain/failed/obsolete
  state transitions and a durable outbox.
- Check the complete currentness/authority tuple before send and after receipt.
- Reconcile timeouts and partial batches by exact remote ID/marker/digest; do not
  blindly retry an ambiguous external effect.
- Keep notifications/index writes independent from assessment/publication status;
  failed notification must not turn a completed review into failed investigation.

Evidence: [ApproveRun](../agent/pipeline/pipeline.go) publishes, notifies, writes
memory and finalizes in one procedural chain; notification/memory errors mark the
run failed. `publishFinal` refreshes SCM only when cached SCM is nil.
[GitHub](../agent/tools/github.go) and [GitLab](../agent/tools/gitlab.go) search
kind-level bot markers, then update/post; they do not implement versioned outbox
operations or full freshness fencing.

Exit: S16/S27/S30/S32/S39/S41/S44/S47/S51/S60 and actual crash/timeout/batch races.

### G08: Installed GitHub/GitLab Reviewer Lifecycle

Status: webhook/manual review and basic comments exist; rich installation is missing.
Requirements: R01-R04/R11-R14/R17/R22; SPEC I01-I08.

- Implement GitHub App selected-repository installation, expiring installation
  tokens, capability inventory and test-delivery validation.
- Add GitLab dedicated identity/project validation and endpoint permission bounds.
- Persist unconfigured/validating/enabled/suspended/revoked connection states.
- Handle mentions/finding replies, CI prerequisite events, metadata changes,
  close/merge and installation/access removal.
- Add bot-loop prevention, pause/full/incremental semantics, finding continuity
  and authoritative revision lookup/coalescing.
- Declare and qualify enterprise/API-version/merge-queue limitations.

Evidence: [GitHub webhook](../agent/app/github.go) accepts selected pull-request
actions only. [GitLab webhook](../agent/app/gitlab.go) handles merge-request
events. Existing API clients hold static configured tokens; there is no
installation/token-refresh or native conversation dispatch path.

Exit: S53-S60, both provider fixtures plus controlled hosted installation,
revocation, comments, follow-up revisions and permission failures.

### G09: Native Checks, Code Quality And Coordinated CI

Status: autonomous CI artifact-only execution exists; coordinated outputs are missing.
Requirements: R13/R21-R23; SPEC 16/24.

- Add service handoff with a durable receipt, evaluate-and-await semantics and no
  false pass from a pending handoff.
- Wire CI evidence/gate results into service attempts, not only the isolated runner.
- Publish GitHub Checks/annotations or explicitly selected status-only fallback.
- Publish GitLab pipeline-bound status and schema-valid Code Quality export with
  stable fingerprints and report-only exclusion counts.
- Track annotation batches, permission failures and stale-generation reconciliation.
- Bind explicitly required prerequisites to verified provider metadata or trusted
  operator configuration; add complete no-metadata/cycle negative paths.
- Add baseline acquisition/CLI wiring: baseline compatibility exists but the
  current CLI/runner supplies no compatible baseline to the evaluator.
- Qualify hosted examples. They require a project-specific `scripts/ci-quality`
  producer; this repository does not ship that producer or native format converters.

Evidence: [runner](../agent/ci/runner.go) deliberately disables publishers/channels
and calls the legacy review pipeline in artifact mode.
[dependencies](../agent/ci/dependencies.go) validate an optional caller-supplied
graph; no provider graph acquisition is implemented.
[exports](../agent/ci/export.go) produce canonical files, not native check/status
delivery. No baseline flag is available in [CI CLI](../cmd/7review/ci_command.go).

Exit: remaining S45 variants and S46-S52/S58-S60, with service receipts and native
provider output receipts. Preserve the working artifact-only mode.

### G10: Private Local Dirty-Workspace Review

Status: frozen committed Git/CI primitives exist; the user journey is missing.
Requirements: R01/R02/R06/R18/R21; SPEC 1/2/J01.

- Add a private local entry path for staged/unstaged/untracked input and an
  immutable local manifest, explicit comparison/workspace identity and trust source.
- Freeze content before execution; later workspace changes must not alter the attempt.
- Use the same target controller/assessment contracts without SCM credentials or
  mandatory sidecars; disable external publication by default.
- Define local accounting/storage guarantees and expose differences from coordinated mode.

Evidence: [review CLI](../cmd/7review/main.go) accepts GitHub/GitLab review targets;
[CI environment](../agent/ci/environment.go) requires GitHub/GitLab identities.
[FrozenGitSCM](../agent/tools/local_git.go) freezes committed Git revisions, not
the general dirty-workspace journey described in the target.

Exit: J01, S25 and relevant S01/S09/S24 cases with working-tree mutation barriers.

### G11: Evidence Graph, Invariants And Beyond-Diff Acquisition

Status: document graph/citations exist; run-level proof graph is missing.
Requirements: R07-R09/R15-R17/R19; SPEC 7/17.

- Connect required checks, methods, observations, hypotheses, citations, findings,
  human decisions, memory and gates in a bounded run-scoped evidence projection.
- Persist canonical artifact references and rebuild derived indexes without a
  second authority; enforce scope/revision/dependency/access epochs.
- Validate concrete violation/invariant proof paths, not only citation keywords
  and changed-line eligibility.
- Add bounded repository-file/symbol/caller acquisition for open checks; optional
  code indexes must attest revision/configuration/coverage.
- Retain validated beyond-diff findings in the report even when no inline position
  exists. Do not invent positions or reject all unpositioned confirmed findings.
- Inspect weakened tests and CI gates as dedicated obligations.

Evidence: [CorpusGraph](../agent/pipeline/corpus_graph.go) relates document sections,
not the complete review trajectory. Reasoner tools in
[pipeline](../agent/pipeline/pipeline.go) expose selected/cached SCM/diff/context
surfaces. [DefaultFindingValidator](../agent/pipeline/support.go) rejects confirmed
findings outside changed paths/lines or without positions. Authority/citation
heuristics are not the full typed proof-path contract.

Exit: S02/S04/S05/S08/S09/S20/S38/S40, graph-budget/replay/security fixtures and
paired beyond-diff evaluation. Reuse corpus-selection golden tests.

### G12: Governed Memory And Optional Enrichment

Status: recall/write adapter exists; governance and baseline fallback are missing.
Requirements: R12/R15-R17; SPEC 11/18/21.

- Add provider-neutral typed memory records with repository/domain/module/path
  scope, evidence lineage, versions, expiry, contradictions and access epochs.
- Separate proposal, validation, activation, rejection, revocation and supersession.
- Derive learning from attributed accepted/rejected/revised/no-finding feedback,
  independent from whether publication succeeded.
- Require separate activation authority; final report approval is not memory approval.
- Add secret/instruction scanning, stable deduplication, exact scoped recall,
  semantic-index rehydration and deletion/revocation of derived use.
- Make MemPalace/Headroom optional in the service and setup; unavailable required
  capabilities must remain visible unresolved checks.
- Provide explicit optional code-intelligence capability setup. Specific SCIP/
  CodeQL/Joern integrations are optional choices, not all mandatory dependencies.

Evidence: [MemPalaceStore](../agent/tools/mempalace.go) requires HIL approval and
turns finding titles/final reports into conventions. Memory payloads are string
lists; [bridge](../docker/mempalace-bridge/app.py) persists namespace-wide records
without the target per-record repository/authority lifecycle. Server/config/readiness
still require both sidecars. CI's no-op adapters do not implement exact local memory.

Exit: S18/S19/S26/S27/S34/S37/S40 and lifecycle/isolation/revocation tests.
Compare memory enabled/disabled on adjudicated cases.

### G13: Public Artifacts, Operator Views And Migration

Status: domain structs and legacy/CI DTOs exist; target interface/migration is partial.
Requirements: R12/R18/R19/R21; SPEC 20/22, presentation contract.

- Implement and version complete intake/receipt/assessment/unknown/cost/delivery/
  memory/error schemas, validation and compatibility fixtures.
- Expose attempt/revision/generation, completeness, gate, delivery, costs and
  permitted next actions separately across API, CLI/TUI and native output.
- Add target question/decision/recovery/learning views; retain the existing terminal UI.
- Implement deterministic migration inventory/checksums/dry-run/collision report,
  immutable legacy import, alias mapping and mixed-history reads.
- Qualify drain/backup/cutover/rollback; prohibit dual writers and unsafe downgrades.

Evidence: [runDTO](../agent/app/runs.go) exposes legacy statuses and HIL fields.
[CI artifact DTOs](../agent/ci/export.go) omit parts of the full SPEC 20 envelope
(for example durable intake/intent and complete cost/unknown projections).
`schemas/` contains input-profile and CI-quality schemas only; there is no
target migration command. Recognizing old safe filenames is not the cutover protocol.

Exit: S13/S17/S23/S24/S28/S44/S45, schema conformance and migration process fixtures.

### G14: Product Qualification And Controlled Rollout

Status: deterministic baseline tests/local infrastructure work; target qualification missing.
Requirements: R07/R08/R17/R20-R23; SPEC 12/13/19/23; ROADMAP 7/8.

- Execute every acceptance variant against the relevant target path, not a
  renamed stub or only a type validator.
- Build the real PostgreSQL/process-fault matrix and provider parity tests.
- Produce the adjudicated model-evaluation corpus: at least 120 changes, the
  specified category balance, no project over 20%, untouched 25% validation split.
- Run stochastic cases at least three times per qualified model/version; record
  seeded-defect detection, invalid findings, useful escalation, human effort,
  citation quality, latency and actual/reserved costs.
- Compare the legacy baseline and independent-pass/graph/memory ablations.
- Qualify hosted GitHub/GitLab behavior, deployment backup/restore/recovery,
  resource limits and rollout/rollback with immutable receipts.
- Keep TOON disabled unless the optional optimization meets semantic round-trip,
  exact-tokenizer savings (target 10%) and quality parity gates. Shipping every
  optional optimizer is not required to complete the baseline.

Evidence: corpus/validator examples and live-smoke helpers exist; no representative
qualification dataset, statistical report, target crash harness or full target
rollout receipts were found. The local container/formatter smoke is not that evidence.

Exit: documented SPEC 23 objectives with actual measurements and zero hard-invariant
violations. Model success and green unit tests cannot substitute for these gates.

## R01-R23 Completion Map

Statuses apply to the complete requirement, not its reusable foundations.
No percentage is assigned to composite requirements.

| Requirement | Current status | Work needed to close it |
| --- | --- | --- |
| R01 stable identities | Domain types only in service; legacy keys still authoritative | G01/G02/G07/G10 |
| R02 portable frozen inputs | Implemented for attested SCM/committed CI subsets; dirty local journey missing | G01/G04/G09/G10/G13 |
| R03 team-owned methodology | Policy compiler exists; method-content execution incomplete | G04/G05 |
| R04 explainable composition | Compiler explanation exists; scoped runtime conflict handling incomplete | G04/G05/G11 |
| R05 proportional effort | Legacy corpus heuristics exist; target triage/controller absent | G03/G05 |
| R06 scoped readiness | Validated field types exist; check-scoped planning/waits absent | G05/G06 |
| R07 test/CI integrity | Typed CI imports and review skills exist; target obligation/evaluation evidence incomplete | G04/G05/G11/G14 |
| R08 evidence findings | Existing validator/citations exist; target proof/invariant/beyond-diff contract incomplete | G05/G11/G14 |
| R09 independent scrutiny | Flag can compile; no independent omission-pass runtime | G04/G05/G14 |
| R10 bounded autonomy | Per-call/concurrency/timeout limits exist; shared accounting/controller missing | G02/G03/G05/G09 |
| R11 clarification/resume | Chat and report revision exist; scoped durable question resumption absent | G05/G06/G08 |
| R12 attributable control | Legacy approval exists; role/subject binding and separate authorities absent | G06/G07/G12/G13 |
| R13 trustworthy delivery | Basic provider marker upsert exists; versioned outbox/reconciliation absent | G02/G07/G08/G09 |
| R14 durable recovery | Run JSON persists; accepted jobs/actions/receipts do not have target recovery | G01/G02/G03/G07 |
| R15 governed learning | Semantic recall/write works; record lifecycle/activation/revocation missing | G11/G12/G14 |
| R16 optional enrichment | Artifact CI has no-op sidecars; service baseline still requires enrichments | G10/G11/G12 |
| R17 isolation/confidentiality | Selected provenance/auth/fork checks exist; full actors/scope/epochs/sandbox qualification incomplete | G02/G04/G06/G08/G11/G12/G14 |
| R18 compatible migration | Legacy support exists; deterministic target migration/cutover absent | G01/G10/G13 |
| R19 usable assessment | CLI/TUI/report exists; target completeness/coverage/cost/action views incomplete | G05/G06/G11/G13 |
| R20 measured value | Seed examples exist; representative adjudicated evaluations absent | G14 |
| R21 CI-native execution | Working artifact runner; common adaptive engine/handoff/remaining variants incomplete | G02/G05/G09/G13/G14 |
| R22 native code quality | Basic comments exist; rich checks/status/report/install lifecycle absent | G07/G08/G09/G14 |
| R23 configurable gates | Deterministic evaluator and CI exits implemented; service/native/baseline workflow incomplete | G04/G05/G09/G14 |

## S01-S60 Acceptance Evidence

An entry below is not declared complete merely because an ID appears in tests.
`Named partial` means assertions were read and tested, but some specified
variants/integration boundaries remain unproven. `Primitive/legacy` is useful
existing evidence that must not be relabeled as complete target acceptance.

| Case | Existing evidence | Remaining closure |
| --- | --- | --- |
| S01 docs typo | Legacy docs-small corpus test | Target triage, NA proof, low budget and assessed outcome: G05 |
| S02 auth config | Security skill and generic finding tests | Risk floor plus independent impact investigation: G04/G05/G11 |
| S03 generated dependency | Path filters and corpus tests | Preserve dependency semantics despite generated-text exclusion: G05/G11 |
| S04 weakened assertions | Test-quality skill | Dedicated before/after verification-integrity assertion: G05/G11 |
| S05 self-weakened CI | Trusted-base policy tests | Workflow weakening finding independent of green CI: G04/G11 |
| S06 stale proof | Named partial: readiness rejects h1 for h2 | Controller continues independent work and scopes mandatory wait: G05 |
| S07 missing intent | Readiness types | Nil invariant finding plus only dependent question/wait: G05/G06 |
| S08 contract conflict | Corpus selection and policy validation | Preserve conflicting authority and scoped human resolution: G04/G06/G11 |
| S09 no design corpus | Legacy validator cases | Explicit code-invariant proof path without document authority: G11 |
| S10 pending CI | SCM check ingestion | Durable dependency/wake and no repeated work: G02/G05 |
| S11 clarification | Legacy chat/stream tests | Named question, actor/version validation, scoped resume: G06 |
| S12 new head during wait | Transition-table primitive | Immutable successor, fenced old work and revoked old approval: G01/G05/G07 |
| S13 duplicate command | Domain validation and legacy callback dedup tests | Durable identity/digest conflict and exact command receipts: G01/G02/G06 |
| S14 answer/cancel race | Version-field/transition validation | Concurrent authorized reduction and fencing: G02/G06 |
| S15 accepted-work crash | File persistence tests only | Real process kill after durable acknowledgement: G02 |
| S16 publication crash | Marker-upsert adapter tests | Uncertain outbox/reconciliation across crash: G02/G07 |
| S17 corruption/disk full | File-store validation primitives | Fault recovery, partial evidence and actionable receipts: G02/G13 |
| S18 optional outage | Existing strict reducer-failure test demonstrates legacy failure | Baseline fallback instead of run failure: G12 |
| S19 required unavailable | Compiler rejection and check validation primitives | Scoped unknown check plus unrelated useful work: G04/G05/G12 |
| S20 reviewer disagreement | No target controller | Independent passes, supported disagreement and bounded escalation: G05/G11 |
| S21 empty/exhausted | Model parse/empty-result tests | Budget-backed incomplete coverage despite zero findings: G03/G05 |
| S22 injection | Named partial: head source cannot compile as base policy | Retrieval/prompts/tool suggestions and credential negative assertions: G04/G11 |
| S23 outcome taxonomy | Legacy malformed-output/repair tests | Target refusal/invalid/unsupported outcomes, spend and partial states: G03/G05/G13 |
| S24 provider parity | Domain primitives and legacy GitHub/GitLab lifecycle fixtures | Same target attempts/actions/decisions across both providers: G01/G08/G13 |
| S25 workspace mutation | Committed Git snapshot tests | Immutable dirty-workspace capture and mutation barrier: G10 |
| S26 repository isolation | Named partial: cross-repository attestation rejected | Role-authorized commands, reads, memory and disclosure isolation: G06/G11/G12 |
| S27 independent effects | Existing final-publication/memory tests enforce coupled legacy flow | Separate delivery and memory activation/retries: G07/G12 |
| S28 migration | Legacy filename fallback tests | Inventory, aliases, collision quarantine, dry run and cutover: G13 |
| S29 encoding | JSON baseline | If TOON enabled, exact tokenizer/round-trip/fallback qualification: G14 |
| S30 publish race | Human-decision version validation primitive | Refresh head/base/policy/generation before/after send: G07 |
| S31 question budget | No target question scheduler | Question identities, batching, deduplication, deadlines and limits: G03/G06 |
| S32 grant withdrawn | No grant lifecycle | Revoke/fence pending and reconcile in-flight: G07 |
| S33 method replacement | Compiler/delegation tests | Execute replaced methods while preserving required obligations: G04 |
| S34 memory revoked | No governed memory lifecycle | Exclude revoked source and revalidate derived use: G12 |
| S35 late lease result | No durable action leases | Reject stale authority; separately settle attributable cost: G02/G03 |
| S36 conflict/empty predicates | Matching/validation tests | Target scoped conflict state and independent continuation: G04/G05 |
| S37 scoped memory value | Semantic recall contract smoke | Scoped active records, non-normative influence, paired evaluation: G12/G14 |
| S38 beyond-diff caller | Document graph traversal tests | Real caller evidence, report-level finding and paired evaluation: G11/G14 |
| S39 freshness without head change | No target pointer/outbox | Base/policy/generation invalidation: G01/G07 |
| S40 derived revocation | No access-epoch/lineage reducer | Invalidate recalled/promoted/derived output at use/disclosure: G11/G12 |
| S41 grant/manual precedence | Compiler publication restriction primitives | Actual grants cannot bypass required manual authorization: G04/G07 |
| S42 total cost | Limit schema/ceiling validation | Atomic input-inclusive concurrent reservations: G03 |
| S43 parallel reducer | Mutex/cloning/transition primitives | Action tokens/dependency epochs/CAS without repeated I/O: G02/G05 |
| S44 immutable publication | Kind-level bot markers | Separate immutable operations from serialized logical output: G07 |
| S45 CI execution | Named partial: exit mapping, cancellation; other runner tests cover artifacts | Destruction/handoff receipts, evaluate-and-await and full error variants: G09 |
| S46 native exports | Canonical JSON exports only | GitHub annotations, GitLab Code Quality mappings/exclusions: G09 |
| S47 old generation/pipeline | CI identities only | Service-owned current native output and stale-write reconciliation: G07/G09 |
| S48 hostile fork | Named partial: panic model is not invoked for fork; artifact verifier negative tests | Full untrusted execution/publication credential boundary: G08/G09/G14 |
| S49 dependency cycle | Named assertions: cycle/missing/explicit independent graph | Verify dependency-source authority and wire all required waits: G04/G09 |
| S50 baseline | Named evaluator assertions: absent/compatible/incompatible baseline | Acquire/bind baseline through real runner/service command: G09 |
| S51 permission/batch failure | General HTTP error tests | Native capability selection and uncertain annotation batches: G07/G09 |
| S52 synthetic merge | Named domain/runner assertions: head vs merge, mapping digest, missing base | Position export/native attachment and complete cross-provider qualification: G09 |
| S53 installation | Static tokens/webhooks | GitHub App, explicit project scope and least-privilege setup: G08 |
| S54 triggers/loops | Webhook actions, draft/label/branch policy tests | Complete event matrix, pause, bot-loop prevention and required gate handling: G08 |
| S55 incremental/full | Legacy rerun calls whole pipeline | Selective evidence revalidation and immutable full-review intent: G01/G05/G08 |
| S56 native commands | CLI/channel commands, not native SCM dispatch | Mention/reply authorization, pause/resume and scoped question commands: G06/G08 |
| S57 finding continuity | Legacy inline comments/suppression | Semantic finding identity, attributed dispositions and native thread lifecycle: G06/G08/G11 |
| S58 check mapping | Pure gate/exit mapping | Current native pending/running/final projections without silent clear: G09 |
| S59 enterprise/queue | No explicit compatibility inventory | Setup feature matrix, truthful fallback and rejection: G08/G09 |
| S60 access revoked | No installed connection/grant lifecycle | Fence new reads/sends, reconcile in-flight, report retained audit: G07/G08 |

## Engineering Case Families

| Family | Cases | Missing runtime/qualification |
| --- | --- | --- |
| Progress | ENG-P01-P06 | Typed admitted progress, semantic duplicate/non-progress detection, evidence invalidation: G05/G11 |
| Stagnation | ENG-S01-S05 | Per-line suspend/eligibility/resume and global incomplete outcome: G05 |
| Priority | ENG-A01-A04 | Protected capacity and observed-risk ordering: G03/G05 |
| Reuse | ENG-R01-R06 | Revision/dependency/environment revalidation and historical authority: G01/G05/G07 |
| Latest revision | ENG-L01-L05 | Confirmed-head coalescing, late events/results, historical intent and unresolved costs: G01/G03/G05 |
| Budgets | ENG-B01-B06 | Shared three-scope atomic accounting and period attribution: G03 |
| Coordination | ENG-C01-C05 | Simultaneous admission, lost replies, authority outage and excluded account usage: G02/G03 |
| Recovery | ENG-DUR01-DUR07 | Real prepare/send/result crash boundaries, late leases and conservative replacement: G02/G03/G07 |
| Consolidated | ENG-F01-F06 | Evidence interpretation, renewal, sandbox isolation, scoped CI gaps and delegation/conflict execution: G03/G04/G05/G09/G14 |

## Research Decisions Still Awaiting Their Implementations

| Accepted lesson | Reusable code | Remaining translation into behavior |
| --- | --- | --- |
| Addy: triage and proportionate effort | Skills, path/corpus heuristics | Cheap triage before retrieval; risk/uncertainty plan and protected checks: G03-G05 |
| Addy: green tests are not correctness proof | Verified CI artifacts, test-quality skill | Weakened-test/CI checks and independent omission review: G05/G11/G14 |
| Greptile: beyond-diff understanding | Corpus graph, SCM adapters | Revision-bound code reads/dependency evidence and report-level proof paths: G11 |
| Greptile: scoped methods and conversation | Policy compiler, CLI/TUI/chat | Trusted method execution, attributable questions/disputes and scoped resume: G04/G06/G08 |
| Greptile learning / Hermes separation | MemPalace adapter, procedural skills | Governed exact/semantic/episodic/procedural records, independent activation and evaluation: G12 |
| CodeRabbit/Greptile installed experience | Webhook intake and comment APIs | Install/capability/revocation lifecycle, incremental work and native commands/checks: G08/G09 |
| Approved engineering decisions | Domain identities/types and gate functions | Durable journal, accounting, execution fencing and immutable effects: G01-G07 |

Research material and sources remain in ARCHITECTURE section 9 and SPEC 17/18.
This audit does not claim to have repeated competitor research or verified any
proprietary engine. Implementing additional tools without the accepted behavior
would not close these gaps.

## Dependency Order To Completion

1. Reconcile the Phase 2 exit ledger with code. Preserve working policy/CI
   foundations; close trusted method resolution, capability/ceiling wiring,
   scoped conflicts, prerequisites and S01-S09/S22/S24-S26 assertions. Some of
   these assertions cannot finish until the controller exists; record those
   dependencies instead of declaring the phase complete.
2. Implement G01-G03 together as the durable attempt/action/accounting spine.
   Establish crash/authority invariants before claiming resumability.
3. Complete G04-G06 on that spine: executable methods, triage, independent
   scrutiny, progress, protected checks, scoped questions and revision reuse.
4. Implement G07-G09 for independent effects, installed provider workflows,
   native gate delivery and coordinated CI. Existing artifact CI remains usable.
5. Complete G10-G12 with the same engine: private dirty local review, run-level
   evidence graph, scoped governed memory and optional enrichment fallback.
6. Complete G13 alongside those changes so public contracts, UI and migration
   follow the actual state/authority model, not the legacy HIL lifecycle.
7. Close G14 with all scenario receipts, representative evaluations and rollback/
   recovery rehearsal. Only then assess completion of the accepted product.

After each coherent slice: failing behavior fixture first, implementation,
focused tests, relevant race/fault tests, then a commit with explicit contract
and remaining-gap references. Green baseline tests are a regression gate, not the
definition of target completion.

## Documentation Corrections Required

- ARCHITECTURE/SPEC opening candidate/not-approved labels conflict with their later
  accepted-design records and ROADMAP. Reconcile the historical wording without
  inventing a new acceptance decision.
- ROADMAP Phase 2/STATUS remaining-gate prose still lists the ephemeral runner
  integration as missing even though its implementation/evidence sections show it.
- SPEC's scenario entries still say specified-only for IDs with implemented
  partial tests. Replace this with assertion/variant-level receipts; do not
  mark an entire scenario complete just to fix the wording.
- Keep infrastructure alpha, existing workflow alpha and accepted-target
  qualification as separate scopes in every readiness/completion statement.

These inconsistencies are audit findings; this report does not silently rewrite
the accepted scope or mark work complete.
