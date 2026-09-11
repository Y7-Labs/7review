# Implementation Plan: Adaptive Review Platform

Status: REVISED FOR CONTINUED PHASE 1 IMPLEMENTATION
Depends on: `adaptive-review-platform.md`

## Delivery Rules

- Preserve behavior before changing composition.
- One architectural concern per commit.
- Keep `main` green after every phase.
- Do not add channels, deployment platforms, or executable policy engines.
- Run deterministic tests before model/network evaluations.
- Persist compatibility evidence and migration notes in each phase.

## Phase 0: Characterize And Freeze

Goal: establish a reproducible baseline for the current primitives.

- Add golden lifecycle fixtures for GitHub and GitLab normalized requests.
- Capture current skill activations, corpus selections, validator outcomes,
  publication gates, run events, and memory boundaries.
- Add a behavior-equivalence helper that compares normalized artifacts rather
  than report prose.
- Define package dependency and run-state diagrams in architecture docs.

Exit: deterministic baseline scenarios pass on current code with no runtime
behavior change.

## Phase 1: Canonical Run Source

Goal: remove duplicated state before introducing new decisions.

- Add missing plan/repository identity placeholders to `review.Source`.
- Migrate pipeline, stores, Headroom, MemPalace, app DTOs, and tests to read and
  write canonical `Source` fields.
- Reduce `review.Context` to synchronization and transient batch accumulation.
- Add invariant tests that persisted and in-memory artifacts cannot diverge.

Exit: no request, diff, skill, finding, report, or metadata field has two
authoritative copies.

## Phase 2: Repository Snapshot Contract

Goal: bind all repository knowledge to an identified revision.

- Introduce snapshot identity and `fs.FS` contracts.
- Implement verified mounted-workspace snapshots for local/Docker operation.
- Extend GitHub/GitLab adapters with bounded base-revision file/tree access or
  snapshot materialization.
- Route corpus, repository skills, rules, and future policy through snapshots.
- Treat proposed head policy as evidence only.

Exit: a run fails planning when repository identity or trusted revision cannot
be established; path traversal and oversized content tests pass.

## Phase 3: Review Plan Compatibility Layer

Goal: make the effective strategy explicit without changing outcomes.

- Define immutable `review.Plan`, provenance, conflicts, classification, and
  fingerprint types.
- Implement `LegacyPlanCompiler` from `CompiledProfile`, current skill settings,
  corpus policy, validators, tools, and publishing defaults.
- Persist the plan before model execution and expose it through run status.
- Make pipeline selection consume legacy plan fields incrementally.

Exit: baseline scenarios produce behavior-equivalent results plus a stable plan.

## Phase 4: Review Evidence Graph Foundation

Goal: connect review decisions and evidence without building a general code
knowledge platform.

- Extract current `CorpusGraph` selection into `agent/evidence` behind a
  review-focused projector while preserving selected sections, scores, limits,
  and reasons exactly.
- Add typed nodes, relations, and bounded proof paths for change classification,
  plan decisions, skills, corpus, memory, tools, hypotheses, citations,
  findings, validation, HIL, and outcomes.
- Persist the run-scoped graph in `review.Source` and the existing run ledger;
  keep derived indexes rebuildable and add no new runtime service.
- Enforce repository/revision scope, the authority lattice, relation allowlists,
  traversal depth, byte budgets, and memory/inference restrictions.
- Expose graph explanations through run status and operator tools.
- Define optional capability metadata for future SCIP, CodeQL, Joern, or
  language-specific adapters, without implementing mandatory symbol indexing.

Exit: baseline corpus fixtures remain equivalent; every accepted finding has a
bounded explainable path; weak, cross-revision, memory-only, and inferred-only
paths fail deterministic validation.

## Phase 5: Governed Memory Foundation

Goal: replace untyped report recall with scoped, explainable review memory.

- Add `agent/memory` records, queries, recall items, proposals, lifecycle states,
  and a provider-neutral `MemoryEngine`.
- Separate append-only run history from curated memory; stop storing complete
  final reports as conventions.
- Adapt MemPalace behind storage/search capabilities with stable idempotent IDs.
- Compile recall from repository identity, trusted revision, `ReviewPlan`,
  domains, modules, features, and changed paths.
- Add exact-plus-semantic fusion, scope/status filtering, authority and freshness
  ranking, budgets, deduplication, and retrieval explanations.
- Derive feedback-aware proposals after HIL, but require separate memory approval
  and validate redaction, evidence lineage, contradiction, and supersession.
