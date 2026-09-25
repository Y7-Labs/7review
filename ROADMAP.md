# 7review Roadmap

Updated: 2026-09-25
Status: DESIGN ACCEPTED; PHASE 1 IMPLEMENTATION AUTHORIZED.

[ARCHITECTURE.md](ARCHITECTURE.md) defines the product and system;
[SPEC.md](SPEC.md) defines behavior and acceptance;
[STATUS.md](STATUS.md) records facts. This file owns all planned work,
including the former immediate queue and detailed implementation plan.

## Current Objective

Implement the accepted design incrementally, beginning with characterization and
the canonical review domain before changing runtime orchestration.
Private local review, GitHub/GitLab PR/MR review and non-interactive CI are core
entry paths, not competing products. Existing test/lint/security/coverage results
feed the investigation; native checks and quality artifacts expose its results.
Teams own advisory/blocking rules. Humans retain merge authority.

Evaluate the existing SCM, Source, corpus, skills, tools and model primitives for
reuse, adaptation or replacement; do not force the target into their current layout.
Replace rigid final-stage HIL with bounded investigation and scoped questions.
Separate assessment, quality-gate result, disclosure and governed learning.
Headroom, MemPalace and code intelligence are optional in the target, not yet
optional by virtue of this design in the existing packaged runtime.

## Design Sequence

1. Product conception: users, J01-J10 journeys, R01-R23 requirements, method
   freedom and autonomy boundaries.
2. System conception: responsibilities, adaptive loop, identity, persistence,
   evidence/memory, CI modes, permissions and provider limitations.
3. Specification: state transitions, interfaces, policy composition, recovery,
   budgets, quality gates, export and migration.
4. Validation: walk S01-S60 and their variants, challenge contradictions, record
   decisions and approval explicitly. Written scenarios are not executed tests.
5. Documentation refinement: keep these four canonical files coherent. Final
   tutorials and operational guides follow design validation, using the local
   reference projects for organization rather than importing their architectures.
6. Development: only on separate authorization, following the conditional
   implementation plan with tests in each slice.

Steps 1-3 now have a complete candidate revision; ENG-D1 through ENG-D13 are
approved directions and DOC-01 through DOC-06 are closed at design level. The loop
contract and its 50 additional scenarios are in SPEC. The final whole-system review
found no blocking design contradiction. The user accepted the complete design and
authorized Phase 1 on 2026-09-25. No runtime completion or new provider
qualification is claimed.

## Immediate Queue

