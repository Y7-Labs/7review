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

## Slice 25: Durable Canonical Intake And Generation Fencing

- Red: no canonical admission operation existed. Real PostgreSQL cases required
  delivery replay, fresh linked reruns, stale-acquisition rejection and isolated
  ephemeral identity. Additional red cases caught timestamp round-trip mismatch
  and automatic same-snapshot generation churn.
- Green: schema v3 commits immutable intake receipts and registered attempts with
  a per-change tracking pointer. Intake compares the generation observed before
  acquisition; a changed pointer rejects late work rather than rolling it back.
  New service generations fence old dispatch without releasing shared holds.
  Delivery conflicts are rejected and audited; replay retains original deadlines.
- Ephemeral/historical intake does not advance the service tracking pointer.
  Automatic events for the same snapshot/policy reuse the existing attempt;
  explicit manual reruns create fresh linked identities. UTC-normalized receipts
  survive serialization without changing authority binding.
- Verification: full uncached suite with PostgreSQL and accounting race tests.
  This adds the durable operation; pipeline/app wiring follows separately. It is
  not durable HTTP inbox acknowledgement or complete action recovery.

## Slice 26: Canonical Pipeline And Frozen CI Checkpointing

- Red: model calls preceded canonical identity/checkpointing; mutable change-key
  runs overwrote history; intake failures, duplicate receipts and failed saves
  did not stop inference. CI only attached execution identity after model return.
- Green: opt-in canonical pipeline execution checkpoints verified identity before
  model dispatch, uses non-overwriting run IDs and rejects missing intake/storage
  authority. Trusted endpoint hosts cannot be replaced by report URLs. Receipt
  checks reject host/boundary/producer changes and deadline extensions.
- Frozen CI supplies provider host, pipeline/job identity, deadline and per-job
  persistence before inference. Standalone CI has only a local ephemeral receipt;
  an explicit shared intake can checkpoint accounting without taking ownership of
  the service tracking pointer. Replayed CI does not infer again or claim a final
  pass without retrieving the original outcome.
- Canonical publication remains blocked pending governed outbox/currentness;
  these executions are artifact-only. Legacy mode is not silently promoted.
- Verification: full uncached suite with PostgreSQL; pipeline/CI/accounting/
  orchestrator race tests, including real frozen-Git + PostgreSQL CI dispatch,
  duplicate refusal and a new job unable to reset the shared hold.
- Boundary: application construction and operational runtime configuration follow
  separately. This is not durable HTTP inbox acknowledgement or native delivery.

## Slice 27: Opt-In Runtime Construction And Operational Wiring

- Red: partial runtime settings silently disabled accounting and configured
  service construction never installed canonical intake. Integration tests require
  durable attempts with zero HTTP model calls when the route is unqualified.
- Green: complete trusted runtime settings connect migrated PostgreSQL to service
  and trusted CI construction. Missing/unavailable/incompatible storage cannot
  reset allowance or initialize replacement tables. Explicit `accounting migrate`
  is available without normal service credentials. Error paths redact the DSN.
- The service uses opaque queued run IDs matching actual records, trusted endpoint
  hosts and artifact-only canonical execution. Raw authenticated webhook payload
  digests survive normalization; the canonical queue bypasses process-local dedup
  so a changed payload under the same delivery ID reaches durable conflict audit.
- Runtime construction deliberately authorizes no model routes or final receipt
  verifier. Optional ungoverned memory/context effects and legacy canonical
  publication are disabled. Untrusted fork CI does not open shared accounting.
- Compose now forwards the ceiling document and the three runtime settings; it
  previously did not forward the ceiling document at all. Lifecycle goldens were
  reviewed and changed only to add the raw payload digest.
- Additional red hosted-CI cases caught an unbound GitHub target, missing PR/fork
  identity treated as trusted, and GitLab path-based scope differing from the
  service's numeric project ID. Hosted intake now rejects mismatched targets and
  unknown PR identity, treats missing fork provenance as untrusted, and uses
  `CI_PROJECT_ID`. Historical alias/rename migration remains a separate open gate.
- Boundary: this is worker/application intake wiring, not a durable HTTP inbox,
  full action recovery, live route qualification, native outbox or completed
  ceilings. Scoped conflicts remain untouched.
- Verification: full uncached suite with PostgreSQL enabled; real-DB race suite
  across app, CI, pipeline, review, CLI, accounting and orchestrator; formatting,
  `go vet ./...` and Compose configuration validation passed.

### Slice 28: Durable Inbox Storage and Conservative Recovery

- Red: no durable HTTP acceptance contract existed. Added PostgreSQL cases for
  lost-response replay, reopen recovery, concurrent claims, payload conflicts,
  unowned completion, expired pending work and crash-after-claim uncertainty.
- Schema v4 stores normalized requests (including authenticated raw-body digest),
  stable run IDs and original deadlines before acknowledgement. Replay cannot
  extend deadlines or hide changed normalized fields behind an unchanged digest.
- Pending work is claimable across processes using row locks. Claimed work is
  never automatically reclaimed: expiry records `uncertain`, retaining all
  accounting holds. This is not action-level resume or exactly-once effects.
- A deferred SQL trigger tests commit failure after receipt selection: no receipt
  is returned, and the provider can retry the rolled-back delivery safely.
- Explicit migration extends v3; missing current ledger relations remain errors,
  not an excuse to initialize empty tables. Production routes remain unqualified.

### Slice 29: HTTP Acceptance and Restart Workers

- Red: zero-capacity queues could not durably accept requests; webhook responses
  discarded run IDs, conflicts waited for worker execution, and canonical intake
  could fall back to the volatile queue without a runtime. Regression tests cover
  each boundary and actual signed HTTP delivery.
- Canonical enqueue now commits to the inbox first; no volatile fallback exists.
  GitHub/GitLab durable responses carry JSON receipts. Pending recovery uses
  bounded workers and the original database deadline; committed claims never
  grant a second execution on restart. Shutdown cancels and joins inbox workers
  before closing storage. Panic/shutdown does not fabricate safe completion.
- `/inbox?run_id=...` exposes minimal status through existing API-token middleware,
  even before local artifacts exist. Payloads and claim tokens are not exposed.
- Real-DB tests verify replay, payload conflicts before dispatch, inbox outage,
  pending restart recovery, already-claimed crash refusal, shutdown and deadlines.
- Boundary: no exactly-once external effects, action-level resume, durable result
  store or authenticated operator recovery. Model routes remain unqualified;
  full ceilings and scoped conflicts are not declared complete.
- Verification: full uncached `go test ./...` with PostgreSQL enabled, real-DB
  app/accounting/orchestrator race suites, `go vet ./...` and diff checks pass.

### Slice 30: Ownership Commit Faults and Abrupt Process Death

- Deferred PostgreSQL update-trigger failures exercise claim and completion
  commit windows. Failed claim commit returns no executable item and preserves
  pending work. Failed completion commit preserves the exclusive running claim;
  another worker receives no item, even when it cannot know whether effects ran.
