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

CLI method-registry qualification and provider file-reader/confinement qualification;
remaining effect capabilities;
operator ceilings; scoped conflicts/prerequisites; independent investigation;
publication restrictions and all remaining G04 acceptance variants remain open.
Required-check activation alone is not proof of check satisfaction.