- Add memory-on/off scenario metrics before allowing procedural promotion.

Exit: typed recall is behaviorally integrated, MemPalace is replaceable, backend
failure degrades safely, and no memory can independently confirm a finding or
silently change review policy.

## Phase 6: Declarative Repository Policy

Goal: let repositories define scoped review behavior safely.

- Add versioned YAML structs and JSON schemas for project config, packs, rules,
  and scenario manifests.
- Implement strict parsing, reference validation, namespace rules, inheritance
  cycle detection, path confinement, and bounded values.
- Implement deterministic classification and resolver merge laws.
- Add `7review validate-policy` and `7review explain`.
- Review policy changes using trusted base policy.

Exit: unit fixtures cover every predicate, merge rule, conflict, provenance
path, unsafe override, and fingerprint stability case.

## Phase 7: Staged Engine Extraction

Goal: split the monolithic pipeline along tested lifecycle boundaries.

- Introduce stage interfaces only where a deterministic or external boundary
  already exists.
- Extract planning, evidence gathering, evidence-graph projection, model
  execution, validation, draft composition, approval, publication, and memory
  writeback in that order.
- Keep one engine responsible for transitions, retries, cancellation, and run
  persistence.
- Preserve existing public app and tool APIs during extraction.
- Replace the implicit tool follow-up loop with typed hypotheses, required-check
  coverage, investigation budgets, progress detection, and explicit stop reasons.
- Keep the controller single-agent. Add a selective verifier only after
  deterministic candidate validation and only for plan-selected risk classes.
- Persist trajectory artifacts without hidden model reasoning.

Exit: stage contract tests can exercise planning through final publication with
fakes; loop tests prove completion, no-progress, budget exhaustion, superseded
head, escalation, deduplication, and verifier boundaries; current app E2E tests
remain unchanged and green.

## Phase 8: Capability Registry

Goal: make tools auditable and prevent catalog/executor drift.

- Register descriptor and handler together.
- Add actor scope (`model`, `operator`, `system`), side-effect class, lifecycle
  stage, approval requirement, and plan capability checks.
- Migrate existing read tools first, then operator actions.
- Keep model and operator execution entrypoints separate.
- Add setup discovery and explicit opt-in installation guidance for optional
  code-intelligence adapters; unavailable capabilities never block baseline
  review.
- Require repository, revision, tool version, configuration, and digest
  attestation before adapter output enters an evidence graph.

Exit: duplicate names, missing handlers, schema drift, unauthorized actors, and
unapproved side effects fail deterministically.

## Phase 9: Scenario And Evaluation Harness

Goal: turn review strategy and quality into measurable product behavior.

- Implement scenario fixture loading, patch application, expected-plan checks,
  normalized finding scoring, repetition, and machine-readable reports.
- Add trajectory graders for hypothesis yield, evidence gain, coverage, stop
  reason, escalation correctness, and cost.
- Add paired memory-off/on grading for useful recall, harmful/stale recall,
  false-positive recurrence, citation validity, and quality delta.
- Grade expected/forbidden relations, proof-path precision, unsupported-path
  rejection, traversal budgets, and graph context cost.
- Add a model-facing `PromptEncoder` after typed graph and memory artifacts are
  stable. Preserve raw diffs and documents as labeled text, keep compact JSON
  as the structured default, and keep model outputs in JSON.
- Benchmark compact JSON against strict TOON on representative evidence nodes,
  edges, findings, citations, CI observations, file summaries, and memory
  recall using each target model's exact tokenizer.
- Select TOON only for uniform collections when input-token savings meet a
  configurable threshold (10% by default) without reducing review accuracy,
  schema fidelity, or latency. Validate semantic round trips and fall back
  deterministically to compact JSON for unsupported shapes or encoding errors.
- Emit a machine-readable encoding report containing the selected format,
  token counts, savings, fallback reason, and quality comparison.
- Land at least 24 deterministic logical scenarios.
- Add a small tagged live-model subset using `openrouter/free` by default.
- Add budgets and statistical tolerance for non-deterministic models.

Exit: local and CI deterministic suites report strategy accuracy and lifecycle
correctness; opt-in live eval reports finding metrics without gating on prose;
prompt-format changes require tokenizer-specific savings and quality evidence.

## Phase 10: GitHub/GitLab Parity And Product Surface

Goal: prove the architecture against real SCM behavior and make it usable.

- Materialize curated scenarios as temporary GitHub PRs and GitLab MRs.
- Verify normalization, plans, evidence paths, draft/final markers, retry
  idempotency, HIL, memory writeback, and cleanup.
