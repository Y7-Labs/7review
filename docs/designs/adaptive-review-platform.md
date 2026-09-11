# Design: Adaptive Review Platform

Status: APPROVED FOR IMPLEMENTATION PLANNING
Date: 2026-08-28
Branch: `main`
Supersedes: `adaptive-review-policy-engine.md`

## Product Direction

7review should become a repository-governed review platform, not a fixed review
bot and not merely a YAML policy resolver. For each change it must answer four
questions explicitly:

1. What changed and what risks does it create?
2. Which repository-owned review methods and rules apply?
3. Which evidence, tools, models, and deterministic checks should execute?
4. Which outputs may be drafted, published, approved, or remembered?

The answer is a compiled, immutable, explainable `ReviewPlan`, executed through
a run-scoped `ReviewEvidenceGraph`. The existing pipeline, corpus graph,
skills, tools, model orchestrator, HIL flow, channels, run store, Headroom, and
MemPalace remain useful primitives. They are recomposed around the plan and
evidence graph instead of being replaced.

## Current System Assessment

### Strong Primitives To Preserve

- `review.Source` already captures request, SCM context, diff, evidence, skills,
  model output, findings, HIL state, report, and run metadata.
- GitHub and GitLab adapters normalize provider data behind shared interfaces.
- The corpus graph selects repository evidence and records authority and match
  reasons.
- `SKILL.md` procedures carry methods, required checks, risk tiers, and allowed
  read-only tools.
- Deterministic validation separates confirmed findings, human checks, notes,
  and questions before publication.
- The model orchestrator has role routing, fallback chains, bounded parallelism,
  and multiple providers.
- Run persistence, timelines, operator tools, CLI/TUI/chat, HIL actions, approval
  channels, and memory writeback provide a real control plane.
- Docker Compose and CI already package and exercise the Go service plus
  Headroom and MemPalace sidecars.

### Structural Problems To Fix

1. **Duplicated run state.** `review.Context` embeds `Source` but duplicates
   request, diff, skills, findings, report, and metadata. Pipeline code manually
   synchronizes both forms, which makes stale state possible.
2. **Monolithic orchestration.** `agent/pipeline/pipeline.go` owns acquisition,
   selection, prompting, tool rounds, parsing, validation, publication, HIL,
   reruns, and reporting. These are valid capabilities with weak stage
   boundaries.
3. **Mixed configuration concerns.** The input profile combines deployment
   inputs, global review behavior, memory, channels, and publishing. Repository
   methodology and operator/runtime configuration need separate trust models.
4. **Unverified repository context.** Corpus discovery scans `CORPUS_ROOT`, but
   no contract proves that this filesystem matches the reviewed repository and
   trusted base revision.
5. **Selection is distributed.** Skills, corpus, validation thresholds,
   publishing behavior, and tool availability are decided in separate places,
   so the effective strategy is difficult to explain before model execution.
6. **Tool registration can drift.** Catalog metadata and executor dispatch are
   separate static definitions. Model read tools and operator mutation tools
   also need an explicit capability boundary.
7. **Quality is not yet measurable as a product.** Unit and integration coverage
   is substantial, but there is no first-class scenario/evaluation contract for
   strategy accuracy, finding quality, provider parity, or repeated model runs.
8. **Review reasoning is not connected.** Corpus selection reasons, skills,
   observations, citations, findings, HIL outcomes, and memory are neighboring
   artifacts rather than one bounded explanation of why a finding exists and
   what the system learned from it.

## Architecture Principles

- Repository intent is declarative; hard safety invariants remain in Go.
- Policy used for a PR/MR comes from its trusted target/base revision.
- The model never chooses its own policy, permissions, evidence authority, or
  publication rights.
- GitHub and GitLab differences stop at adapter boundaries.
- Every effective decision has provenance and a deterministic explanation.
- A review plan is reproducible from normalized input plus a trusted repository
  snapshot.
- Existing behavior migrates through compatibility adapters; no big-bang
  rewrite.
- The evidence graph models one review, not the entire software world. It links
  changes, applicable methodology, evidence, findings, and outcomes.
- Graph relations are derived explanations with provenance. They never replace
  repository artifacts, the effective plan, or the append-only run ledger.
- Headroom is a context optimization and MemPalace is approved historical
  memory storage. A provider-neutral 7review memory engine owns semantics,
  governance, and evaluation. Neither sidecar may redefine repository truth.
