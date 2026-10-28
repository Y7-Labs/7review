# Policy Execution Progress

Scope: G04 in [the target gap audit](implementation-gap-audit.md).
This is an incremental behavior ledger, not a completion declaration.

## Slice 1: Preview Does Not Enforce

- Red: `TestPipelinePreviewPolicyDoesNotActivateRequiredChecks` exposed an
  `effective-policy` activation created from preview-only checks.
- Green: persist explicit enforcement state and only activate enforced checks.
- Regression: `TestPipelineEnforcedPolicyActivatesRequiredChecks` proves the
  positive enforcement path is retained.
- Verification: full `go test ./...`; uncached pipeline/review package tests
  after adding the positive-path assertion.

## Slice 2: Enforce Model And Reasoner-Tool Capabilities

- Red: six read tools returned data despite denied `repo.read`; serial, streaming
  and parallel model paths invoked providers despite denied `model.review`.
- Green: persist and clone the effective capability intersection; require policy
  permission at model dispatch and both policy/skill permission for reasoner tools.
- Tool schemas in prompts and native requests reflect actual eligibility. A
  model-requested forbidden tool receives a denial without a result, including
  on followup rounds.
- Old persisted enforce-mode records without capability fields fail closed;
  preview/legacy execution remains observational/unrestricted by this policy layer.
- Verification: full `go test ./...`; race tests for pipeline, orchestrator,
  review and CI packages; persistence, positive permission and negative tests.
- Boundary: this slice guards the current reasoner tools and model orchestrator.
  It is not the future action-level permission system, nor a claim that all
  enrichment/publication effects are capability-governed.

## Slice 3: Empty Runtime Inventory Grants Nothing

- Red: bound compilation with no required capabilities widened an empty runtime
  inventory into all requested optional capabilities. Explicit empty inventories
  also incorrectly satisfied required capabilities in unbound compilation.
- Green: bound compilation always treats an absent inventory as empty, never
  wildcard. Only unbound previews with an unspecified inventory show requested
  capabilities without runtime intersection.
- The effective digest distinguishes an unbound preview from an explicit empty
  inventory; a failing identity regression exposed that collision too.
- Verification: full `go test ./...` and policy race tests.

## Slice 4: Execute Base-Owned Method Content

- Red: selected repository method instructions never reached the model; missing,
  malformed, traversal and unknown-builtin references still allowed inference.
- Green: both SCM and frozen-CI admission resolve the selected `repo/<name>`
  method from `.7review/methods/<name>/SKILL.md` at the attested base. Names use
  the existing lowercase skill-name grammar. No working-directory method fallback.
- Register the runtime-owned `builtin/correctness` method without repository
  shadowing. The selected method bodies, namespaced activations, content digests
  and source revisions reach execution and persisted projections. Aggregate method
  content is bounded to 1 MiB and contributes to effective policy identity.
- Enforce mode uses the policy-selected methods instead of the legacy heuristic
  skill plan; preview retains the legacy plan and exposes resolution/warnings only.
- A real Git fixture replaces the method on head and introduces a malformed
  legacy head skill. CI still executes the base method. CI no longer loads head
  legacy skills, and custom `--skills-path` overrides are explicitly rejected;
  the old default is accepted as a compatibility no-op.
- CI coverage must name the actual selected method: the obsolete synthetic name
  cannot satisfy a builtin method's obligation. Missing coverage stays incomplete.
- Checks declared by a selected method join both effective required checks and
  gate-required coverage, with explanation provenance; they cannot become optional
  just because the root configuration did not repeat them.
- Verification: full `go test ./...`, focused uncached method/CI tests and race
  tests for pipeline, CI, review and skills.
- Boundary: loading instructions and retaining model coverage are not proof of
  investigation completeness. The adaptive controller/evidence gates remain open.

## Slice 5: Strict Method Frontmatter

- Red: duplicate names/metadata, unknown fields and malformed YAML were accepted;
  `---` inside a quoted description incorrectly split the document.