- Expose plan, provenance, conflicts, and scenario results in operator APIs,
  CLI/TUI, and documentation.
- Update Docker and CI only for new commands and fixtures, not new services.

Exit: equivalent logical scenarios produce equivalent plans across providers;
at least one complete controlled E2E passes on each provider.

## Atomic Commit Sequence

1. `test(review): characterize current lifecycle artifacts`
2. `refactor(review): make source the canonical run state`
3. `feat(repository): add trusted snapshot contract`
4. `feat(scm): load base snapshots through github and gitlab`
5. `feat(review): compile legacy behavior into review plans`
6. `refactor(evidence): extract corpus graph projector`
7. `feat(evidence): persist review relations and proof paths`
8. `test(evidence): lock authority and traversal boundaries`
9. `feat(memory): add governed memory domain and lifecycle`
10. `refactor(memory): index governed records with mempalace`
11. `test(memory): evaluate recall and feedback learning`
12. `feat(policy): add review pack schema and resolver`
13. `feat(cli): explain and validate repository policy`
14. `refactor(engine): extract explicit review stages`
15. `feat(engine): add bounded hypothesis review loop`
16. `refactor(tools): register governed capabilities`
17. `test(eval): add adaptive review scenario harness`
18. `perf(prompt): add measured adaptive toon encoding`
19. `test(scm): verify github and gitlab scenario parity`
20. `docs(review): publish adaptive review operating model`

## Verification Gates

```text
domain/unit -> projector equivalence -> engine contracts -> scenarios
                                                    |
                                                    v
                                      opt-in model evaluations
                                                    |
                                                    v
                                      GitHub/GitLab provider E2E
```

```bash
GOCACHE=/tmp/7review-go-cache go test ./agent/review ./agent/profile
GOCACHE=/tmp/7review-go-cache go test ./agent/policy ./agent/repository
GOCACHE=/tmp/7review-go-cache go test ./agent/evidence
GOCACHE=/tmp/7review-go-cache go test ./agent/memory
GOCACHE=/tmp/7review-go-cache go test ./agent/skills ./agent/tools
GOCACHE=/tmp/7review-go-cache go test ./agent/pipeline ./agent/app
GOCACHE=/tmp/7review-go-cache go test ./...
make verify
make compose-smoke
```

Live model and SCM E2E gates are explicit, credentialed jobs and do not replace
deterministic CI.

## Next Implementation Slice

Phase 0 and the first Phase 1 slice are complete. Continue canonical state
migration before snapshots, plans, evidence graph, or memory changes:

1. Move skills, selected corpus, findings, report, and run metadata authority to
   `Source` in small behavior-preserving commits.
2. Remove each duplicated `Context` field only after pipeline, stores,
   Headroom, MemPalace, app DTOs, and tests read the canonical value.
3. Extend lifecycle artifact fixtures after each migration.
4. Finish Phase 1 with invariant tests and a full green suite.

Do not start snapshot, evidence-graph, memory, or policy runtime work until
Phase 1 exits. Design documents may evolve, but runtime composition stays
frozen.

## GSTACK REVIEW REPORT

Review target: complete 7review system direction, including architecture,
quality, tests, performance, security, operations, and agent behavior.

Status: **APPROVED WITH PHASE GATES**

### Scope Decision

The policy engine alone is insufficient. The accepted scope is the adaptive
review platform: canonical run state, trusted repository snapshots, compiled
plans, a run-scoped Review Evidence Graph, governed memory, staged execution, a
bounded hypothesis loop, governed capabilities, evaluation scenarios, and
provider parity. A universal software knowledge graph and mandatory code
indexers are explicitly outside the product direction.

### Architecture Review

- Preserve the rich primitives; do not rewrite the product around YAML.
- Make `review.Source` authoritative before adding new policy state.
- Bind policy, rules, methods, and corpus to one trusted base snapshot.
- Evolve the existing `CorpusGraph` into a review explanation connecting
  change, methodology, evidence, findings, human outcomes, and learning.
- Persist evidence relations in `review.Source` and the run ledger; add no graph
  database or parallel source of truth.
- Keep MemPalace as a rebuildable semantic index over governed memory records.
- Keep one deterministic engine and one bounded review agent, not a swarm.
- Separate repository methodology from deployment/operator configuration.
- Keep model tools read-only and operator mutations on a distinct actor surface.