- Subprocesses exit immediately after acceptance or claim commit, skipping all
  deferred cleanup. A new authority recovers the pending receipt but cannot
  redispatch claimed work. These supplement in-process restart simulations.
- Namespace isolation prevents another host-owned project from inspecting or
  claiming the row. Migration refuses to recreate a missing v4 inbox ledger and
  leaves existing receipts intact. These are fault qualifications, not full
  action recovery, operator intervention or production failover qualification.
- Verification: PostgreSQL-enabled app/CLI/accounting/orchestrator race suite.

### Slice 31: Durable Inbox Readiness

- Red: readiness only inspected the legacy volatile queue and could conceal a
  durable inbox outage. The canonical path now checks inbox storage/schema and
  reports separate pending/running/uncertain backlog counts.
- Missing storage or runtime marks readiness down; a healthy in-memory queue is
  not a substitute. Storage readiness explicitly does not qualify model routes
  or turn uncertain executions into successful reviews.
- Verification: real-DB outage/backlog regression and app/accounting/orchestrator
  race suites, followed by the full PostgreSQL-enabled suite and vet.

### Slice 32: Atomic Acceptance Jobs and Admission Linkage

- Red: inbox acceptance had no durable job, and attempt registration could commit
  independently of its execution owner. Schema v5 creates an acceptance job in
  the inbox transaction, then supports atomically linking the attested admission
  receipt and attempt under the current job claim.
- Deferred commit failures prove job creation rolls back acceptance and failed
  linkage rolls back attempt/head changes. Stale owners and request rebinding
  are rejected before admission replay. Manual job retries use stable job identity.
- Automatic duplicate jobs retain a receipt link but cannot gain fresh dispatch
  authority. Existing claimed v4 rows migrate as legacy-unknown, not retryable
  work. This storage contract does not yet assert live worker claim propagation.
- Verification: PostgreSQL-enabled accounting, orchestrator, app and CLI suites.

### Slice 33: Live Job Ownership at the Sending Boundary

- Worker context now carries the host-issued job claim through canonical
  admission and every model dispatch. Run identity must match the claim. Tokens
  remain outside model input and Source artifacts.
- Linked attempts cannot spend without their live job owner. Finished, expired,
  stale-token and duplicate-only jobs are denied. Sending, job effect-started
  state and all three holds commit together using job/head/attempt/scope ordering.
- Deferred sending-commit failure tests prove no orphan action or permit escapes.
  A real service-constructor test verifies the run-to-attempt FK, and orchestrator
  tests verify claim propagation without leaking it to the provider payload.
- Receipt recording remains permitted for late attributable responses: it is
  observation, not authority to send again. Production route catalog stays empty.

### Slice 34: Recover Committed Action Responses Without Resending

- Red: recorded outcomes had no durable local reduction record. Schema v6 adds
  unique action-result projection with request/response provenance and explicit
  `recorded_unvalidated` status. A unique partial index enforces one spending
  job per attempt, while duplicate receipt aliases remain non-spending.
- Recovery locks one job and indexes bounded committed responses in one SQL
  transaction. Failed commits replay only this local operation. Sending/unknown
  outcomes retain their holds, corrupt hashes fail closed, and no result is
  silently approved as evidence, settled or promoted to completed job status.
- Subprocess exits after outcome and reduction commits verify crash recovery.
  Concurrent reducers index once logically. Live worker adapter tests verify
  local recovery without rerunning the pipeline, and refuse new inbox claims
  when the recovery journal is unavailable.
- `/inbox` exposes attempt/phase and unvalidated/unresolved counts without private
  output bodies. This is local result-index recovery, not prepared-action reclaim
  or validated continuation of the full review state machine.

### Slice 35: Close Alternate Service Entry-Point Bypasses

- Red: the legacy rerun/tool path could call the canonical pipeline without an
  inbox claim; the low-level authority API could also register/spend an unlinked
  service attempt. Production runtime now requires owned service jobs at both
  admission and dispatch, independently of route qualification.
- Service pipeline wiring rejects unowned execution before creating a run.
  Legacy rerun remains visibly denied until its command returns a fresh durable
  intake receipt. Explicit job-local CI is not mislabeled as service ownership.
- Tests exercise pre-existing unowned attempts with a qualified test catalog,
  direct admission/registration, rerun entry, production constructor wiring and
  successful ephemeral CI. This is execution enforcement, not config validation.
- Full ceilings remain open: prepared reclaim, validated continuation, full
  before-202 aggregate/state/result transactions, provider currentness, non-model
  accounting, operator recovery and production provider qualification are not
  replaced by these guarded slices. Scoped conflicts remain untouched.
- Verification: full uncached suite with PostgreSQL enabled; real-DB race tests
  across app, pipeline, CLI, accounting and orchestrator; vet, formatting, diff
  and Compose configuration checks pass.

### Slice 36: Caller-Owned Acceptance Transactions

- Red: admission and inbox helpers committed independently of their caller.
  Added real-DB rollback cases for inbox/job creation and attempt/intake/head
  identity. Both now have transaction-scoped implementations; public operations
  still commit explicitly and retain conflict audits.
- This enables, but does not itself claim, the complete before-202 aggregate.
  Existing replay, commit-fault and accounting behavior remains covered.
- The disposable PostgreSQL harness now waits for TCP readiness, not the
  bootstrap-only Unix socket that could yield transient connection resets.

### Slice 37: Atomic Accepted Aggregate Storage

- R14/S15 red cases now cover failure after the final aggregate INSERT: inbox,
  job, attested attempt, intake receipt, generation pointer, prepared Source and
  initial acceptance event all roll back together. Acceptance returns only after
  the single transaction commits.
- Schema v7 stores a bounded, checksummed, versioned prepared checkpoint. Replays
  preserve original identity/deadline and verify state/receipt/event consistency;
  conflicting payloads are durably audited and corrupt checkpoints fail closed.
- Internal admission ownership is revoked before commit. The job becomes pending
  with an admitted attempt, and a worker must obtain a separate execution claim.
  Pre-v7 nonterminal jobs migrate as legacy-unknown rather than reconstructing
  their missing state from newer SCM data. Holds are not deleted or reset.
- This slice establishes storage atomicity; HTTP preparation and worker reuse of
  the persisted Source are separate wiring steps, not yet implied by this API.

### Slice 38: Side-Effect-Free Preparation and Frozen Worker Continuation

- R14/S15 preparation now reads trusted SCM/base policy, resolves skills/rules,
  observes generation before acquisition and returns a serializable checkpoint
  without creating runs, attempts, model calls or publications.
- Accepted execution consumes that checkpoint without new SCM reads or
  readmission. Required skills/rules use the same selection helper as existing
  pipeline execution. Trigger rejection is ignored, never promoted to authority;
  preview/unverified policy cannot become an accepted service aggregate.
