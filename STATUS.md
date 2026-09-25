# 7review Status

Updated: 2026-09-25
Current phase: PHASE 1 COMPLETE; PHASE 2 NOT YET STARTED.

## Current Direction

The target is an adaptive, team-configurable review engine shared by local
changes, GitHub/GitLab PRs/MRs and non-interactive CI. Design contracts guide
intended behavior, concrete code invariants can establish ordinary defects,
and effort follows impact/uncertainty. Humans retain merge authority while
trusted repository policy can enforce blocking quality gates.

The canonical set is now:
- [ARCHITECTURE.md](ARCHITECTURE.md): product, ownership, decisions and rationale.
- [SPEC.md](SPEC.md): behavioral contracts, subsystem detail and acceptance.
- [ROADMAP.md](ROADMAP.md): immediate queue and conditional implementation.
- This file: recorded facts, verification scope and remaining blockers.

The user accepted the complete design and authorized Phase 1 implementation on
2026-09-25. This authorizes work; it does not mark target behavior implemented.

## Recorded Runtime Baseline

These statements summarize prior implementation work at baseline `11f3f55`;
no fresh source audit, Go suite, Compose smoke or hosted CI run was performed
during consolidation.

- GitHub/GitLab webhook and authenticated manual intake, bounded in-process workers,
  SCM metadata/diffs/discussions and provider publishing adapters exist.
- Source normalization, document-corpus selection, authority/citation validation,
  portable skills, model-role routing and governed read-only tools are existing
  primitives, not evidence that the new resumable scheduler is implemented.
- The existing lifecycle publishes a draft, gates final publication on human
  approval and couples approved memory writeback to that workflow.
- Input profiles and CLI/TUI/chat operator surfaces exist. WhatsApp, Telegram and
  SimpleX channel foundations exist; full live-provider qualification is pending.
- The packaged runtime uses Headroom and MemPalace sidecars. Optional enrichment
  in the target does not make those dependencies optional in the current code.
- Existing finding validation favors verifiable source-backed, positioned findings;
  speculative/weak concerns remain notes or human-check material. This is not the
  proposed CI quality-gate evaluator or full invariant-based target contract.

## Target Implementation Gaps

| Target | Current evidence / gap |
| --- | --- |
| Immutable attempts, full freshness and scoped resume | Legacy per-change stores/reruns do not provide these contracts |
| Transactional accepted work and separate effects | In-process accepted jobs can be lost on restart; full recovery/outbox semantics remain proposed |
| Adaptive methods, triage and independent omission review | Existing skills, roles and tools are reusable; new controller/policy contracts not implemented |
| CI-native runner, native feedback and gate evaluator | Specified with autonomous/service lifetimes, fork trust and service-owned publication; not qualified or delivered by these docs |
| Governed memory and evidence graph | Existing recall/corpus are foundations; lineage, revocation and activation semantics remain target |
| Optional Headroom/MemPalace/code intelligence | Setup and required-capability behavior still need implementation and tests |
| TOON optimization | Proposed model-input-only optimization; no measured savings or quality parity |

## Phase 1 Implementation Evidence

Completed on 2026-09-25 in commit `1bb0f2a`:

- Added validated canonical contracts for change/snapshot/attempt identity,
  execution context, events, observations, execution and human decisions, checks,
  assessment, coverage, gate and delivery projections.
- Added the pure attempt transition table and a validated canonical-context
  constructor while preserving `NewContext` for legacy callers.
- Removed shadow copies of corpus/skills/findings/reports/SCM and run metadata from
  `review.Context`; `review.Source` is now authoritative for those artifacts.
- Added defensive `Source`/`Context` cloning at memory and file run-store boundaries.
- Added deterministic contract cases tied to SCHEMA-04/05/06, LOOP-05, PUB-01 and
  scenarios S13/S14/S19/S24/S30/S45, plus store aliasing regression coverage.

Verification: `go test ./...` passed across all Go packages;
`go test -race ./agent/review ./agent/pipeline` passed. No external API, model,
database, Docker or hosted CI qualification ran. The target scheduler, policy V2,
durable attempt store and native gate publication remain future phases.

ENG-D7/D8 select a PostgreSQL budget authority/action journal inside the server
for coordinated team/CI use. SQLite remains a candidate only for autonomous local
accounting. Neither has been installed or implemented in this phase. No mandatory
graph database, new channel or autonomous merge is added.