- Channels are control-plane transports. They do not influence review strategy.

## Target Architecture

```text
                    CONTROL PLANE
 webhooks / CLI / TUI / chat / channels / operator tools
                         |
                         v
  +--------------------------------------------------------+
  |                     REVIEW ENGINE                      |
  | acquire -> plan -> evidence graph -> bounded review    |
  |          -> validate -> draft -> approve -> publish    |
  +--------------------------------------------------------+
       |              |               |              |
       v              v               v              v
 Repository       Decision      Review Evidence    Run ledger
 snapshots        engine            Graph         + artifacts
       |              |               |
 GitHub/GitLab    policy packs   corpus, skills, memory,
 or mounted FS    + classifier   tools, hypotheses, findings
                                      |
                         Headroom + MemPalace indexes
```

### 1. Domain And Run Ledger

`agent/review` remains the dependency-light domain package. `review.Source`
becomes the only mutable aggregate during execution and the persisted audit
snapshot after each stage. `review.Context` is reduced to synchronization and
temporary execution helpers, then removed after callers migrate.

The run state machine becomes explicit:

```text
accepted -> acquiring -> planned -> reviewing -> validating -> draft
                                                        |
                                                        v
completed <- publishing <- approved <- awaiting_approval
    ^                         |
    +------- failed / cancelled / superseded -----------+
```

Every state transition is idempotent, timestamped, and linked to plan
fingerprint, base revision, head revision, provider delivery, and publication
markers.

### 2. Repository Snapshot Boundary

Introduce a provider-neutral `RepositorySnapshot` backed by Go `fs.FS`:

```go
type RepositorySnapshot interface {
    Identity() RepositoryIdentity
    Revision() string
    FS() fs.FS
    Close() error
}
```

The acquisition layer can open a verified mounted checkout or materialize a
read-only snapshot through GitHub/GitLab. Policy, repository methods, rules, and
corpus all read from the same trusted base snapshot. Changed patches and head
metadata remain supplied by SCM enrichment. A policy file changed in the head is
review evidence only; it cannot govern its own review.

### 3. Decision Engine

The decision engine contains four deterministic components:

- **Classifier:** derives provider, paths, file kinds, domains, modules,
  features, change types, labels, branches, and bounded risk signals.
- **Policy loader:** validates `.7review/review.yaml`, packs, methods, and rules
  from the trusted snapshot.
- **Resolver:** matches packs, expands acyclic inheritance, applies explicit
  merge laws, and rejects ambiguous or unsafe composition.
- **Compiler:** emits `review.Plan`, including classification, methods, rules,
  evidence budget, validators, tools, model role, publishing policy, conflicts,
  provenance, trusted revision, and fingerprint.

Runtime profiles remain operator-owned deployment configuration. Existing
profile V1 behavior is translated by a `LegacyPlanCompiler` until migration is
complete.

### 4. Repository Review Contract

```text
.7review/
  review.yaml
  packs/*.yaml
  methods/*/SKILL.md
  rules/*.{md,yaml,json}
  scenarios/*/scenario.yaml
```

Packs may scope behavior by path, excluded path, provider, label, branch, file
kind, change type, domain, module, feature, and deterministic risk signal.
Built-ins use `builtin/<name>` and repository methods use `repo/<name>` to
prevent shadowing.

Collections use stable union. Required beats optional. Exclusions beat
inclusions at equal scope. Stricter safety and approval requirements win.
Budgets use explicit replacement. Conflicting scalar decisions without a schema
winner fail planning. Arbitrary scripts, remote downloads, model-selected
policy, native plugins, CEL/Rego, and WASM are excluded from V2.

### 5. Review Evidence Graph

The graph is the bounded, explainable context for one review run. It is not a
general knowledge graph, a complete code property graph, or a new database
service. It starts from the existing `CorpusGraph` and connects:

- change, file, domain, module, feature, and risk classification;
- active pack, method, skill, rule, contract, and required check;
- selected corpus section, memory recall, tool request, and observation;
- hypothesis, citation, candidate finding, validation result, HIL decision, and
  final outcome.

Typed relations include `applies_to`, `selected_because`, `requires`,
`investigates`, `supports`, `refutes`, `cites`, `accepted`,
`rejected`, `learned_from`, `contradicts`, and `supersedes`. Each
relation carries source artifact references, repository and revision identity,
authority, confidence, and a deterministic explanation.