- A red continuation test caught a publication-mode bypass in the new branch;
  accepted execution now requires artifact-only delivery before any model call.
- Verification: focused red/green tests and the full pipeline package pass.
  HTTP acceptance has not yet been switched to the new preparation path.

### Slice 39: Complete Before-202 Service Aggregate

- R14/S15 signed HTTP red cases exposed acknowledgement before attempt/state
  commit, failure to reject aggregate commit errors, and policy selection hiding
  a prior accepted receipt. Canonical HTTP intake now prepares read-only evidence,
  commits the full initial aggregate and only then returns its stable receipt.
- Workers claim only complete production aggregates and load frozen state; the
  old incomplete acceptance API is denied in production. Invalid preparation,
  missing/corrupt state and commit failure cannot produce a durable 202 receipt.
- PostgreSQL precision tests caught nanosecond/microsecond deadline mismatch;
  deadlines normalize downward. An expired retry still resolves an existing
  receipt. Concurrent identical deliveries share one aggregate; failed new-head
  commits preserve old currentness, while successful supersession fences it and
  retains unresolved holds.
- Subprocess exit immediately before writing 202 proves the committed aggregate
  survives a lost response. Restart performs no new SCM/policy reads. The default
  unqualified catalog makes zero calls; an explicitly test-qualified in-memory
  provider executes once after sending/three holds commit. Replay makes no new
  attempt, job, model call, usage settlement, HIL approval or publication.
- v6 upgrade keeps old jobs legacy-unknown without inventing checkpoints or
  resetting holds. Original ownership remains for late attributable response
  recovery through the immutable sending intent, not a mutable dispatch grant;
  this also works when v7 has already disabled the old grant. It does not restore
  dispatch authority or rewrite an already-applied migration.
- This closes initial before-202 aggregate atomicity under the controlled tests,
  not prepared reclaim, validated continuation, final-artifact persistence,
  operator recovery, provider currentness or the complete ceilings/release gates.
- A final corruption case caught a cross-run acceptance event passing replay
  checks. Event type/run/attempt/generation are now all checked against the
  committed aggregate, independently of checkpoint-body integrity.
- Verification (2026-11-02): full PostgreSQL-enabled suite, broad real-DB race tests, focused
  before-202 crash/replay/corruption races, vet, gofmt, diff and Compose checks.

### Slice 40: Durable Non-Sending Preparation

- REC-01 red tests establish `prepared` as a distinct execution state. Immutable
  request/quote and three holds commit once with an initially unowned lease row.
  Preparation replay preserves identity and never authorizes provider I/O.
- The immediate-send compatibility API cannot promote an existing prepared action.
  REC-02 negative tests reject outcome and final-usage receipts before sending;
  impossible final receipts are quarantined without releasing the reservation.
- Schema v8 adds lease/fence storage without inventing leases for historical
  sending actions. Claims, sending transitions and runtime wiring follow as
  separate verified steps. Production route qualification remains unchanged.
- Verification: PostgreSQL accounting/orchestrator suite, including migration and
  existing receipt/hold behavior, passed.

### Slice 41: Fenced Prepared Claims and Confined Reclaim

- REC-01 claims use database time, bounded leases, expected fencing epochs and
  monotonically increasing fences. Live owners cannot be stolen; expired owners
  cannot begin. Sending/currentness, immutable qualification, scope authority,
  reservation integrity and budget incidents are rechecked under ordered locks.
- Reclaim rotates the durable service job token and confines it to the selected
  action. Neither the stale owner nor the recovery owner may schedule a new ID
  as a substitute for validated continuation. Jobs with prior external progress
  are not taken over. Ephemeral runtime recovery requires service handoff.
- Claim/reclaim/sending commit faults return no authority and roll back token,
  fence and marker changes. A durable sending fence overrides contradictory
  `prepared` state; sending/uncertain/result actions are never reclaimable.
- Schema v9 records fenced provenance so loss of a lease row cannot degrade
  result recording to legacy authorization. Late attributable responses retain
  their sending fence even after lease expiry. No unused/unknown hold is released.
- Bounded host-only discovery finds lost preparation receipts; descriptors do
  not grant dispatch. Recovered immutable input bytes remain available on claims.
- Verification: real PostgreSQL race cases for stale owners, concurrent claims,
  commit faults, authority revocation, missing records and outcome fencing.

### Slice 42: Live Fenced Dispatch Wiring

- Every configured PostgreSQL model invocation now prepares, claims and begins
  before provider I/O. Failure or malformed receipt at any stage is terminal;
  an authority exposing the staged protocol never downgrades to immediate send.
- The orchestrator validates immutable input, worker/fence provenance and the
  sending deadline. RecordOutcome carries the issued sending fence. The shared
  boundary still covers parallel, fallback, formatter, streaming and native paths.
- The real service test asserts a committed sending fence and three holds before
  its controlled provider call. It caught monotonic/local-zone time metadata in
  quote comparison; canonical persisted JSON now compares the full semantic quote
  without weakening price/version/validity checks.
- `/inbox` reports prepared holds separately from unresolved sends. Readiness
  checks lease storage too. Production catalog remains empty; no provider or
  autonomous recovery adapter is implicitly qualified by enabling leases.

### Slice 43: Process Death and Expired-Owner Race Qualification

- REC-01/REC-02/S15 subprocesses exit after preparation, lease claim and sending
  commit, skipping all callbacks and deferred cleanup. Lost prepared receipts are
  discoverable, and only their original immutable bytes are recovered.
- Expiry waits on the authoritative database lease. A stale original owner races
  a newly fenced owner; exactly one controlled provider effect is permitted.
  Sending commit followed by process death grants no new owner or resend even
  when no provider callback ran. All three holds survive; no usage is invented.
- Boundaries remain explicit: no automatic whole-job replay, no unqualified
  native/stream recovery, no operator recovery command, no prepared cancellation
  refund, no validated continuation or completed ceilings. Scoped conflicts are
  untouched and production route/receipt qualification remains fail-closed.
- A red deadline test caught the ownership lease accidentally shortening the
  existing inference contract. Lease validity gates Begin; committed sends keep
  their qualified active-time/execution/price bounds and cannot be reclaimed.
- Verification: `scripts/test-accounting-postgres.sh ./...` and the real-PostgreSQL
  race suite for accounting, orchestrator, app, CI, pipeline and CLI passed.
  `go vet ./...`, formatting, diff checks and Compose configuration also passed.

### Slice 44: Automatic Confined Prepared-Action Recovery

- A red invocation test exposed that stored requests could not distinguish
  Complete from Stream. New intents bind the actual API mode; historical missing
  modes fail closed rather than guessing. Stream compatibility uses Complete.
- A red fresh-service test established the missing worker wiring. Existing bounded
  inbox workers now discover/reclaim and dispatch only immutable prepared actions.
  The original provider/model, schemas and bytes are used without role rerouting,
  fallback, new action IDs or whole-pipeline execution.