- Green: policy-selected methods use the structured YAML decoder with known
  fields, duplicate-key rejection and line-delimited frontmatter. Legacy heuristic
  skills keep their historical parser; that path is not authority in enforce mode.
- Pipeline regressions prove invalid frontmatter stops before provider calls.
- Verification: uncached full Go suite, targeted race tests across changed
  behavior in skills/pipeline/CI/review/orchestrator, and `go vet ./...`.

## Slice 6: Obligation-Only Pack Inheritance

- Red: closed-schema decoding rejected all inheritance, preventing execution of
  the specified composition behavior. Negative tests assert graph-specific errors,
  not just any decoding failure.
- Green: optional `inherits` expands before matching, rejects missing parents,
  duplicate edges and cycles even in unmatched packs, and retains expansion bounds.
- Only obligations are composed. Matchers, priority and delegation are not
  inherited; ancestors still match independently. A child cannot weaken inherited
  risk/manual/independent-review restrictions or reopen a disjoint disclosure set.
- Diamond, unrelated-scope, repeated-compilation/input-immutability and delegated
  authority fixtures exercise behavior beyond the happy path.
- Verification: policy tests, full Go suite and policy race tests.

## Slice 7: Base-Bound Pack References

- Red: imported parent packs could not control execution; CLI `policy validate`
  falsely reported an unresolved/missing pack as valid.
- Green: explicit root `pack_refs` reads closed versioned pack documents at the
  same repository/base as the root, validates the complete registry before matching
  and then expands inheritance. No glob or recursive/remote import fallback.
- Unresolved compilation, unsafe paths, duplicate keys/IDs, unknown fields,
  incomplete/version-mismatched documents, oversized inputs and cancellation are
  covered. Referenced content/provenance contributes to effective identity and
  persists with the base revision; input policy is not mutated.
- CLI requires explicit `--repository-root` and confines local preview reads with
  `os.Root`; a real outside-root symlink test must fail without emitting success.
- Verification: full Go suite and targeted policy/pipeline/CLI race tests.

## Slice 8: Selected Base-Owned Rules And Gate Identity

- Red: inherited rule statements never reached investigation; missing rules in
  unmatched packs could not be validated through the declared reference path.
- Green: closed RuleV1 JSON/YAML/Markdown documents are read at the trusted base,
  validated across the complete declared rule registry and selected by root or
  matched expanded pack. Rules cannot supply tools, permissions or delegation.
- Shared files load once; duplicate stable IDs across files, duplicate keys/checks,
  unknown fields, unsupported versions, empty statements and unresolved compilation
  fail closed. Pack/rule reference bytes share the aggregate reference bound;
  the root retains its separate configuration-size bound.
- Selected rules provide authoritative citation sections, stable IDs, provenance
  and `rule:<id>` coverage obligations. Proposed-head corpus entries at declared
  rule paths cannot replace base authority or activate unmatched rules. Preview
  remains observational; missing preview references are visible warnings.
- Behavioral regressions exposed mutable compiled rule slices, sentence-as-rule-ID
  gate mapping and a valid citation hidden after another citation. These are fixed;
  stable mapping revalidates source/key/quotation against selected base evidence.
- A real Git fixture replaces both pack and Markdown rule on head. The runner
  retains base authority and distinguishes incomplete rule coverage, covered-clean
  assessment and a validated blocking violation under the stable rule ID.
- CLI resolves root/pack rules with the same explicit-root confinement and rejects
  missing files without printing a successful validation receipt.
- Verification: full uncached Go suite, targeted race tests and `go vet ./...`.

## Slice 9: Complete Declared Method Registry

- Red: unmatched missing/malformed/traversal/unknown-builtin methods were ignored,
  model calls proceeded and preview showed no warning.
- Green: SCM and frozen CI validate all unique methods declared by defaults and
  the complete inline/imported pack registry before inference. Only effective
  selected methods activate instructions/tools/checks; inactive methods remain
  inspectable provenance, not execution authority.
- Bound admission to 256 unique IDs and 1 MiB aggregate method content; shared
  references read once. Check metadata IDs are valid/unique/bounded. Cancellation,
  inactive-content digest binding and head-corpus exclusion have regressions.