`review.Source` persists selected graph nodes, relations, and proof paths as
run artifacts. The append-only run ledger remains authoritative; graph views
are rebuildable. A confirmed finding needs a path from changed code through an
applicable rule or contract to concrete violation evidence. Memory and
model-inferred relations may support or refute a hypothesis but cannot complete
that authoritative path alone.

The initial graph remains document- and review-centric. SCIP, CodeQL, Joern, or
language-specific indexes may later enrich a run as optional read-only tools.
Setup can detect and offer these capabilities explicitly, but absence or stale
revision identity falls back to the generic path/diff/corpus flow. See
`review-evidence-graph.md`.

### 6. Governed Memory Engine

Memory is a typed, evaluated learning subsystem, not a report archive. The
append-only run ledger preserves complete review outcomes; `agent/memory`
derives compact semantic, episodic, feedback, procedural-candidate, and
operational records with scope, provenance, authority, confidence, lifecycle,
and supersession links.

Recall is compiled from the effective plan and trusted repository identity,
combines exact and semantic retrieval, and enters the evidence graph with its
scope and provenance. Human outcomes add `learned_from`, `contradicts`, or
`supersedes` links to governed memory proposals.
Repository truth always outranks recalled knowledge. Writes are separately
approved after HIL, idempotent, redacted, and contradiction-aware. Learned
procedures can only be promoted into repository policy or `SKILL.md` through a
normal PR/MR. MemPalace remains the initial replaceable semantic backend. See
`memory-engineering.md` for the full contract.

### 7. Staged Review Engine

The existing pipeline migrates to an engine with explicit stage contracts:

```text
Acquire -> CompilePlan -> GatherEvidence -> BuildEvidenceGraph -> PrepareContext
        -> ExecuteReview -> Validate -> ComposeDraft
        -> AwaitApproval -> PublishFinal -> WriteMemory
```

Stages receive `*review.Source`, produce typed artifacts, and append run events.
They do not mutate unrelated fields. The engine owns ordering, retries,
cancellation, persistence, and transition guards. This keeps one obvious
workflow while allowing deterministic stages to be tested without models or
networks.

### 8. Skills, Evidence, Tools, And Models

- Skills become plan-selected review methods, not an independent keyword-only
  decision system. The current loader and metadata parser are retained.
- Corpus graph selection becomes the first evidence-graph projector and keeps
  current authority, citation, trace, scoring, and anti-noise behavior.
- Skills, tools, memory, hypotheses, and findings append typed graph relations
  instead of inventing a second selection model.
- Tool descriptors and handlers are registered together. Capabilities declare
  stage, side effects, approval requirement, input schema, and actor scope.
- Model-accessible tools are read-only and intersect the plan allowlist with the
  hard runtime allowlist. Operator mutations remain on a separate actor surface.
- Optional code-intelligence tools are capability adapters, not mandatory
  infrastructure. Their outputs must attest repository, revision, tool version,
  and configuration before entering the graph.
- A model-facing `PromptEncoder` projects typed context without changing its
  semantics. Raw diffs and source documents remain labeled text; compact JSON
  is the structured default. Strict TOON is eligible only for uniform
  collections when the exact model tokenizer demonstrates a configurable
  minimum saving (10% by default) with no quality regression. APIs,
  persistence, webhooks, tool schemas, and model outputs remain JSON.
- The orchestrator remains role-based. Plans select a semantic role or bounded
  quality tier, never credentials or unrestricted provider details.

### 9. Bounded Review Agent Loop

7review uses a deterministic workflow with one bounded review agent inside it.
It does not use an open-ended autonomous loop or a multi-agent swarm. The agent
is useful only after policy compilation and initial evidence selection, when it
must decide which missing read-only observation would confirm or refute a review
hypothesis.

The current three-round tool loop is retained as a compatibility seed, then
upgraded to an explicit trajectory:

```text
compiled plan + diff + initial evidence graph
              |
              v
       propose hypotheses
              |
              v
     map required checks/gaps <-------------------+
              |                                   |
              v                                   |
 request bounded read tools -> observe -> append evidence relations
              |                                   |
              +---- progress and budget check ----+
              |
              v
 synthesize candidates with bounded proof paths
              |
              v
 deterministic validation -> selective verifier -> draft/HIL
```