- Recovered calls share the live deadline/active-time/outcome recording boundary.
  Complete/native responses and locally captured Stream output remain unvalidated;
  no draft, job completion, publication, hold release or new spending grant follows.
- Real subprocess loss after prepare/claim/sending, duplicate recovery workers,
  stale originals and faults before sending/after provider I/O exercise the
  automatic adapter. Only pre-sending failure permits eventual reclaim; uncertain
  sending remains non-retryable even when recording the response fails.
- Full PostgreSQL suite, accounting/orchestrator/app race suites and vet passed.
  Production routes remain unqualified;
  validated continuation, operator controls, safe cancellation/refund and upgrade
  rehearsal remain separate gates, not completed ceilings.

### Slice 45: Explicit V8-To-V9 Upgrade Rehearsal

- A populated v8-shaped schema retains prepared leases, fenced sending actions,
  historical unfenced sending actions and all three-scope holds through upgrade.
  Worker runtime refuses v8; only explicit migration advances the version.
- A deferred commit failure rolls back both new columns and provenance backfill.
  Retry is idempotent and preserves immutable requests/quotes, amounts, original
  deadlines, lease epochs and job tokens. Existing sending receipts remain usable
  only under their original fencing contract; no sending action becomes prepared.
- Full PostgreSQL suite and focused upgrade/crash/recovery race tests passed.
  No deployed database was migrated, and backup/failover qualification is not implied.
- Next continuation prerequisite: persist the exact model-loop/evidence checkpoint
  and a versioned deterministic reducer, not just a raw response or initial Source.
  The current unvalidated journal cannot authorize a draft or unrestricted job
  continuation. Operator controls and cancellation/refund also remain open.

### Slice 46: Serial Batch Coverage Gap

- A red test showed that disabling parallelism silently discarded all but the
  first diff batch. Serial review now dispatches every batch with a stable slot
  name and retains partial audit evidence on failure. Empty work fails explicitly
  rather than panicking or implying completed coverage. Orchestrator race tests pass.

### Slice 47: Deterministic Parallel Reduction Order

- A red reversed-completion test exposed arrival-order-dependent raw findings.
  Workers now fill distinct batch slots and merge successful responses by index
  after joining, including partial responses when a sibling fails. The batch plan
  does not change with scheduling. Orchestrator race tests pass.

### Slice 48: Immutable Pre-Action Model Checkpoints

- A real-service red test required the continuation input to exist before provider
  I/O. Initial batches, tool rounds and both formatter repairs now freeze the exact
  Source, prior raw responses, typed cursor and retained coverage into the action.
- The configured default-validator confidence/version is frozen too; custom
  validators deliberately lack a deterministic replay qualification. Batch slots
  share one pre-batch snapshot even when preceding serial batches finish first.
- Reservation rejects mismatched identity, execution, policy, SCM or cursor without
  leaking holds. Reclaim returns identical checkpoint bytes. A valid but different
  prior-response history cannot be rebound to an existing action ID. Checkpoint
  and full-action size bounds fail closed; absent legacy context is not reconstructed.
- Full PostgreSQL suite, review/orchestrator/accounting/app/pipeline race suites and
  vet passed. No schema change is needed for the existing immutable request body.
- Still required: versioned deterministic local reductions, durable reducer output
  and atomic fenced next-action scheduling, including batch joins and tool/repair
  cursors. Responses remain unvalidated; no recovery fence is cleared yet. Operator
  controls, cancellation/refund and full ceiling/release qualification remain open.

### Slice 49: Every Batch Must Be Parsed

- A red live-pipeline test proved that concatenating responses selected only the
  first JSON candidate, silently losing later findings. Every batch now parses
  separately before findings, coverage and bounded tool requests are combined.
- Malformed/empty slots cannot be hidden by valid peers. Missing or duplicate
  per-batch coverage cannot become meaningful aggregate coverage. The recovery
  reducer and live path share this parser. Pipeline and focused race tests pass.

### Slice 50: Versioned Pure Local Model Reducer

- Red tests require exact request/response/checkpoint binding, all batch slots,
  deterministic permutation-independent results and pinned confidence settings.
- `ModelReducerV1` uses immutable inputs only. It proposes tool/findings-repair/
  coverage-repair cursors, blocks exhausted tools, and otherwise applies the
  existing deterministic coverage, location and finding validators before rendering
  a local draft. Rejected findings and raw input references remain auditable.
- It never invokes a model, tool, store or publisher. Missing qualification,
  changed identity, unknown versions and absent changed-file evidence fail closed.
  Pipeline/review/orchestrator tests and focused race cases pass.

### Slice 51: Formatter Tool Requests Are Not Repaired Findings

- A red regression showed a formatter tool request being interpreted as successful
  empty findings repair. The live parser now rejects that shape, matching the pure
  reducer; it cannot bypass the malformed-output gate. Focused race cases pass.

### Slice 52: Exhausted Tools Cannot Complete A Review

- An end-to-end red test found four tool-request responses producing `drafted`
  despite `tool_loop_incomplete`. This is now a terminal parse failure before
  further coverage-repair calls, retaining raw audit but never successful status.
  Live pipeline tests and focused race cases pass.

### Slice 53: Atomic Durable Local Reduction And Schema V10

- Schema v10 stores reduction artifacts and immutable action-input links. Existing
  inbox workers index committed responses then reduce locally, with one job lock,
  complete group checks and unique links. `/inbox` reports artifact/continuation/
  blocked counts without response bodies. Raw journal rows remain separate.
- Real PostgreSQL tests cover deferred commit failure, concurrent reducers, panic,
  exact namespace/source/receipt checks, faulty adapters, missing then reverse-order
  batch arrivals and OS process death before/after reduction commit. Retry repeats
  local computation only, never a model/tool effect or a hold reservation.
- The v9-to-v10 populated upgrade rehearsal proves DDL rollback, idempotent retry
  and reduction of a prior v9 result without resetting request/quote/outcome/holds.
  Older v1/v6/v8 rehearsals still run through the current explicit migration.
- Full PostgreSQL suite, broad accounting/app/pipeline/orchestrator/review race
  suites, focused latest regressions and vet passed. Runtime now requires schema
  v10; no operator/deployed database was migrated by these tests.
- Still open: atomic parent-to-child cursor transitions, durable terminal job
  projection, authenticated operator controls and safe cancellation/refund. No
  reduction clears `recovery_action_id`, grants current use or approves/publishes
  a report. Live qualification and complete ceilings/release gates remain open.

### Slice 54: Frozen Role Routing For Continuation

- Red tests require reasoner/formatter route chains and output limits to survive
  later configuration changes. Pipeline checkpoints now freeze this host contract;
  dispatch must match its role. Reducer artifacts preserve it without reinterpretation.