The documentation audit found six substantive design blockers. They are now closed
in [SPEC sections 20-25](SPEC.md#20-public-schema-contracts): full public schemas,
operational lifecycles, legacy compatibility, qualification targets, a two-mode
publication topology and clause-level traceability. Closure is a design result,
not implementation evidence.

1. Review the [product contract](ARCHITECTURE.md#product-contract) and
   [architecture](ARCHITECTURE.md#system-architecture) as one product:
   local, PR/MR and CI review with team-owned methods and quality gates.
2. Preserve the accepted [specification](SPEC.md) as the implementation contract.
   Amend it only through an explicit decision when implementation evidence exposes
   a contradiction. The final challenge covered S01-S60 and all 50 ENG cases.
3. Preserve the independent whole-system review result: after two correction
   cycles it found no remaining blocking design contradiction across CI,
   installed-SCM, schemas, lifecycles, migration, metrics and traceability.
4. Preserve approval of ENG-D1-ENG-D13 in
   [the decision register](ARCHITECTURE.md#decisions-and-tradeoffs). Review the
   complete candidate revision as one system; do not reopen individual decisions
   without concrete contradictory evidence or count written cases as tested.
5. Maintain ARCHITECTURE/SPEC/ROADMAP/STATUS as the canonical set. Final tutorials
   and operator documentation follow validation; runtime development requires
   separate authorization, not automatic continuation.

The [validation record](STATUS.md#september-25-contract-reconciliation) distinguishes
static checks, historical reviews and future execution evidence.
This roadmap owns sequence; [STATUS](STATUS.md) owns baseline/target state.

## Authorized Implementation

The phases below are authorized in sequence as of 2026-09-25. Each phase still
requires its own tests and exit evidence; authorization is not proof of completion.

## Gate 0: Review The Design — Complete

Review the concrete recommendations in
[the decision register](ARCHITECTURE.md#decisions-and-tradeoffs), including compared
storage/orchestration choices, scoped readiness, complete budget accounting,
publication grants and migration. DOC-01 through DOC-06 have complete accepted
contracts.

The contracts were reviewed against [S01-S60](SPEC.md#acceptance-cases), the 50
ENG cases, security boundaries, provider limitations and developer workflows.
The independent final challenge found no blocking design contradiction after
corrections. The user explicitly accepted the complete revision on 2026-09-25.

Exit satisfied: no unresolved structural contradiction remained and the user
accepted the complete specification revision and decision record.

## Phase 1: Characterization And Canonical Domain — Complete 2026-09-25

Scope: `agent/review`, pipeline/store consumers, scenario fixtures.
- Preserve existing behavior with baseline fixtures and record known limitations.
- Finish `Source` authority for corpus, skills, findings, reports and metadata.
- Define identities, events, checks, observations and decision envelopes from the spec.
- Include ExecutionContext, immutable GateResult and separate investigation,
  coverage, gate and delivery projections from the first domain slice.
- Create the deterministic scenario harness now, including failing/new-target
  expectations isolated from legacy regression assertions.

Exit: no competing authoritative copies; baseline tests green; spec invariants
mapped to test names. Avoid broad interface churn.

Evidence: `review.Source` now owns review inputs, SCM data, corpus, skills, memory,
findings, reports and execution metadata. Target attempt/execution identities,
events, observations, decisions, checks, assessment, coverage, gate and delivery
projections have validated Go contracts. Run-store boundaries clone canonical
state defensively. Contract tests cite SPEC clauses/scenarios; `go test ./...` and
`go test -race ./agent/review ./agent/pipeline` pass. Legacy HIL fields remain an
explicit compatibility surface for later command/effect migration, not a second
copy of canonical review artifacts.

## Phase 2: Trusted Inputs And Policy

Scope: repository snapshots, profile compatibility and policy resolution.
- Bind base/head/local snapshots to verified identity and digests.
- Compile legacy behavior explicitly; implement strict V2 schema and merge laws.
- Add proposed policy validation/explanation commands and readiness projection.
- Preserve scope, source-of-truth authority and corpus anti-noise fixtures.
- Bind CI job, source head, comparison/merge tree and imported artifact provenance.
- Compile scoped quality-gate rules, baseline compatibility and independent
  prerequisites; reject self-dependency and unverified required dependencies.

Exit: S01-S09, S22, S24-S26 policy/input assertions pass without model calls.
All schema fields have documented precedence, bounds and error behavior.

Progress (2026-09-25): verified SCM/local snapshot attestations, revision-bound
test readiness, strict JSON/YAML `ReviewConfigV2`, explicit legacy projection,
runtime ceilings, deterministic pack composition and offline validate/explain
commands, deterministic trigger evaluation and GitHub/GitLab trusted-base intake
wiring are implemented. Remaining: complete delegation and quality-gate semantics, CI/artifact provenance,
corpus fixtures and all named scenario assertions required by the exit criterion.

## Phase 3: Durable Attempts And Recovery

Scope: storage, app intake and job lifecycle.
- Implement the reviewed coordinated PostgreSQL budget/action ledger and its
  atomic reservations/intent. Qualify autonomous local storage separately;
  SQLite remains a candidate there, not a shared multi-runner database.
- Specify fixed-period accounting, unresolved carry-over and all three ceilings;
  test concurrent admission, idempotent settlement and authority outages.
- Add unique attempts, command receipts, durable jobs, leases and version fencing.
- Implement migration dry run, aliases, backup/restore and read-only legacy import.
- Recover accepted jobs and interrupted actions; fail visibly on corruption.
- Never blindly resend uncertain non-idempotent provider calls. Qualify each
  adapter's idempotency window, receipt lookup and SDK retry behavior.
- Keep network/model calls outside transactions.
- Qualify isolated ephemeral stores separately from persistent service recovery;
  verify acknowledged handoff and service-owned shared publication without hidden
  infrastructure requirements for autonomous artifact-only jobs.

Exit: S12-S17 and S28 recovery tests pass; restart loses no acknowledged input
under the documented durability assumptions. Unknown model-call usage is bounded.

## Phase 4: Delivery And Human Commands

Scope: application commands, SCM publication, existing channels, outbox.
- Separate publication authorization, finding feedback, investigation requests
  and memory activation.
- Implement stable delivery identity, provider reconciliation and uncertain state.
- Recheck revisions around publication; translate legacy commands explicitly.
- Retry notifications/index writes without rerunning review.
- Implement separately granted native GitHub checks/annotations and GitLab
  pipeline-bound statuses in the coordinated service; reconcile uncertain batches
  and fence stale jobs. Missing permissions cannot produce fake success.

Exit: S13-S16, S26-S28 and S30 pass with fakes; stale authorization cannot silently
publish a current-looking report. Test real adapter reconciliation separately.

## Phase 5: Resumable Scheduler

Scope: pipeline extraction at existing boundaries.
- Implement cheap triage, eligibility, shared budget reservations and checkpoints.
- Schedule bounded tool/model actions, waits, cancellation and scoped resumption.
- Track coverage, per-line stagnation and explicit incomplete outcomes. Protect
  priority checks from discretionary budget consumption; coalesce automatic work
  toward the latest confirmed revision and revalidate reused evidence.
- Separate independent omission review from candidate verification.
- Use the existing orchestrator and tool implementations under governed permissions.
- Add the non-interactive CI entry path using this same scheduler, with bounded
  wait/handoff, deterministic gate evaluation, export manifests and exit mapping.
- Consume trusted existing quality evidence without running untrusted PR scripts
  with model or publisher credentials.

Exit: S01-S14 and S19-S23 action/state assertions pass. Every expensive action
has a reason and reservation; waiting consumes no worker; duplicate responses
cannot restart completed work.

## Phase 6: Evidence And Governed Memory

Scope: evidence projection, memory semantics, capability integration.
- Extract corpus graph by equivalence; persist bounded proof paths.
- Support invariant-backed findings without inventing missing contracts.
- Govern feedback candidates, activation, expiry, contradiction and deletion lineage.
- Keep local exact recall; adapt MemPalace as optional index and Headroom as
  optional optimization. Add setup selection and health checks, not silent installs.
- Attest optional code-intelligence scope/revision and expose coverage limits.
- Specify and qualify optional isolated runtime verification before exposing it:
  no model/publisher secrets in repository execution, bounded network/resources,
  dependency provenance, cancellation and cleanup. Generated tests are not authority.
- Preserve the accepted method-comparison and correction-dossier product scope;
  neither authorizes auto-activation, repair or external transfer.

Exit: S08-S09, S18-S19, S22, S26-S27 pass; memory/index absence does not break
baseline review. Required missing capabilities cannot silently pass.

## Phase 7: Quality And Product Validation

CI integration is a product deliverable, not just verification of this repository.
Qualify the runner, policy and publication slices introduced in Phases 1-5 as
one product workflow: artifacts/exit contract, deterministic gate, GitHub checks
and GitLab status/Code Quality export. Verify runner/service credential separation,
fork safety, prerequisite-cycle checks and explicit pipeline/merge-tree identity.
Execute S45-S60 and provide version-pinned CI examples.
Advisory is the default; teams may enable blocking rules through trusted policy.

- Implement and execute S01-S60 and the 50 ENG cases, shared provider fixtures and
  local snapshot scenarios. Case count is not a coverage or quality measurement.
- Run opt-in repeated live-model comparisons against the characterized baseline.
- Measure independent-pass, graph and memory contributions separately.
- Evaluate TOON only after typed artifacts stabilize; default JSON remains.
- Surface plans, questions, coverage, costs and next actions consistently across
  existing CLI/TUI/HTTP/channel surfaces.
- Run controlled temporary GitHub PR/GitLab MR tests with explicit cleanup.

Exit: no critical silent-clear or lifecycle invariant failures in the acceptance
corpus; quality/cost reports published with actual model identity and limitations.
No score-only merge recommendation.

## Phase 8: Controlled Rollout

Shadow planning first: no duplicated external publications or memory activation.
Then opt-in new attempts, with legacy draining and version-compatible read support.
Verify local-volume deployment, disk limits, backup, restore and rollback.
Qualify ephemeral jobs without persistent sidecars, separately from service
backup/recovery. Enabling required protection contexts needs explicit repository
owner action and rollback instructions; old report grants do not authorize it.
Do not scale horizontally or add channels in this milestone.

Exit: operator runbook and recovery rehearsal pass; no old binary can write the
new schema. Broader activation requires explicit operational approval.

## Commit And Verification Discipline

One concern per commit; tests accompany each behavior change. Examples:
`test(review): characterize attempt lifecycle`,
`refactor(review): canonicalize finding state`,
`feat(storage): persist review inbox atomically`,
`feat(engine): resume revision-bound questions`.

Run focused tests for touched packages, then the full existing Go suite before
merging. Add tests for proposed packages only after those packages exist:

```bash
GOCACHE=/tmp/7review-go-cache go test ./agent/review ./agent/pipeline ./agent/app
GOCACHE=/tmp/7review-go-cache go test ./...
make verify
```

Deployment changes additionally require the existing Compose smoke gate.
Credentialed models/SCM tests remain opt-in, never replacements for deterministic
CI. Documentation-only work uses link/consistency/diff validation; it does not
establish a fresh green runtime baseline.

## Historical Baseline And Unfinished Qualification

Earlier work recorded runtime packaging completion on 2026-08-27, including
pinned sidecars, container hardening, readiness and Compose smoke coverage.
Prior Go/Compose green results are historical; none was rerun for this document.
The old packaging-first roadmap and its file-store-versus-external-queue choice
are superseded by the candidate design and D06 comparison.

Existing webhook intake, draft/final publication, profile support and approval
channels remain compatibility assets. Hosted provider callbacks, recovery,
review accuracy and operator procedures still need evidence for the new system.
An earlier draft-publication smoke does not establish the entire final approval
and learning lifecycle, nor native CI integration.

## Exit Gates

Design: complete for implementation entry. Structural choices, critical
transitions, traceability, independent whole-system review and user acceptance
are recorded.

Implementation: actual deterministic and fault-test receipts, hosted-provider
qualification, measured quality/cost results and operational recovery evidence.
No documentation-only check can establish these gates.