- A real Git fixture with an unmatched missing method fails CI admission before
  a panic provider can run. Preview discloses the same error without enforcement.
- Verification: uncached full Go suite and targeted pipeline/CI/review race tests.

## Slice 10: Confined Repository-Read Transport

- Red: remote readers sent traversal paths to providers and followed redirects
  into another repository/origin while carrying credentials.
- Green: trusted file reads require canonical bounded paths, provider-specific
  repository identities and full immutable commit SHAs. Unsafe/ambiguous origins
  fail before network I/O. Redirects are never followed, even same-origin.
- A private copy of the configured HTTP client preserves caller ownership while
  enforcing response/request/aggregate byte bounds and cancellation. Unrelated
  SCM publishing/intake transport is unchanged.
- Verification: full uncached suite and focused repository-reader race tests.
- Still open at this slice: tree-mode/object binding, symlink/submodule handling
  and frozen Git object confinement. Transport guards alone do not prove these.

## Slice 11: GitHub Commit/Tree/Blob Confinement

- Red: the contents reader accepted disguised symlinks/submodules/directories,
  incomplete/mismatched tree receipts and wrong blob data without proving regular
  file membership in the requested revision.
- Green: resolve the requested immutable commit to its tree, walk only regular
  tree ancestors and read only regular/executable blob leaves. Reject symlinks,
  submodules, truncated trees, duplicate selected paths and identity/size mismatches.
- Fetch blobs by object ID under the same configured API/repository and verify
  raw bytes using native Git blob identity. Provider-returned URLs are ignored.
- Positive nested-path/enterprise-prefix, missing-file, ancestor-symlink,
  tampering and negative object-mode fixtures qualify the adapter without live API calls.