- Optional fields keep legacy checkpoint/group digests stable; absent old routing
  is never filled from current settings. Configuration detachment, role-bound
  substitution and real-service pre-I/O checkpoint tests pass with race coverage.

### Slice 55: Caller-Owned Reservation Transaction

- A red caller-rollback test required action/lease/all three holds to participate
  in the same transaction as a confined job pointer. The private reservation helper
  now composes without committing; public wrappers still return only after commit.
- Rollback leaves no action, holds, lease or grant, and does not consume capacity.
  Existing reservation/concurrency/commit-fault race tests remain green.

### Slice 56: Pure Formatter Child Construction

- A red parent-context test requires the exact original formatter, output bound,
  prompt template, prior raw responses and validation settings. The versioned
  builder consumes the stored artifact only, not current role/profile configuration.
- It proposes no action identity/token/permission. Missing legacy routing and
  terminal/tool states cannot guess a billable child or fabricate observations.
  Pure builder/reducer tests pass under the race detector.

### Slice 57: Atomic Confined Formatter Children

- Schema v11 links a parent reduction to one deterministic child. Current job,
  parent input/response proof and immutable source checks precede rotation of the
  job token and restriction to that exact child. Reservation and link commit
  together; deferred FK enforcement forbids a dangling committed child grant.
- The inbox loop advances eligible formatter plans, then the existing fenced
  recovery path handles the linked child. Parent proof is rechecked at claim/Begin;
  unrelated/unresolved work remains non-reclaimable. No whole pipeline is replayed.
- PostgreSQL tests cover duplicate advancement, deferred commit failure, bad
  builders, changed evidence/deadlines/catalog/attempt/job epoch, parent revocation after
  claim and OS process death after child prepare/claim/sending. Sending children
  are not resent, holds are retained, and parent replay keeps the same child ID.
- The fresh-service test now follows a malformed recovered parent through exactly
  one formatter child into a validated local artifact without memory-store replay.
  Consumed plans no longer appear as pending continuations.
- Explicit v10-to-v11 migration rehearses DDL/FK rollback, idempotent retry and
  rejection of dangling grants; older rehearsals remain green through v11.
  Full PostgreSQL, broad race, latest focused race, vet and format checks pass.
- Still required: direct live-pipeline/canonical child unification, prepared batch
  backlog manifests/ownership, committed tool
  observations and tool continuation, durable terminal projection, authenticated
  operator controls and safe cancellation/refund. No job completion, approval,
  publication or hold refund is claimed; ceilings and alpha remain incomplete.

### Slice 58: Committed Live Result References

- A red test requires a live action/request/response reference only after durable
  outcome recording succeeds. Failed outcome commits cannot leave a reference
  that might authorize continuation. Parallel callers use the context mutex.
- References are detached across context clones and are transient, not serialized
  into Source, prompts or authority. Orchestrator/review race tests pass.

### Slice 59: Live Formatter Handoff Uses The Recovery Cursor

- A red service test requires normal canonical execution to hand off at formatter
  repair, not issue a new random legacy call. Findings/coverage repair now use the
  committed parent reference, shared local reducer and guarded advancement API.
- Parent digests, phase, run, attempt and execution deadline must match. Missing
  authority or bad receipts are terminal budget errors, never fallback authority.
  Handoff is a distinct result: the live frame is not falsely failed/completed,
  and the inbox worker does not call FinishInbox with a revoked parent token.
- The fresh-service test completes the stored child without SCM/pipeline replay.
  A barrier-controlled overlap pauses live advancement while recovery creates and
  executes the child, then releases the old frame. Two total calls, one child,
  exact holds and refusal of old-token completion are verified under the race detector.
- Full PostgreSQL, broad app/pipeline/orchestrator/review/accounting race suites,
  vet and formatting pass. No schema change; current explicit version remains 11.
- Still open: durable terminal projection, batch manifests/backlog ownership,
  committed tool observations/continuation, authenticated operator controls and
  safe cancellation/refund. Handed-off frames must be replaced by authoritative
  stored results, never replayed. Ceilings and alpha remain incomplete.

### 60. Durable Terminal Model Projection

- Schema v12 adds a terminal reduction reference. Completion atomically finishes
  the owned inbox/job and disables dispatch, only for validated or blocked results
  with current attempt, deadline, job epoch and sending fences. Repeated finishers
  converge on one result; deferred commit failure exposes no projection.
- Red regressions caught two real omissions: changed formatter-parent evidence
  after dispatch, and an unrelated already-reduced sibling. Completion and reads
  now recheck bounded ancestry, exact frozen child plans, original outcomes and
  coverage of every action. Historical reads do not reopen dispatch authority.
- Worker recovery performs local finalization; HTTP run/list views override stale
  memory with checked durable results. Restarted successful/formatter jobs become
  visible without pipeline replay. Corrupted storage returns 503 rather than a
  stale live frame. A separate red test caught missing ancestry rows returning
  SQL not-found and accidentally selecting stale memory; only an absent terminal
  reference now permits fallback. Blocked results become failed, never approved
  or published.
- Real PostgreSQL tests interrupt a process at a deferred COMMIT barrier and
  terminate its waiting backend to establish rollback; another process exits
  immediately after successful commit. Local retry preserves actions and holds.
  Client death alone is not proof of rollback. The populated v11-to-v12 rehearsal
  verifies DDL/version rollback, explicit retry and prior evidence preservation.
- Verification: full PostgreSQL-backed suite, broad accounting/app/pipeline/
  orchestrator/review race suite, latest focused race regressions, vet, formatting,
  bridge tests and Compose configuration validation pass.
- Unknown usage remains held, not refunded or settled. Batch manifests/backlog
  ownership, durable tool observations/continuation, authenticated operator
  controls and safe cancellation/refund remain open. Ceilings and alpha are not
  complete. No deployed database has been migrated.

### 61. Atomic Initial Batch Preparation

- Added the accounting `PrepareModelBatch` operation: one bounded, ordered set of
  frozen initial batch slots reserves all actions and all three scope holds in
  one transaction, without acquiring leases or authorizing provider I/O.
- Every slot must share the same job token, group digest, source and qualified
  validator. Missing/duplicate/reordered slots and mixed checkpoints fail before
  preparation. Existing partial/foreign legacy work cannot be adopted as a new
  atomic set. Retrying an already-sending group never resets its effects.
- PostgreSQL tests exercise two competing preparers, a reopened authority retry,
  later-slot route/capacity denial, and deferred commit failure. Failed operations
  expose no receipts and leave no partial actions/holds; successful retries do not
  reserve twice.
- Verification: full PostgreSQL-backed suite, focused batch race tests, vet and
  formatting pass.
- This is the storage foundation, not complete batch recovery. `CompleteParallel`
  still uses individual dispatch preparation; integration, sealed membership,
  group-confined recovery ownership and live/restart overlap are the next work.
  Existing action-only reclaim restrictions remain intact. No schema change;
  explicit runtime version remains 12. Ceilings and alpha remain incomplete.

