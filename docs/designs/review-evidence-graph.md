# Design: Review Evidence Graph

Status: APPROVED FOR IMPLEMENTATION PLANNING
Date: 2026-08-28

## Product Purpose

The Review Evidence Graph explains how 7review moved from a change to a review
outcome:

```text
change -> scope -> applicable method/rule -> selected evidence
       -> hypothesis -> observation -> finding -> human outcome -> learning
```

It improves review precision, context selection, auditability, and memory
feedback. It is not a general knowledge graph, a complete model of a codebase,
or a replacement for static analysis.

## Existing Foundation

`agent/pipeline.CorpusGraph` already builds document-section nodes and typed
edges for hierarchy, identifiers, interfaces, data, components, and ownership.
Bounded traversal expands evidence and records selection reasons. The first
implementation extracts and preserves this behavior instead of creating a
parallel graph engine.

The missing connection is between corpus selection and the rest of the run:
active skills, required checks, memory recalls, tool observations, hypotheses,
citations, validation, HIL decisions, and final outcomes.

## Run-Scoped Model

Nodes use stable run-local IDs and reference canonical artifacts:

- `change`, `file`, `domain`, `module`, `feature`, `risk`;
- `pack`, `method`, `skill`, `rule`, `contract`, `required_check`;
- `corpus_section`, `memory_record`, `tool_request`, `observation`;
- `hypothesis`, `citation`, `candidate`, `finding`, `hil_decision`,
  and `outcome`.

Relations are namespaced and allowlisted:

- planning: `classifies_as`, `applies_to`, `activates`, `requires`;
- selection: `selected_because`, `matches`, `recalled_because`;
- investigation: `investigates`, `observes`, `supports`, `refutes`;
- validation: `cites`, `violates`, `duplicates`, `downgrades_to`;
- feedback: `accepted`, `rejected`, `revised`, `learned_from`,
  `contradicts`, `supersedes`.

Every relation contains subject, predicate, object, source artifact reference,
repository identity, base/head revision, authority class, extraction method,
confidence, selection reason, and creation time. Authority and confidence are
different: high model confidence never outranks repository truth.

## Authority And Proof Paths

The non-overridable authority order is:

```text
repository truth > approved decision > implementation evidence
                 > design/supporting context > approved memory > inference
```

A confirmed finding requires a bounded proof path containing:

1. an addressable changed file and line;
2. the applicable source-of-truth rule, contract, or deterministic invariant;
3. concrete evidence that the changed behavior violates it;
4. a citation validated against selected content.

Memory and inferred relations can select context, support or refute a
hypothesis, or request a human check. They cannot complete a confirmed path
alone. Missing, contradictory, or over-budget paths downgrade or reject the
candidate deterministically.

## Lifecycle

```text
CompilePlan
    -> seed change/scope/method/check relations
GatherEvidence
    -> project CorpusGraph + recalled memory
Investigate
    -> append hypotheses, requests, observations, support/refutation
Validate
    -> build and verify candidate proof paths
HIL
    -> append accepted/rejected/revised outcomes
Memory
    -> derive separately approved learning proposals
```

`review.Source` persists nodes, relations, proof paths, and graph budgets with
the run. The append-only run ledger is authoritative. Adjacency maps and
operator views are derived and rebuildable; V2 adds no graph database or runtime
service.

The controller compiles initial seeds and mandatory checks from the
`ReviewPlan`. The agent may request a bounded read-only expansion only for an
open hypothesis. Relation allowlists, maximum depth, nodes, bytes, elapsed time,
and repository/revision scope are enforced outside the model.

## Memory Integration

The graph supplies structured outcomes to the governed memory engine. Accepted,
rejected, revised, duplicate, and no-finding results retain links to the run,
finding, evidence path, policy fingerprint, and revisions.

MemPalace remains a semantic index over stable governed-memory IDs. A recalled
ID is rehydrated, status-checked, scope-filtered, and then added as a supporting
graph node. Superseded, contradicted, expired, missing, or cross-repository
records are discarded before prompt construction.

## Model-Facing Serialization

The graph, run ledger, APIs, persistence, webhooks, and model outputs retain
typed Go/JSON contracts. A separate `PromptEncoder` optimizes bounded model
input projections:

- raw patches, contracts, and design documents remain labeled text;
- compact JSON is the default structured representation;
- uniform arrays of evidence nodes, relations, findings, citations, file
  summaries, tool observations, CI results, and memory recall may use strict
  TOON;
- TOON is selected only when the exact target-model tokenizer shows at least
  the configured saving threshold (10% by default) and evaluation shows no loss
  of review quality or schema fidelity;
- encoded data must round-trip to the same JSON data model, with strict
  validation and deterministic compact-JSON fallback.

TOON is never a public or persisted contract. Nested, sparse, or irregular
structures remain compact JSON when TOON provides no measured benefit.

## Optional Code Intelligence

The default system remains language-agnostic and uses SCM metadata, paths,
diffs, repository documents, structured contracts, and read-only tools.

SCIP, CodeQL, Joern, or language-specific indexers may later be exposed through
the capability registry. Setup may detect them and offer explicit opt-in
installation or connection instructions. They are optional enrichers, not
dependencies of baseline review.

Adapter output must attest repository, revision, tool version, configuration,
and index digest. Mismatched or unhealthy indexes are rejected entirely and the
run falls back to baseline evidence. Specialized results enter as implementation
evidence or supporting paths under existing authority rules.

## Evaluation And Failure Modes

Golden tests first prove exact equivalence with current `CorpusGraph`
selection. New deterministic fixtures cover:

- required and forbidden relations and proof paths;
- authority conflicts and memory-only confirmation attempts;
- cycles, duplicate edges, hub expansion, and budget exhaustion;
- stale revisions, cross-repository leakage, malformed references, and replay;
- rejected findings that become feedback without becoming conventions;
- compact JSON versus TOON on varied shapes, exact tokenizers, semantic round
  trips, fallback reasons, and finding-quality parity;
- missing or unhealthy optional capabilities with baseline fallback.

Quality evaluation measures evidence-path precision/recall, citation validity,
unsupported finding rejection, useful evidence gain, context bytes, graph
construction latency, false-positive recurrence, and memory-on/off quality
delta. More nodes or edges are not success metrics.

## Research Basis

- W3C PROV separates entities, activities, derivation, responsibility, and
  trust-oriented provenance.
- SARIF models findings, related locations, and code-flow paths without
  requiring one universal code graph.
- SCIP, CodeQL, and Joern demonstrate that precise symbol or data-flow analysis
  belongs in specialized code-intelligence providers.
- Graphiti demonstrates temporal memory relations and hybrid retrieval, but
  does not replace repository authority.

References:

- https://www.w3.org/TR/prov-dm/
- https://www.w3.org/TR/prov-constraints/
- https://docs.oasis-open.org/sarif/sarif/v2.1.0/os/sarif-v2.1.0-os.html
- https://github.com/scip-code/scip
- https://codeql.github.com/docs/writing-codeql-queries/creating-path-queries/
- https://docs.joern.io/code-property-graph/
- https://help.getzep.com/graphiti/getting-started/overview
- https://github.com/toon-format/spec
- https://github.com/toon-format/toon

## Non-Negotiable Invariants

- The graph explains a review; it does not redefine repository truth.
- Every relation is repository- and revision-scoped with artifact provenance.
- Traversal is relation-allowlisted and bounded by depth, size, and time.
- Memory or inferred relations cannot independently confirm a finding.
- Optional analyzer failure reduces enrichment, never mandatory checks.
- Human feedback is preserved without silently changing policy or methodology.