Each hypothesis identifies the changed location, applicable method/rule, claim,
required evidence, current support state, confidence, and outcome. States are
`open`, `supported`, `refuted`, `insufficient`, or `duplicate`. Tool requests
must reference an open hypothesis or an uncovered required check. A model may
not turn an unlinked observation directly into a publishable finding.

The controller owns graph expansion. Initial seeds and mandatory paths are
compiled from the plan and diff. The agent can request an allowlisted, budgeted
expansion only for an open hypothesis; it cannot issue arbitrary graph queries
or promote a weak relation.

The controller, not the model, enforces:

- maximum rounds, tool calls, observation bytes, elapsed time, and model calls;
- plan tool allowlist intersected with the hard runtime allowlist;
- deduplication of identical requests and findings;
- required-check coverage and method completion;
- cancellation when a newer head revision supersedes the run;
- no-progress detection when two rounds add no evidence, coverage, or hypothesis
  state change;
- escalation instead of guessing after tool errors, missing trusted evidence,
  contradictory authoritative sources, or exhausted high-risk checks.

Default V2 budgets remain conservative: at most 3 investigative rounds and 12
read-only tool calls per review, with lower budgets for docs-only or low-risk
plans. Packs can tighten budgets or request bounded increases; they cannot remove
hard ceilings.

After exploration, the agent emits candidates only. Existing deterministic
location, citation, authority, confidence, and publication validators run first.
A separate verifier model is optional and selective: it examines only high-risk,
high-severity, or borderline candidates, may accept, downgrade, reject, or
request one bounded observation, and may not invent new findings. Human approval
remains the final gate.

Parallelism is domain or diff-batch fan-out under one shared plan. Workers return
hypotheses and coverage, not final comments. A deterministic reducer merges,
deduplicates, checks cross-file interactions, and runs the final validation path.

Persisted trajectory data includes tool request reasons, bounded observations,
hypothesis state changes, required-check coverage, budgets, stop reason, and
verifier decisions. It excludes hidden chain-of-thought. This makes behavior
replayable and evaluable without storing private model reasoning.

### 10. Control Plane And Integrations

`agent/app` continues to own HTTP authentication, webhooks, bounded work intake,
and composition. `agent/channel` remains transport-specific HIL messaging.
Provider adapters own normalization, snapshot acquisition, and idempotent
publication. CLI, TUI, and chat consume stable application DTOs instead of
duplicating domain representations.

The initial deployment stays single-instance and file-backed. Durable external
queues and multi-instance coordination remain a separate operational milestone;
the new architecture must not pretend current webhook acceptance is restart-safe.

## Scenario And Evaluation Platform

A scenario is a versioned logical change, not a permanent branch. It includes a
base fixture or revision, patch, request metadata, policy, expected plan,
expected/forbidden findings, lifecycle expectations, and cleanup rules.

Five gates are distinct:

1. Resolver tests: exact classification, matches, merge laws, provenance, and
   fingerprint, with no model or network.
2. Engine contract tests: stage transitions, evidence graph, tools, validation,
   HIL, publication, and memory using fakes.
3. Quality evaluations: repeated real-model runs scored by normalized findings,
   rules, locations, strength, citations, and forbidden topics.
4. SCM parity: the same logical scenario yields equivalent normalized inputs
   and plans on GitHub and GitLab.
5. Provider E2E: temporary PR/MR branches verify webhook, retry, draft, approval,
   final publication, memory, and cleanup.

The initial benchmark contains at least 24 scenario families covering clean
changes, contracts, auth, secrets, migrations, retries, frontend accessibility,
infra, tests, docs/generated files, mixed domains, feature rules, architecture
boundaries, policy conflicts, cycles, missing rules, self-weakening policy,
duplicate delivery, weak evidence, corpus noise, malformed model output, and
GitHub/GitLab parity. Each fixture also declares expected and forbidden graph
relations or proof paths where they affect review behavior.

Metrics include strategy precision/recall, finding precision/recall and false
positive rate, citation validity, downgrade correctness, parity, fingerprint
stability, parse repair, latency, model calls, duplicate publication count,
hypothesis yield, evidence gain per tool call, required-check coverage, no-progress
stops, escalation correctness, trajectory cost, useful/harmful memory recall,
false-positive recurrence, stale-memory rate, memory-on/off quality delta,
evidence-path precision, unsupported-path rejection, and graph context cost.