### 62. Sealed Batch Membership At Execution Boundaries

- A red PostgreSQL regression showed that atomic preparation still allowed an
  extra action to reserve money with the same job token. Schema v13 now commits
  an ordered, digest-bound membership seal and job reference with the complete
  action/hold set. Exact retries verify it; legacy unsealed sets cannot be adopted.
- Reservation, claim and Begin verify every original slot and group input. Extra
  actions, changed seal digests, missing/duplicated slots and detached job bindings
  fail closed. A replacement execution token does not rewrite the original intent.
  Terminal projection also verifies sealed membership.
- The only supported nonmember is the exact formatter child derived from a
  checked reduction and confined by the current action grant. A full two-batch
  response -> formatter -> terminal test verifies that valid continuation still
  works and that later seal corruption makes the result unavailable.
- Deferred seal commit failure rolls back all actions, holds and membership.
  The populated v12-to-v13 rehearsal verifies DDL/version rollback, idempotent
  retry, unchanged sending requests/quotes/holds and fenced outcome attribution.
  Migration never manufactures legacy batch recovery authority.
- Verification: full PostgreSQL-backed tests, broad accounting/app/pipeline/
  orchestrator/review race tests, vet, formatting, bridge tests and Compose
  configuration validation pass.
- Still open: group-confined batch reclaim, live `CompleteParallel` integration,
  interrupted/overlapping batch recovery, durable tool continuation, authenticated
  operator controls and safe cancellation/refund. Schema v13 requires explicit
  migration; no deployed store was changed. Ceilings and alpha remain incomplete.

### 63. Action-Confined Recovery Across A Sealed Batch

- Red PostgreSQL tests reproduced a stranded second slot and epoch rotation on
  expired-lease reclaim. First takeover now revokes the live owner once; later
  slots retain that epoch and use increasing per-action fences. The cursor moves
  only to the first unfinished sealed slot, never to arbitrary job work.
- Every slot is checked before takeover: active prepared owners, unsealed sibling
  actions, sending/uncertain progress, integrity incidents, changed proof or
  foreign result epochs block dispatch. Existing attempt/deadline/route/capacity
  checks still apply transactionally. Failed claim commit preserves original
  token/cursor/fences; stale owners cannot Begin or finish the job.
- The automatic scanner now selects a single eligible slot per sealed group.
  Competing recovery workers and reopened authorities converge without duplicate
  calls. A separate red test caught the scanner excluding groups with a known
  live result; mixed groups now progress and finalize with checked original-token
  provenance, not arbitrary stale-token acceptance.
- Additional red proof tests found that matching zero fences could masquerade as
  sending provenance. Recovery and terminal ancestry now require positive fences
  plus recorded worker/lease metadata; equality alone grants no authority.
- A separate boundary regression caught `FinishInbox` accepting action-only
  recovery tokens. Verified commit `9d7ab73` denies both success/failure completion
  by confined owners and disables dispatch on ordinary inbox completion.
- Subprocess exits after claim, sending and recorded response verify that restart
  recovers prepared work, never retries sending, and skips recorded effects. Holds
  remain intact and unknown usage is not settled/refunded. Automatic local
  indexing/reduction/finalization also completes mixed live/recovery groups.
- Verification: full PostgreSQL-backed suite, broad accounting/app/pipeline/
  orchestrator/review race suite, latest focused race regressions, vet, formatting,
  bridge tests and Compose configuration validation pass.
- Still open: canonical live batch planning/handoff and live/recovery overlap,
  durable tool continuation, authenticated operator controls and safe
  cancellation/refund. `CompleteParallel` still uses individual preparation.
  No schema change; explicit version remains 13. Ceilings and alpha are incomplete.

### 64. Canonical Live Batch Planning And Durable Handoff

- Red service tests caught multi-batch execution issuing individual live calls
  without a seal. Canonical durable-service multi-batch calls now freeze the full
  ordered partition and request set, reserve/seal atomically, and return the
  existing durable-handoff result before provider I/O.
- The planner binds the checkpoint's complete diff, source, qualified validation,
  routing and output bounds. Shared intent preflight preserves host/policy ceiling
  checks. Missing authority, stale partition/source, builder mutation, cancellation,
  failed commit or invalid receipt cannot fall back to individual dispatch.
  The prompt builder cannot mutate either source or partition after validation.
  A red exact-data test also caught large tool-schema integers being rounded by
  cloning; structured decoding now preserves JSON numbers.
- PostgreSQL service tests cover fresh-service restart after batch commit, a
  post-commit panic, recovery finishing while the live frame is paused, formatter
  repair and normal HTTP acceptance -> inbox workers -> terminal draft. Later
  role/model/output configuration changes cannot reselect stored requests.
  Every provider call observes the complete seal/holds and its sending fence.
- Red completion tests caught the old live token still able to fail a committed
  seal before takeover. Verified fix `9daaeb6` removes whole-job completion rights
  at seal commit. Invalid receipts and a lost prepare acknowledgement after a real
  PostgreSQL commit now leave the intact stored plan recoverable; no live resend
  or whole-pipeline replay is granted.
- The abandoned frame cannot finish or publish. Durable run views report drafted
  results without approval; the restarted in-memory store remains empty. Tool
  responses persist as pending `needs_tools` plans with no invented observations,
  extra model effects or terminal success.
- Canonical multi-batch I/O now uses the sequential action-confined recovery
  cursor. Single-batch and ephemeral/legacy dispatch keep their existing paths.
  Full PostgreSQL-backed tests, broad accounting/app/pipeline/orchestrator/review
  race tests, vet, formatting, bridge tests and Compose configuration validation
  pass on the integrated tree.
  Still open: durable tool observation/continuation, authenticated operator
  controls and safe cancellation/refund, plus remaining operational/provider
  qualification. No schema change; current explicit version is 13. Ceilings and
  alpha remain incomplete.

### 65. Snapshot Tool Input Scope And Honest Truncation

- Red tests showed that model-loop tools ignored arguments, including a foreign
  `run`, and labelled incomplete, invalid-UTF-8/JSON result prefixes as `ok`.
  Verified commit `777b4b3` validates the run-only input contract, preserves round
  identity and returns a bounded valid JSON preview with status `truncated`.
- Denied inputs expose no result. Truncated observations are not successful tool
  evidence. This changes the actual existing read executor, not just a future
  configuration validator. Full pipeline tests, focused race tests and vet passed.

### 66. Qualified Pure Snapshot Observation Sets

- Red parser tests caught silently dropped blank tool names and omitted requests
  beyond the first five. Malformed/over-bound plans now remain explicitly
  incomplete and cannot become successful empty evidence. Batch aggregation
  preserves that fatal status rather than erasing it.
- `ModelToolObserverV1` consumes a verified pending parent and produces a detached
  bounded set, with exact run/attempt, parent/input and ordered-request digests.
  It reads only captured Source through the shared governed snapshot executor.
  No profile, SCM, publisher, store or model provider is consulted.