## September 25 Contract Reconciliation

- User-approved ENG-D1 through ENG-D13 are recorded in ARCHITECTURE. Earlier
  candidate D06 is superseded for coordinated accounting, not silently approved.
- SPEC now defines loop eligibility/progress, per-line suspension, protected
  coverage, fixed-period budget arithmetic, action/receipt fields, transactional
  operations and conservative recovery. These representations are the accepted
  implementation contract.
- All 50 ENG acceptance cases were moved into the canonical SPEC alongside
  S01-S60: 110 cases specified, none executed as target acceptance tests.
- DOC-01 through DOC-06 are closed at design level in SPEC sections 20-25:
  public schemas, complete effect/memory lifecycles, legacy migration, measurable
  qualification objectives, two-mode CI publication and clause traceability.
  Their implementation and qualification evidence remains absent.
- Automated CI under setup grants, truthful incomplete coverage and scoped method
  conflicts are retained. Optional isolated runtime verification is product scope;
  safe sandbox contracts and qualification remain unfinished.
- README/ROADMAP distinguish current runtime from these targets. AGENTS.md was
  preserved as requested; its existing runtime sidecar guidance is not a target
  architecture decision.
- This pass changes documentation only. No runtime tests, dependency installation,
  migration, deployment, commit or push is claimed.
- gstack-guided primary review covered architecture, code-quality boundaries,
  future test coverage and performance risks. A separate same-family agent found
  three contract inconsistencies: billable retry identity/period, historical
  currentness and delegated obligation replacement. All three were corrected
  and rechecked as resolved. This is not a cross-model or whole-system sign-off.
- The final static pass found 62 valid local links/anchors, 60 S cases plus 50
  unique ENG cases, twelve numbered architecture sections and balanced code
  fences. All 16 Mermaid blocks rendered in headless Chromium after the closure
  edits; `git diff --check` passed and AGENTS.md remains unchanged.
- An independent same-family whole-system review found five concrete issues, then
  two residual issues after the first correction. After schema/config alignment,
  publication recovery, memory-state unification, separate detection/escalation
  scoring, atomic traceability and compensation variants were fixed, its final
  recheck found no blocking design contradiction. This is not cross-model evidence.
- No Go suite, live SCM/model qualification or crash tests ran during design. The
  design is accepted for implementation; it is not an implemented or
  production-ready system.

## Historical Verification

Runtime packaging was recorded complete on 2026-08-27: pinned sidecars, hardened
containers, readiness, embedded assets, isolated Compose smoke cleanup, real
Headroom reduction and MemPalace semantic write/recall. Earlier green Go/Compose
results are historical, not current CI status.

A prior live GitLab smoke exercised acceptance, enrichment, context/skills,
model tools, findings and inline/draft publication. It did not establish every
model finding's correctness or prove the full final-publication/learning lifecycle.
Current hosted CI health has not been queried in this documentation phase.

Known limits remain provider/model variance, speculative findings, live channel
callbacks, accepted-work durability, uncertain external effects, operator recovery
and unmeasured review precision/recall. This is a packaged development baseline,
not a production-readiness claim.

## Historical Design Validation

- Product review applied gstack methods with separate Codex contexts. Six
  subagent and five CLI findings were considered; no Claude/cross-provider
  consensus or complete autoplan approval was established.
- DX used a primary design walkthrough, not an independent usability test.
- Six independent engineering findings were resolved at contract level and
  rechecked: full freshness, derived-content revocation, grant precedence, total
  cost reservation, parallel reducer fencing and immutable publication versions.
  Their rationale and scenario mappings are in ARCHITECTURE.
- The later primary-agent consistency pass brought CI into product journeys,
  ownership, interfaces, policy, recovery, native effects and implementation
  slices. It distinguished assessed-with-violations from incomplete review,
  job-local receipts from durable handoff and comments from check/artifact grants.
- The expanded CI scope and consolidation later received a same-family independent
  whole-system review and explicit user acceptance. Cross-model review remains
  unclaimed and is not an implementation prerequisite.

The previous pre-consolidation static pass checked 13 documents, 61 local links,
52 scenario IDs and 23 requirement mappings. This is historical artifact
verification, not evidence that the scenarios ran.

## Integration And Documentation Clarification