- Verification: full uncached suite and focused tools race tests.
- API contract evidence: [GitHub trees](https://docs.github.com/en/rest/git/trees),
  [blobs](https://docs.github.com/en/rest/git/blobs) and the contents API's
  [symlink behavior](https://docs.github.com/en/rest/repos/contents).

## Slice 12: GitLab Tree/Raw-Blob Confinement

- Red: raw file reads accepted symlinks/submodules/directories and unbound bytes
  without checking their membership/mode in the requested commit.
- Green: walk regular tree ancestors at the same project/full SHA, validate the
  selected regular blob and fetch exact raw bytes by its object ID. Verify the Git
  blob hash rather than using the JSON blob API, which can transcode stored text.
- Pagination stays pinned to reconstructed project/ref/directory requests. Reject
  duplicate selected paths across pages, invalid/conflicting pagination and
  resource-bound exhaustion. Provider-supplied next URLs are never followed.
- An additional failing regression caught a later duplicate hidden behind
  Link-only pagination. Binary-byte, namespace, ancestor-link and headerless
  bounded-pagination fixtures now cover those paths.
- Verification: full uncached suite and focused tools race tests.
- API contract: [GitLab repository trees and raw blobs](https://docs.gitlab.com/api/repositories/).

## Slice 13: Frozen Git Object Confinement

- Red: `git show` exposed symlink bytes as regular policy content, replacement
  refs changed bytes under an immutable SHA, and ambient repository/object paths
  redirected reads away from the configured checkout.
- Green: walk regular trees with literal pathspecs, accept only regular blob
  leaves, read by blob ID and verify native Git object identity. Missing-file
  classification no longer depends on parsing English `git show` errors.
- Frozen commands disable replacement objects and remove ambient repository,
  worktree, object-store and injected-config overrides. This is object confinement,
  not the future general-purpose repository-command sandbox.
- Real Git fixtures cover symlink text shaped like a method, directories,
  submodules, blob/commit replacement, environment overrides and cancellation.
  CI rejects a base symlink method even when head replaces it with a regular file,
  before a panic model provider can run.
- Verification: full uncached suite and targeted tools/CI race tests.

## Slice 14: CLI Registry And Regular-File Qualification

- Red: CLI validation still reported success for missing/unknown unmatched methods
  and symlinked methods, unlike service admission. Explain omitted the registry.
- Green: CLI uses the same bounded declared-method validator, requires an explicit
  root for repository dependencies and rejects symlink leaves/ancestors. Inactive
  methods are validated/provenanced without adding instructions or checks.
- Receipts label the scope `unbound_policy_bundle`; explain labels `unbound_preview`
  and separates registry from selected methods. This does not freeze a mutable
  local workspace or attest a base revision; the private local-review journey
  remains G10, not completed by these validation commands.
- The documented example itself failed the new qualification because its backend
  method was absent. Add the explicit example method root and execute both README
  commands. Validation reports two methods; explanation selects the backend method
  only for the matching path. An automated fixture protects the documented command.
- Verification: full uncached suite, broad tools/pipeline/CI/review/CLI race tests,
  `go vet ./...`, and actual local validate/explain commands. No paid inference or
  live GitHub/GitLab API qualification was run.

## Slice 15: Explicit Provider Commit/Completeness Receipts

- Red: a mismatched GitLab commit receipt did not affect file acceptance because
  the adapter had not checked that receipt; GitHub accepted missing truncation
  metadata as if the tree were known complete.
- Green: GitLab resolves and checks the exact commit ID before tree traversal.
  GitHub requires an explicitly non-truncated tree receipt. Invalid/missing
  identity or completeness cannot fall through to blob reads.
- Verification: full uncached suite, tools race tests and `go vet ./...`.
  Contract source: [GitLab single-commit API](https://docs.gitlab.com/api/commits/#get-a-single-commit).

## Slice 16: Comparable Operator Ceilings

- Red: currency and shared-period mismatches passed numeric ceiling checks;
  negative operator limits acted as unlimited and changed ceilings did not change
  effective identity.
- Green: validate the operator limit structure, require comparable currency and
  fixed periods, reject every requested resource above its ceiling and bind the
  operator ceilings into effective identity. Compiled limits detach period pointers.
- Verification: full uncached suite and policy race tests.
- Boundary: this is compilation authority, not runtime usage enforcement. Runtime
  wiring and dispatch authorization remain open at this slice.

## Slice 17: Trusted Runtime Ceiling Configuration

- Red: enforce-mode configuration accepted absent/malformed operator ceilings.
- Green: closed versioned `POLICY_RUNTIME_CEILINGS_JSON` is host configuration,
  never a repository default. Missing members, duplicate/unknown keys, unsupported
  versions and invalid values fail; enforce-mode service configuration requires it.
- Service and CI admission receive the trusted limits. Real CI fixtures prove
  attempt/change/project increases are rejected before a panic provider can run.
- Verification: full uncached suite and targeted policy/config/CI race tests.
- Boundary: these checks do not reserve spend or police aggregate usage. Dispatch
  must separately refuse missing/unavailable accounting authority.

## Slice 18: Fail-Closed Model Dispatch Budget Boundary

- Red: complete, native streaming and parallel batches reached providers with
  enforced policy but no reservation authority. These are provider-I/O tests,
  not configuration-only assertions.
- Green: every model invocation now reserves through an explicit trusted
  authority before I/O, including formatter/repair/fallback and compatibility
  streaming paths. The receipt binds the serialized request and actual route.
  Missing authority, stale operator binding, zero capacity, excessive output and
  invalid receipts deny dispatch. Budget errors cannot trigger provider fallback.
- The authority contract requires atomic hierarchical reservation, trustworthy
  input/price bounds, valid canonical identity and durable sending markers. No
  production implementation or in-memory substitute is installed. Enforced
  inference is therefore intentionally unavailable until that backend exists.
- Response outcomes are recorded even after cancellation. Current string-only
  providers cannot supply trustworthy usage: unknown usage is never represented
  as settled or released. Active-time bounds cancel provider contexts.
- Trusted ceilings also guard legacy/preview dispatch when configured. With no
  host ceilings those older modes retain their characterized unbudgeted behavior.
- Tests fund unrelated method/rule fixtures with clearly test-only authorities;
  shared parallel admission, separate fallback reservations, record failure,
  cancellation, stream ordering and CI incomplete behavior are covered.
- Verification: full uncached Go suite, targeted race tests and `go vet ./...`.
- Boundary: this does not complete operator ceilings. PostgreSQL coordination,
  crash recovery, canonical attempt wiring, price/token qualification, provider
  usage receipts and non-model effect/tool accounting remain open. Scoped
  conflicts have not been started.

## Slice 19: Terminal Budget Failure And Partial Audit Recovery

- Red: a coverage-repair reservation denial was swallowed and the pipeline
  synthesized coverage then drafted successfully. A JSON-repair denial triggered
  another repair and obscured the budget failure. Initial model denial also lost
  admitted policy/ceiling provenance in the failed service run.
- Green: accounting failures and cancellation are terminal across both repair
  callers. No synthesis, additional repair or draft follows denial. Partial model
  audit and policy limits are saved using bounded cancellation-independent
  persistence; persistence errors remain visible to the caller. CI recovers that
  partial source on failure and keeps the gate incomplete.
- An additional red fixture caught unvalidated model candidates escaping through
  that partial snapshot. Failed pre-validation runs now retain raw audit only,
  not accepted finding projections.
- Verification: focused denial/repair/cancellation tests include a context-aware
  failing store and a real frozen-Git CI run that retains its first model response
  after repair-budget exhaustion. Full uncached suite, orchestrator/CI/pipeline
  race tests, formatting and `go vet ./...` passed.
- Boundary: this is conservative failure handling, not a durable ledger or a
  claim that resource accounting is complete.

## Slice 20: Qualified Model Cost Upper Bounds

- Red: cost qualification had no implementation; valid hard-limit pricing failed
  and malformed/duplicate authority records were accepted by the initial stub.
- Green: an immutable exact-provider/model catalog rejects absent/expired
  qualification, mismatched request bytes/model, unqualified native tools,
  excessive output and integer overflow. Input reservation uses a documented
  provider-enforced billable hard limit, not prompt character heuristics or an
  estimated token-count endpoint. All billable output must be bounded by the
  request cap; fixed charges are included. Integer micro-prices round upward.
- Operator qualification records require version, evidence reference, currency
  and half-open validity. No built-in production models or current prices are
  asserted; fixture routes are test-only. A hard-limit quote can be deliberately
  expensive and deny requests that a future exact tokenizer could safely admit.
- Boundary: this is the bound calculator, not durable admission or live route
  qualification. No default service route becomes spend-authorized.
- Verification: accounting unit and race tests pass. Full-suite verification is
  temporarily blocked by an unattributed edit removing committed CI ceiling wiring;
  that unrelated removal is not included in this slice.

## Slice 21: Real PostgreSQL Shared Model Admission

- Red: real database fixtures failed against the empty authority: no three-scope
  admission, durable sending marker, identity qualification or rollback contract.
- Green: trusted attempt registration precedes worker dispatch. Ordered scope
  locks atomically commit request, quote, attempt/change/project holds and sending.
  Unknown outcomes retain holds; closed attempts cannot spend. Replay receipts
  prohibit resend. Canonical tagged digests, unique action IDs and reserved
  active-time bounds now cross the orchestrator boundary.
- Additional red fixtures caught project scope resetting across repositories,
  mutable price-version labels and a live authority spending against an
  incompatible schema. These now fail closed using trusted project identity,
  immutable qualification records and per-transaction schema checks.
- Real-DB tests cover last-unit races, rollback of every hold, independent
  connections, worker process death after commit, stale authority, unknown holds
  carried from prior periods and provider calls preceded by durable admission.
- PostgreSQL migrations and pgx are explicit. Go remains 1.24; no default service
  route or legacy attempt is silently promoted into coordinated accounting.
- Verification: full uncached suite with real PostgreSQL enabled, isolated
  PostgreSQL race script, formatting and `go vet ./...` pass. The unattributed CI
  removal noted in slice 20 was restored with explicit user authorization.
- Boundary: synchronous model admission only, not the complete action journal.
  Final reconciliation, over-bound incidents, prospective reconfiguration and
  end-to-end intake/controller integration remain open. See `docs/accounting.md`.

## Slice 22: Final Settlement, Integrity And Non-Destructive Migration

- Red: real PostgreSQL final settlement was absent; receipt replay, origin-period
  accounting, quarantine and over-bound usage had no implementation. A known
  active-time overrun was ignored when token usage remained unknown.
- Green: explicit trusted verification precedes final receipt settlement. One
  atomic transaction replaces the three holds with actual usage in their origin
  periods. Replay cannot double-charge or double-release. Partial/unverified
  usage cannot settle; authenticated mismatches are quarantined. Closed attempts
  remain closed after late settlement.
- Over-bound actuals are recorded, not truncated, and an integrity incident
  blocks new spend in every affected scope even after renewal. Known active-time
  overruns block immediately without inventing token receipts.
- Additional red: migration recreated a missing ledger table as empty accounting.
  Migrations now distinguish initialization from repair, refuse missing/partial
  stores and preserve existing sending actions/holds through the v1-to-v2 upgrade.
- Tests include concurrent receipt replay, settlement rollback, late receipt
  fencing, past-period actuals versus lifetime attempts and real-DB migration.
- Verification: full uncached suite with PostgreSQL enabled, isolated real-DB
  race script, formatting and `go vet ./...` passed.
- Boundary: production verifier adapters, authenticated operator corrections,
  end-to-end intake/currentness and non-model accounting remain open. Test
  verifiers are not live provider-usage qualification.

## Slice 23: Expiring Authority And Cross-Project Receipt Races

- Red: an expired reservation permit still reached the model provider. A real
  PostgreSQL lock wait could outlive a price contract and commit a stale quote.
  Concurrent conflicting receipt IDs in unrelated projects settled only once
  but lost the losing receipt's quarantine record.
- Green: permits carry the earliest execution/qualification deadline and
  dispatch applies it before provider I/O. Admission rechecks the database clock
  after qualification locking, then binds the current period and price. A
  transaction-scoped receipt identity lock serializes receipt conflicts before
  scope locks, preserving quarantine without double settlement.
- Test evidence includes an actual blocked PostgreSQL session held through price
  expiry and a delayed-insert trigger reproducing the cross-project receipt race.
- Verification: full uncached suite with PostgreSQL enabled, isolated real-DB
  race suite, formatting and `go vet ./...` passed.
- Workspace note: earlier working-file drift was restored to verified commits
  with user authorization. Its writer/root cause is not established; opening the
  changes view was reported as correlated timing, not proven cause.

## Slice 24: Canonical Intake Identity Checks

- Red: verified constructors accepted mismatched request provider, change and
  base/head identities and discarded the supplied attestation. SCM attestation
  could also rebind the request to a different provider, repository or change.
- Green: canonical constructors validate supplied identity fields against the
  accepted attempt and retain an owned attestation. SCM response identity cannot
  silently replace authenticated request identity. Display repository aliases
  remain separate when a canonical project ID is supplied.
- Verification: full uncached suite and review race tests. Database admission
  and service/CI wiring are separate following slices, not implied by constructors.

## Method File Example

Policy selects `repo/backend`; commit this file at
`.7review/methods/backend/SKILL.md` in the trusted base:

```markdown
---
name: backend
description: Verify backend authorization and state transitions.
allowed-tools: scm-api diff-analyzer corpus-selector
required-checks: backend-contract
---
Check authorization before privileged state mutation. Identify the concrete
contract or invariant and evidence for each finding; preserve unknown coverage.
```

Only `builtin/correctness` is registered initially. Unknown builtins fail closed.
Method tool metadata cannot widen compiled runtime capabilities.

## Remaining

Hosted GitHub/GitLab permission/API-version qualification and the separate mutable
local snapshot journey; remaining effect capabilities;
operator ceilings; scoped conflicts/prerequisites; independent investigation;
publication restrictions and all remaining G04 acceptance variants remain open.
Required-check activation alone is not proof of check satisfaction.