## Research Basis

The loop follows several converging findings from current agent and code-review
practice:

- Anthropic distinguishes fixed workflows from agents and recommends simple,
  composable patterns, well-designed tool interfaces, and evaluator-optimizer
  loops only when evaluation criteria are clear.
- OpenAI recommends layered deterministic and model guardrails, baselined evals,
  and human intervention for high-risk actions or repeated failure.
- SWE-agent shows that the agent-computer interface and tool ergonomics can be
  as important as the model for repository tasks.
- Current agent-evaluation guidance treats the full multi-turn trajectory as the
  unit of evaluation, not only the final response.
- GitHub and Microsoft now support repository-wide plus path-scoped review
  instructions, validating the need for global and domain-specific methodology.
- Recent code-review benchmarks emphasize that false positives are costly and
  require granular scoring against real PR feedback.
- Defect-focused review research favors multi-stage filtering and validation to
  suppress unsupported or low-value comments.
- W3C PROV supports provenance-centered trust decisions; SCIP, CodeQL, Joern,
  and SARIF show that precise code relations and result paths belong in
  specialized, optional capabilities rather than a universal graph.

References:

- https://www.anthropic.com/engineering/building-effective-agents
- https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents
- https://www.anthropic.com/engineering/writing-tools-for-agents
- https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/
- https://arxiv.org/abs/2405.15793
- https://docs.github.com/en/copilot/how-tos/use-copilot-agents/request-a-code-review/use-code-review
- https://github.blog/ai-and-ml/github-copilot/unlocking-the-full-power-of-copilot-code-review-master-your-instructions-files/
- https://arxiv.org/abs/2603.26130
- https://arxiv.org/abs/2603.11078
- https://openreview.net/pdf?id=mEV0nvHcK3
- https://www.w3.org/TR/prov-dm/
- https://github.com/scip-code/scip
- https://codeql.github.com/docs/writing-codeql-queries/creating-path-queries/
- https://docs.joern.io/code-property-graph/
- https://docs.oasis-open.org/sarif/sarif/v2.1.0/os/sarif-v2.1.0-os.html
- https://github.com/toon-format/spec
- https://github.com/toon-format/toon

## Non-Negotiable Invariants

- authenticated webhook and operator boundaries;
- authorized HIL sender and explicit final approval;
- base-revision policy authority;
- filesystem confinement and bounded input sizes;
- changed-file and changed-line validation;
- authority-aware citations for confirmed findings;
- bounded, revision-scoped evidence relations and proof paths;
- no inferred or memory-only path may confirm a finding;
- idempotent provider publication and memory write authorization;
- deterministic policy validation before any model call;
- deterministic, tokenizer-aware prompt encoding with semantic validation and
  compact-JSON fallback;
- no secrets in plans, run artifacts, logs, prompts, or memory.

## Scope Decisions

Build now: canonical source, repository snapshots, legacy plan compilation,
the review evidence graph, governed memory, declarative packs, deterministic
resolver, staged engine migration, explain and validate commands, scenario
harness, and provider parity.

Defer: a universal knowledge graph, mandatory whole-repository symbol indexing,
bundled SCIP/CodeQL/Joern execution, organization-wide remote pack registries,
executable validators, multi-tenant policy distribution, autonomous final
approval, new channels, external durable queue, multi-instance scheduling, and
WASM/CEL/Rego.

## Success Criteria

- Existing profile-driven behavior remains equivalent during migration.
- One trusted snapshot supplies policy, repository methods, rules, and corpus.
- Every run persists an effective plan and stable fingerprint before model use.
- Every accepted finding has an explainable, bounded evidence path persisted in
  the run; rejected findings retain feedback without becoming repository truth.
- Engineers can define global, domain, module, feature, and path-specific review
  behavior without changing Go code.
- `7review explain` shows why every pack, method, rule, tool, evidence root,
  validator, and publishing decision is active.
- Invalid or unsafe policy fails before review execution.
- The same logical change compiles equivalently on GitHub and GitLab.
- Scenario and live-model reports measure quality instead of comparing prose.
- Existing Docker, CI, HIL, publication, and memory gates remain green.
- Memory improves measured review quality without becoming evidence authority,
  and its semantic provider can fail without disabling mandatory review checks.