Official Greptile and CodeRabbit documentation was consulted on 2026-09-12 to
distinguish installed repository review from standalone CI execution. ARCHITECTURE
records the sourced comparison and deliberate differences, not equivalence claims.
SPEC now includes I01-I08: installation, permissions, triggers, incremental work,
conversation commands, finding lifecycle, native gate mappings and compatibility.
S53-S60 specify their qualification; none has run.

The documentation uses a tailored arc42 coverage map, C4 context/deployment
abstractions, a glossary and BCP 14 normative language. This is a documented
method, not external certification or proof that every contract is correct.

## Consolidation Verification

The design is consolidated from ten design notes and PENDING into the four
canonical root documents. R01-R23, D01-D13, J01-J10, the original S01-S52 definitions,
future test names and requirement mappings are retained. S53-S60 add installed-SCM
qualification, giving 60 specified cases in total. SPEC sections 1-16
keep their numbers; graph/memory detail and verification registries are included
rather than silently dropped. Research and intermediate reviews are synthesized
into rationale and this bounded evidence record, not competing specifications.

Static transfer checks confirmed all original scenario bodies and R/J/D rows were
preserved. Link/anchor validation passed for the four root documents and README
(53 local references); all 60 cases have the required fields and future test
names, and the 23 requirement mappings match their scenario declarations.
No references to the removed design files remain outside excluded Git/dependency
directories. `git diff --check` passed. These are artifact checks only.
No candidate scenario, runtime test, hosted SCM workflow, benchmark, dependency
installation, migration, deployment, commit or push is implied by this change.
AGENTS.md remains untouched.

## Historical Whole-Document Quality Audit

A primary-agent audit covered ARCHITECTURE, SPEC, ROADMAP, STATUS and README.
It did not rerun independent gstack engineering review. The previous adapted
standards map and successful link checks were insufficient to establish that
the architecture/specification were implementation-ready.

ARCHITECTURE now uses the twelve arc42 sections explicitly, separating context,
logical containers, internal components, runtime sequences and physical placement.
SPEC retains behavioral section and requirement/scenario IDs, with Mermaid views
for lifecycle, delivery, evidence and memory. References and diagrams complement
the normative text; they do not replace missing schemas or transitions.

That audit recorded six open precision findings as DOC-01 through DOC-06: schema
completeness, operational lifecycles, legacy mappings, quality/recovery objectives,
CI publication ownership and clause-level verification. The current revision
closes those design questions in SPEC sections 20-25. This supersedes their former
open status but does not retroactively turn the historical audit into runtime proof.

The README now introduces the target and contrasts it with existing runtime
instructions. No proposed command or endpoint is presented as executable.
Remaining documentation work is substantive, not just diagram formatting.

Audit verification: the 12 numbered architecture sections, 64 local links/anchors,
60 scenario definitions and 23 requirement mappings passed static checks. Original
R/J/D rows were preserved. All 16 Mermaid blocks across ARCHITECTURE, SPEC and
README rendered with the installed offline Mermaid bundle in Chromium; selected
context, container, sequence, lifecycle and memory renders were visually inspected.
Syntax errors and unreadable crossing/overwide views found during verification
were corrected. `git diff --check` passed. This is primary-agent artifact
verification, not an independent standards audit or runtime test. DOC-01 through
DOC-06 were closed later in the current revision. No runtime/dependency changes
were made.

## Remaining Gates

1. Continue Phase 2 by wiring verified snapshots and compiled policy into intake;
   finish trigger, delegation and scoped quality-gate semantics without replacing
   the characterized legacy runtime prematurely.
2. Follow ROADMAP's staged migration and preserve accepted design decisions unless
   implementation evidence requires an explicit amendment.
3. Execute the remaining Phase 2 scenarios. S06 now has a deterministic named
   assertion for stale test proof; the other specified scenarios remain unproven.
   Related legacy tests do not count as their execution.

Current Phase 2 evidence (2026-09-25): `agent/review` validates immutable snapshot
attestations and readiness provenance. `agent/policy` rejects unknown/missing or
duplicate configuration, binds authority to an attested base, enforces runtime
ceilings and resolves packs deterministically. `7review policy validate|explain`
provides offline preview only. `go test ./...` and
`go test -race ./agent/review ./agent/policy` pass. No intake, SCM status or CI
adapter consumes V2 policy yet.

Do not resume development automatically or label the redesigned system complete
because its documents are consolidated.