- The adapter validates parent identity/cursor/input references and rechecks the
  original response digests and full normalized proposals. Altered declarations
  and legacy partial plans are rejected. Repeated rendering is deterministic;
  cancellation between observations exposes no partial set or success authority.
- Missing snapshot data now yields errors rather than fabricated empty results;
  explicitly captured empty diffs remain valid. Permission tests now provide real
  captured data, preserving independent policy/skill intersection checks. Denials,
  unsupported tools/fields, foreign run inputs and truncation remain explicit.
- Verification: full PostgreSQL-backed suite, corrected broad accounting/app/
  pipeline/orchestrator/review race suite, latest focused observer/round tests,
  vet, formatting, bridge tests and Compose validation pass. The permission
  fixture failures were resolved by supplying captured evidence, not by relaxing
  the missing-data rule. PostgreSQL durability settings remained enabled.
- Still open: durable observation commit and tool resource accounting, guarded
  reasoner-child creation/recovery, authenticated operator controls and safe
  cancellation/refund. The canonical worker still leaves `needs_tools` pending;
  the pure adapter grants no execution or financial rights. Schema remains 13;
  no deployed database was changed. Ceilings and alpha remain incomplete.

### 67. Bounded Snapshot Observation Usage Contract

- Red tests required a shared usage contract before accepting tool observations
  into durable accounting. `SnapshotUsage` now checks ordered request digests,
  parent/run/attempt attribution, bounded round transitions, exact tool/surface
  identity, policy/skill/input confinement and bounded status/result semantics.
- Every evaluated proposal counts, including denials, errors and duplicates.
  Observation bytes use the actual encoded record, including metadata and JSON
  escaping. A later invalid member exposes no partial charge. No provider bill,
  token usage, active time or settlement is inferred from a local rendering.
- Scope-bypass tests preserve internally consistent request hashes so rejection
  exercises enforcement, not just stale digests. Canonical truncation rejects
  extra/duplicate fields and cannot be relabelled successful evidence. The pure
  renderer now checks this contract before returning an artifact.
- Focused review/pipeline tests, their complete race suites, the full
  PostgreSQL-backed suite, vet, bridge tests and Compose validation pass.
  This contract is not a durable receipt,
  capacity admission or independent provenance proof. Durable observation commit,
  shared-scope charging and fenced reasoner-child continuation remain the next
  implementation step. No automatic tool continuation or new schema is enabled.
  Schema remains explicitly v13; controls, cancellation/refund and operational
  qualification remain open. Ceilings and alpha are not complete.

### 68. Durable Snapshot Charges And Confined Observation Epoch

- Snapshot acceptance now commits one immutable private receipt, its three scope
  links and old-owner revocation together. Parent lineage, current job/head/
  attempt, sending fences and the original model holds are verified before a
  trusted pure renderer runs. No model child or provider usage receipt is created.
- A red missing-hold case caught incomplete scope locking; acceptance now rejects
  missing/foreign parent holds before rendering. Further red claim/Begin cases
  caught empty shared-period IDs that hid newly committed charges. Those checks
  now use fresh database time after route locking and include snapshot usage.
  A final red sealed-batch case rejected reconstruction of a missing recovery
  cursor from recorded results; original, recovered and mixed ownership remain
  separately covered.
- Denials/errors count as accepted logical evaluations. Exact rendered bytes are
  checked on all three scopes before acceptance. Pure render rollback may repeat
  computation; local CPU/external tool active-time qualification remains open.
  Known local charges use their recorded periods; unknown model holds stay intact.
- Duplicate committers converge once, and historical replay returns only checked
  data, never new ownership. Deferred commit failure, renderer error/panic,
  cancellation, stale epochs/heads/attempts/operator authority, unrelated siblings,
  process death during rendering and immediately after commit have regression
  coverage. An original unconfined live frame loses completion/failure and
  unlinked-dispatch rights at observation commit. Tampered or missing charge
  links cannot silently erase capacity. Rehashed origin-period edits are checked
  against accounting time, not accepted as a free renewal. Shared charges also
  constrain a later attempt's model admission, claim and Begin. Competing PRs
  cannot each consume the project's last local tool-call allowance.
- Explicit schema v14 migration has populated rollback/retry coverage; it
  preserves existing requests/outcomes/reductions/holds/tokens and invents no
  legacy observations or grants. Earlier migration fixtures retain their rollback
  coverage through the new schema. No deployed database was changed.
- Verification: the full PostgreSQL-backed suite and full race suite pass,
  followed by a full-suite rerun and focused race checks for the final sealed
  cursor and historical-read cases. Vet, formatting, bridge tests and Compose
  validation pass. PostgreSQL durability settings remained enabled.
- Guarded reasoner-child authorization, full round lineage and automatic worker
  use remain next. The canonical worker still leaves tool plans pending rather
  than sending an unlinked child. Authenticated controls, safe cancellation/refund
  and remaining provider/operational qualification remain open. Ceilings and alpha
  are unfinished.

### 69. Guarded Reasoner Children And Cross-Role Lineage

- A pure versioned builder consumes the exact parent/observation set, appends
  current tool history once and freezes the reasoner route, output bound,
  validation, cursor and raw-response history. Pipeline and authority now share
  the compiled native snapshot definitions, intersected with policy and skills.
  A builder proposal has no action identity, job token or sending authority.
- `AdvanceModelToolContinuation` checks the committed observation epoch and
  complete parent proof before atomically preparing one child, three model holds,
  its typed parent/observation link and a new child-only job epoch. Denials and
  deferred commit failure preserve accepted observation charges and ownership.
  Competing creators and real post-commit process exits cannot reserve twice.
- Red integration tests caught the old formatter-only reclaim gate. Prepared
  tool children now enter the existing fenced scanner/claim/Begin protocol.
  Crash tests at prepared, claimed and sending states preserve exact stored
  requests; sending children never resend. Post-claim receipt/cursor/model-proof
  changes block sending. No whole-job replay or current-profile route fallback.
- All three rounds retain ordered requests, observations and raw responses and
  reach checked local terminal completion without settling unknown model holds.
  Sealed initial groups admit explicit checked nonmember children, not extra
  batch slots. Mixed tool-to-formatter repair retains its full ancestry.
- Further red tests caught formatter preparation/Begin trusting only the
  immediate model parent after a tool round. Complete bounded ancestry and exact
  membership are now required there too; a corrupted older model outcome cannot
  authorize the later repair. Checking only receipt digests was insufficient.
- Explicit schema v15 migration has populated deferred-commit rollback and
  idempotent retry coverage. It preserves accepted observations/charges/holds/
  ownership and invents no child. Earlier upgrade fixtures include the new table.
  No deployed database was changed.