Primary production failure addressed: a run must not silently use policy or
evidence from the wrong repository/revision. Planning fails closed when snapshot
identity cannot be verified; optional enrichers fall back to baseline review.

### Code Quality Review

- The largest risk is synchronization drift between duplicated `Context` and
  `Source` fields. Phase 1 removes it before new features.
- The 2,600-line pipeline and static tool dispatch are decomposition targets,
  but extraction follows behavior characterization and existing boundaries.
- Existing corpus graph scoring and anti-noise behavior migrate through exact
  equivalence fixtures before new relations are added.
- Review relations stay in the review domain; optional code intelligence remains
  behind the capability registry instead of creating a platform subsystem.
- New interfaces are allowed only for domain, deterministic, or external I/O
  boundaries already visible in the code.

### Test Review

- Existing package tests remain regression gates.
- Evidence-graph tests cover exact corpus equivalence, required/forbidden paths,
  authority, cycles, budgets, stale revisions, isolation, and replay.
- New resolver tests are model-free and exact.
- Agent-loop tests grade trajectories, progress, stop reasons, and escalation.
- Real-model evals use normalized outcomes and repeated runs, not exact prose.
- Prompt-encoding fixtures cover uniform, nested, sparse, empty, single-row,
  delimiter-heavy, newline-heavy, and Unicode inputs; they verify semantic JSON
  round trips, fallback behavior, exact token counts, and finding-quality parity.
- GitHub/GitLab parity and provider E2E remain separate credentialed layers.

### Performance Review

- Snapshot reads, evidence bytes, tool calls, rounds, model calls, and elapsed
  time are budgeted and observable.
- Graph traversal is run-scoped and bounded by relation allowlist, depth, nodes,
  bytes, and elapsed time; no new database service is introduced.
- Snapshots are cacheable by repository plus revision, immutable, and bounded.
- Parallel review workers share one plan and merge typed hypotheses before final
  validation to prevent duplicate comments and context multiplication.
- Headroom reduces context but is never a correctness dependency.
- Prompt serialization is selected per artifact and model. TOON is used only
  when tokenizer-specific ablation clears the savings threshold and preserves
  review quality; compact JSON is the deterministic fallback.

### Security Review

- Base-policy authority and snapshot identity are hard invariants.
- Repository policy remains non-executable and path-confined.
- The effective allowlist is policy permissions intersected with runtime
  permissions; repository policy can never grant a forbidden capability.
- Final publication and memory remain explicitly human-authorized.
- Memory activation is a distinct authorization from final review approval;
  recalled memory remains advisory and repository truth always wins.
- Memory and inferred relations cannot independently complete a confirmed
  finding path; optional analyzer output requires exact revision attestation.

### Readiness Dashboard

| Area | State | Gate before implementation |
| --- | --- | --- |
| Product direction | Ready | Design approved |
| Domain model | Needs migration | Finish Phase 1 canonical source |
| Repository trust | Designed | Snapshot contract tests |
| Policy composition | Designed | Legacy plan equivalence |
| Evidence graph | Designed | Corpus equivalence and proof-path tests |
| Prompt efficiency | Planned | JSON/TOON ablation after typed artifacts stabilize |
| Agent loop | Designed | Typed trajectory tests |
| Memory | Designed | Typed lifecycle, retrieval ablation, safe degradation |
| Tools | Existing, fragmented | Registry migration after engine |
| Evals | Planned | 24 deterministic scenarios |
| GitHub/GitLab | Existing adapters | Snapshot and parity E2E |
| Channels | Frozen | Resume after platform gates |
| Docker/CI | Green baseline | Preserve through every phase |

No unresolved architectural decision blocks continued Phase 1. Universal
knowledge graphs, mandatory code indexes, organization packs, executable
validators, durable multi-instance queues, autonomous final approval, and new
channels remain explicitly deferred.

### Runs / Status / Findings

| Review | Runs | Status | Findings |
| --- | ---: | --- | --- |
| Engineering plan review | 2 | clean | Scope corrected from universal graph to run-scoped review evidence |
| Architecture | 2 | approved | Canonical state, snapshots, plans, evidence graph, memory, bounded loop |
| Code quality | 2 | approved | Extract CorpusGraph by equivalence; add no parallel graph engine |
| Tests | 2 | approved | Added graph authority, path, isolation, budget, replay, and ablation gates |
| Performance/security | 2 | approved | Run-scoped bounds, revision trust, fallback, HIL, and memory limits |

VERDICT: CLEARED FOR CONTINUED PHASE 1 IMPLEMENTATION

NO UNRESOLVED DECISIONS