- Verification: full PostgreSQL-backed tests, cross-role/sealed focused tests,
  the complete PostgreSQL-backed race suite, vet, formatting, bridge tests and
  Compose validation pass. Final builder-panic and unchanged-owner failure checks
  also pass under the race detector. PostgreSQL durability settings remained enabled.
- Automatic worker creation and canonical live tool handoff remain next. No
  end-to-end continuation, authenticated operator control, safe cancellation/
  refund, provider/CPU accounting or release qualification completion is claimed.
  Ceilings and alpha remain unfinished.

### 70. Automatic Tool Workers, Live Handoff And Budget-Wait Isolation

- Inbox workers now advance current qualified tool parents through observation
  acceptance and exact child preparation, then use existing fenced recovery.
  Canonical live tool proposals hand off before any legacy observation mutation
  or follow-up model call. Both paths use frozen evidence/routing and retain the
  original deadline. Ephemeral/noncanonical behavior is unchanged.
- Restart and duplicate-worker tests cover all three rounds, formatter repair,
  crashes before/after observation acceptance, paused live/recovery overlap,
  deferred observation/child commit failure, lost child acknowledgements, missing
  handoff capability and an unavailable result journal. Recovery reaches local
  checked drafts without SCM reads, whole-job replay, approval or publication.
- Red tests caught ordinary live failure/completion rights after sending and
  after an unsent committed preparation. `FinishInbox` now rejects any reserved
  model work. Recorded-result handoff errors abandon the frame without falsely
  completing/failing durable work. No-reservation failures remain finishable;
  legacy terminal rows still block outstanding claims.
- Further red tests caught exhausted attempts and prepared actions blocking
  healthy recovery/inbox progress. Typed known pre-send budget waits now yield
  bounded scans using scheduling hints only. They change no deadline, grant,
  lease, state or hold. Post-send provider quota-shaped errors and failed/missing
  scheduling stores remain failures, not inferred non-execution or free retries.
  Malformed negative ledger amounts remain integrity errors, not budget waits.
- Completed tool parents no longer appear as pending continuations. Original
  reduction artifacts remain immutable; explicit typed links prove progression.
- Verification: full PostgreSQL-backed tests and the complete PostgreSQL-backed
  race suite pass, along with vet, formatting, bridge tests and Compose checks.
  Durability settings remained enabled throughout; controlled providers are not
  live-provider qualification.
- No schema change; explicit schema v15 remains required. No deployed store was
  changed. Authenticated controls, safe cancellation/refund, full tool/CPU and
  live provider/operational qualification remain open. Ceilings and alpha remain
  unfinished.

### 71. Explicit Operator Principals And Scoped Durable Reads

- Red: tests first required a host-mapped principal rather than the legacy
  optional/shared-token guard, exact boundary/project/provider/host/repository
  roles, and an actor that could not be forged by supplying an ID or roles.
  HTTP/accounting tests initially failed on the absent contracts and route.
- Green: a bounded closed V1 host document resolves environment-secret
  references into an immutable directory, rejecting ambiguous credentials,
  duplicate/unknown fields and grants, missing secrets and wildcard scopes.
  Tokens are not serialized or persisted. Authentication issues an opaque actor
  pinned to that directory; accounting independently checks its provenance and
  repository role. No identity is inferred from `REVIEW_API_TOKEN`.
- Green: `GET /operator/attempt` requires an exact attempt ID and a reviewer or
  operator grant. It rejects repeated/conflicting credential headers, body
  claims, extra/duplicate query fields and oversized queries. Repository scope
  comes from the checksummed durable binding, never the client. Durable
  aggregate integrity is checked before returning the small status projection;
  expired/inactive records remain inspectable without granting dispatch.
- Verification: the complete PostgreSQL-backed suite, focused PostgreSQL-backed
  operator/runtime race tests, vet, formatting and diff checks pass. Tests cover
  missing/unmapped authentication, foreign-directory actors, wrong repository,
  namespace and role, run aliases, historical status, corrupt checkpoints and
  concurrent authentication. PostgreSQL was disposable; no deployed store changed.
- No schema change; explicit schema v15 remains required. This is read-only
  operator access, not command execution or a mutable command version.
  Authenticated mutation receipts, cancellation/refund, broader tool/CPU and live
  operational/provider qualification remain open. Ceilings remain unfinished.

### 72. Durable Cancellation Commands And Fenced Dispositions

- Red: closed versioned envelope tests required exact command/attempt/revision,
  nonzero bounded expected version and a reason, rejecting actor claims, unknown
  fields, ambiguous JSON and oversized input. The contract is committed as
  `cd36ea3`. Backend tests then failed on the absent cancellation operation;
  PostgreSQL HTTP tests failed on the missing command route.
- Green: host-mapped reviewer/operator credentials now authorize
  `POST /operator/commands` cancellation. HTTP and accounting independently
  enforce actor provenance and repository scope. A transaction commits the
  command/receipt, advances a separate control version, closes the attempt,
  revokes its job claim and disables dispatch. Stale currentness/revision/version
  returns a scoped conflict, never silently cancelling a newer attempt.
- Green: namespace-wide command identity binds actor and exact payload. Retries
  return the original checksummed receipt after authorization, including after
  restart/lost acknowledgment; conflicting actors/payloads cannot rebind it.
  Duplicate delivery jobs cannot replace the original attempt's control target.
  A further red test caught aliases still appearing pending after cancellation;
  effective inbox views, queue counts, claiming and stale-owner completion now
  respect the original attempt's cancellation without granting an alias control.
  Terminal model/ordinary completion advances the same control version and cannot
  overwrite a cancellation that won the transaction race.
- Behavior coverage includes duplicate workers, sending and terminal-completion
  races, interrupted reclaim, expired work, corrupt aggregate/audit evidence,
  revoked credentials, wrong roles, real process/backend interruption before
  commit, death after commit, and an unknown commit acknowledgment. Client death
  alone is not treated as proof of rollback; exact-command replay reconciles it.
- Verification: the final complete PostgreSQL-backed suite and focused
  PostgreSQL-backed cancellation/operator/alias/migration race tests pass, along
  with vet, formatting and diff checks. A complete race run also passed before
  the final alias-view/queue correction; that correction was then covered by the
  final full suite and focused race tests. No live provider was qualified.
- Schema v16 is explicit. Its populated v15 upgrade rehearsal injects deferred
  commit failure, verifies complete DDL/version rollback, retries idempotently and
  preserves original attempts, requests, outcomes, leases, tokens and holds.
  Historical rehearsals, including v8-to-v9, construct the actual older schema
  without leaving newer command objects behind. Migration invents no commands,
  refunds, sending rights or human consent. No deployed store was changed.
- Cancellation receipts explicitly retain all reservations. Positive sending
  evidence and attributable late results survive without reviving dispatch or
  terminal model completion. Verified-unused release/refund, CLI/TUI mutations,
  other human commands, broader tool/CPU and live operational/provider
  qualification remain open. Ceilings and alpha remain unfinished.

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
