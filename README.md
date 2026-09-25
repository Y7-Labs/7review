<div align="center">
  <img width="320" height="320" alt="7Review Mascot" src="https://github.com/user-attachments/assets/f2cafd71-b49b-4faf-98d5-798a492dc26f" />
</div>

# 7review

7review is being redesigned as a team-configurable review engine for GitHub/GitLab,
local changes and CI. It investigates consequences under repository-owned methods,
reports evidence and unknowns, and leaves merge authority with humans. Trusted
team policy may enforce a blocking quality gate; the model does not approve code.

## Product Direction

Target behavior, not a list of already delivered capabilities:

- Installed PR/MR review: selected repositories, automatic triggers, incremental
  follow-up, native summary/inline feedback and authorized conversation commands.
- Shared adaptive engine: cheap triage, scoped investigation, evidence validation,
  bounded cost, clarification and revision-aware continuation.
- Team-owned methods by project, domain, module or feature; design contracts govern
  intended behavior without being mandatory for ordinary correctness findings.
- CI execution: non-interactive jobs, provenance-bound artifacts and explicit
  advisory/blocking gate outcomes, distinct from model confidence.
- Governed memory and attempt-scoped evidence graph; semantic indexing, code
  intelligence and Headroom remain optional in the target.

Figure R1. Target product flow, not the current runtime pipeline. Arrows show
normalized inputs, artifact flow and explicit human interaction.

```mermaid
flowchart TB
  scm["Installed GitHub / GitLab integration"] -->|"Authenticated change"| engine["Shared adaptive review engine"]
  local["Local or trusted CI input"] -->|"Frozen comparison"| engine
  method["Trusted team methods"] -->|"Rules and obligations"| engine
  engine -->|"Evidence, findings and coverage"| assessment["Versioned assessment"]
  assessment -->|"Team policy evaluation"| gate["Deterministic quality gate"]
  assessment -->|"Authorized feedback"| human["Human reviewer"]
  human -->|"Scoped answer or dispute"| engine
  gate -->|"Native status or job result"| output["SCM / CI"]
```

## Project State And Documentation

The redesigned system remains a **design candidate**, not a released integration
equivalent to Greptile or CodeRabbit. Thirteen engineering directions are approved
(2026-09-25): adaptive progress, scoped stopping, priority coverage, revision reuse,
latest-head scheduling, hierarchical budgets, coordinated PostgreSQL accounting,
conservative recovery, semantic guards, fixed periods, credential isolation,
automated CI and delegated methods. DOC-01 through DOC-06 are closed as design
contracts, the final whole-system review found no blocking design contradiction,
and the complete design was accepted on 2026-09-25. Phase 1 canonical-domain work
is complete. Phase 2 is in progress: immutable intake attestations, typed readiness,
strict policy V2 validation/compilation and offline policy preview exist, but are
not yet connected to review intake or SCM/CI delivery. None of the later target
runtime capabilities is implied complete.

The target has autonomous local accounting and optional coordinated team/CI mode.
Only the coordinated authority can enforce ceilings shared across machines.
PostgreSQL is selected for that budget/action ledger, not installed by this design;
SQLite remains a local candidate. Optional isolated verification is in product
scope, with sandbox qualification still required. No autonomous merge is permitted.

| Document | Purpose |
| --- | --- |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Product, arc42 architecture views, decisions and remaining runtime risks |
| [SPEC.md](SPEC.md) | Behavioral contracts, schemas, acceptance cases and precision closure |
| [ROADMAP.md](ROADMAP.md) | Immediate design work and conditional implementation |
| [STATUS.md](STATUS.md) | Recorded baseline, validation evidence and remaining gates |

The remainder of this README documents the existing runtime. Those commands are
not new-engine instructions, and historical smoke results are not current CI
evidence. In particular, existing draft/HIL/final memory flow and packaged sidecar
requirements differ from the target's independent effects and optional enrichment.

## Existing Runtime

7review is usable as a local-first draft review agent for GitHub pull requests
and GitLab merge requests. Operators can manually request a review for a
specific PR/MR, while SCM webhooks are policy-gated before they enqueue work.
The agent enriches changes, selects repository knowledge, runs model review
with governed read-only tools, validates findings, publishes draft comments,
and keeps final publication behind human approval.

Core capabilities:

- GitHub pull request and GitLab merge request webhooks
- bounded webhook worker queue
- GitHub/GitLab enrichment, draft publishing, and final publishing
- multi-provider model routing with role fallbacks
- OpenAI, Anthropic, OpenRouter, DeepSeek, Mistral, Gemini, Ollama, and
  OpenAI-compatible providers
- provider-native tool calling for OpenAI-compatible/OpenRouter, Anthropic,
  Gemini, Mistral, and Ollama responses
- portable `SKILL.md` review procedures with required core/provider coverage
- generic document graph retrieval for repository knowledge selection
- deterministic finding validation and inline draft comment publishing
- Docker Compose runtime with agent, Headroom bridge, and MemPalace bridge
- operator CLI/TUI/chat for setup, status, run inspection, approval, reruns,
  final publishing, and memory review
- authenticated manual review trigger through CLI or `/tools/execute`

Current operating recommendation: use 7review as an automated draft reviewer
with human-in-the-loop approval. It is not yet intended to auto-publish final
approval comments without engineer review.

## Existing Runtime Architecture

7review is split into two planes:

- review plane: webhook intake, SCM enrichment, context selection, model review,
  finding validation, draft publishing, HIL, final publishing, and memory write
- operator plane: authenticated tools, run inspection, context audit, chat, CLI,
  and TUI

System overview:

```mermaid
flowchart TB
    GitHub[GitHub PRs] --> Webhooks[agent/app webhooks]
    GitLab[GitLab MRs] --> Webhooks

    Webhooks --> Queue[bounded worker queue]
    Queue --> Pipeline[agent/pipeline]

    Pipeline --> SCM[GitHub/GitLab enrichment and publishing]
    Pipeline --> Corpus[repository knowledge graph from CORPUS_ROOT]
    Pipeline --> Skills[SKILL.md review procedures]
    Pipeline <--> MemPalace[MemPalace memory sidecar]
    Pipeline --> Headroom[Headroom context reducer]
    Pipeline --> Orchestrator[role-based model orchestrator]
    Orchestrator --> Providers[OpenAI/Anthropic/OpenRouter/DeepSeek/Mistral/Gemini/Ollama]
    Pipeline <--> Runs[MEMORY_DIR/runs JSON store]

    Operator[CLI/TUI/chat] --> Tools[authenticated operator tool API]
    Tools --> Runs
    Tools --> Pipeline
```

Existing review lifecycle, distinct from the target adaptive engine:

```mermaid
flowchart TB
    Request[normalized request] --> Enrich[SCM enrichment]
    Enrich --> Diff[structured diff]
    Diff --> Context[skills + graph corpus + memory]
    Context --> Reduce[Headroom reduction]
    Reduce --> Review[model reasoner]
    Review --> ToolsLoop[governed read-only tool loop]
    ToolsLoop --> Review
    Review --> Validate[finding validation]
    Validate --> Inline[inline draft comments]
    Inline --> Draft[draft publish]
    Draft --> HIL[human approval]
    HIL --> Final[final publish]
    Final --> Memory[approved memory write]
```

Repository knowledge is selected by an in-process document graph:

```mermaid
flowchart LR
    Docs[repository docs] --> Split[sections]
    Split --> Index[ID / route / schema / entity / component indexes]
    Index --> Graph[typed document graph]
    Signals[review signals] --> Seeds[exact seeds]
    Seeds --> Graph
    Graph --> Selected[selected evidence]
    Selected --> Manifest[evidence_manifest]
    Selected --> Prompt[review prompt]
```

The graph connects requirements, contracts, APIs, data models, design docs,
ownership docs, and rules through typed trace edges. Retrieval expands only from
exact review signals and records why each section was selected in the
`evidence_manifest`.

During model review, the reasoner may request governed read-only tools. The
pipeline executes only the allowlisted tools, records `tool_call_started` and
`tool_call_completed` events, appends tool observations to review context, and
then asks the reasoner for final JSON findings. Write actions remain outside the
reasoner loop and stay behind deterministic validation and HIL gates.

Package map:

- `cmd/7review`: server and operator CLI entrypoint
- `agent/app`: HTTP routes, webhooks, run endpoints, chat streaming, tool
  execution
- `agent/pipeline`: review lifecycle, run store, deterministic gates, report
  rendering
- `agent/review`: normalized request, source, diff, SCM, finding, report, and
  run state
- `agent/tools`: GitHub/GitLab, Headroom, MemPalace, tool catalog, executor
- `agent/llm/providers`: concrete model provider clients
- `agent/orchestrator`: model role routing, fallback chains, streaming
- `agent/skills`: portable `skill-name/SKILL.md` review procedures
- `agent/ui`: Lip Gloss based setup, status, and chat rendering

For the detailed component model, lifecycle boundaries, state model, evidence
graph retrieval, operator surface, and verification commands, see
[`docs/architecture.md`](docs/architecture.md).

For current verification state, live smoke coverage, and known review-quality
limits, see [`STATUS.md`](STATUS.md).

The web documentation site lives in [`site/`](site). It is built with
Docusaurus, includes English and French operator docs, and is configured for
GitHub Pages at `/7review/`. Before the first Pages deployment, configure the
repository once in GitHub: **Settings → Pages → Build and deployment → Source:
GitHub Actions**. The workflow can deploy with `GITHUB_TOKEN`, but GitHub may
block automatic Pages site creation with `Resource not accessible by
integration`.

## Quick Start

Generate a local environment file:

```sh
go run ./cmd/7review setup
```

Run the test suite:

```sh
go test ./...
```

Start the agent locally after configuring `.env`:

```sh
set -a
. ./.env
set +a
go run ./cmd/7review
```

Check readiness:

```sh
go run ./cmd/7review status --server http://localhost:8080
```

Validate or preview a target V2 repository policy without activating it:

```sh
go run ./cmd/7review policy validate --file profiles/review.v2.example.yaml
go run ./cmd/7review policy explain --file profiles/review.v2.example.yaml \
  --project owner/repository --path backend/auth/service.go \
  --capability repo.read --capability model.review
```

`policy explain` is an unbound preview. Runtime activation must additionally bind
the policy digest and revision to a verified trusted-base snapshot.

Start the Docker runtime:

```sh
make docker-up
```

Check the running Docker agent:

```sh
make docker-status
```

Run the documentation site locally:

```sh
make site-install
make site-dev
```

Manually enqueue one review:

```sh
go run ./cmd/7review review gitlab --project 25 --mr 19 --server http://localhost:8080
go run ./cmd/7review review github --repo owner/repo --pr 7 --server http://localhost:8080
```

## Required Configuration

7review requires:

- one SCM target: GitHub or GitLab webhook/API credentials
- one model provider credential or endpoint
- `HEADROOM_URL`
- `MEMPALACE_URL`
- `REVIEW_API_TOKEN`

Common variables:

```sh
LISTEN_ADDR=:8080
REVIEW_API_TOKEN=change-me
ORCHESTRATOR_CONFIG=./orchestrator.yaml
HEADROOM_URL=http://headroom:8787
MEMPALACE_URL=http://mempalace:8788
MEMORY_DIR=./.7review
CORPUS_ROOT=.
WEBHOOK_WORKERS=4
WEBHOOK_QUEUE_SIZE=128
WEBHOOK_REVIEW_MODE=manual_first
REVIEW_LABEL_INCLUDE=7review,ready-for-review
REVIEW_LABEL_EXCLUDE=no-review,wip,draft
POLICY_V2_MODE=legacy
POLICY_V2_PATH=.7review/review.yaml
POLICY_RUNTIME_CAPABILITIES=repo.read,model.review
```

Webhook review modes:

- `manual_first`: default; webhook events enqueue review only when include
  policy matches.
- `auto`: webhook events enqueue review unless explicit excludes or allowlists
  reject them.
- `off`: valid webhook events are accepted but ignored by review policy.

Repository policy V2 activation:

- `legacy`: default; preserves the characterized input-profile behavior.
- `preview`: reads and compiles policy from the PR/MR base revision, records its
  projection and warnings, but does not change admission.
- `enforce`: fails closed when trusted policy is missing/invalid and applies its
  automatic triggers before skills, corpus, memory or model calls.

In `preview` and `enforce`, `POLICY_V2_PATH` is fetched through the GitHub/GitLab
API at the verified base SHA. Operator project/repository, label-exclusion and
branch-exclusion guards remain absolute. Use `policy validate` and `policy
explain` before enabling enforcement.

GitHub:

```sh
GITHUB_API_URL=https://api.github.com
GITHUB_TOKEN=...
GITHUB_WEBHOOK_SECRET=...
```

GitLab:

```sh
GITLAB_URL=https://gitlab.com
GITLAB_TOKEN=...
GITLAB_WEBHOOK_SECRET=...
```

Model providers:

```sh
ANTHROPIC_API_KEY=...
OPENAI_API_KEY=...
OPENROUTER_API_KEY=...
DEEPSEEK_API_KEY=...
MISTRAL_API_KEY=...
GEMINI_API_KEY=...
OLLAMA_BASE_URL=http://localhost:11434
```

## Webhooks

Routes:

- `POST /webhook/github`
- `POST /webhook/gitlab`
- `POST /webhook`

Webhook handlers verify the configured provider secret and enqueue bounded
background work. Request handlers do not run review work inline.

## Operator Commands

```sh
7review setup
7review status --server http://localhost:8080
7review tui --server http://localhost:8080
7review tui --watch --refresh 5s --server http://localhost:8080
7review runs --server http://localhost:8080
7review run <run-id> --server http://localhost:8080
7review history <run-id> --server http://localhost:8080
7review history <run-id> --type chat_message --limit 20 --server http://localhost:8080
7review chat
7review chat <run-id> --server http://localhost:8080
7review chat --run <run-id> --server http://localhost:8080
# inside run chat: /status, /tools, /providers, /skills, /run, /draft final.md, /approve --report-file final.md
7review approve --run <run-id> --report-file final.md --server http://localhost:8080
7review publish-final --run <run-id> --report-file final.md --server http://localhost:8080
```

`REVIEW_API_TOKEN` is sent as both `Authorization: Bearer ...` and
`X-7review-Token` by the CLI.

## HTTP API

Operator endpoints:

- `GET /health`
- `GET /ready`
- `GET /tools`
- `POST /tools/execute`
- `GET /runs`
- `GET /run?id=<run-id>`
- `POST /chat/stream?run=<run-id>`
- `POST /approve?run=<run-id>`
- `POST /publish/final?run=<run-id>`

Tool executor example:

```sh
curl -H "Authorization: Bearer $REVIEW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"list_skills"}' \
  http://localhost:8080/tools/execute
```

## Skills

Skills live under `agent/skills/<skill-name>/SKILL.md`.

Each skill uses YAML frontmatter plus Markdown instructions. The loader validates
that the frontmatter `name` matches the directory name, that `name` and
`description` exist, and that the Markdown body is not empty.

Core always-on review skills:

- `methodology-review`
- `project-knowledge`
- `framework-rules-review`
- `traceability-review`

Provider skills activate by SCM:

- `github-merge-api`
- `gitlab-merge-api`

Other skills activate from request text, labels, branches, and changed paths.

## Docker

Compose services:

- `7review`: Go agent
- `headroom`: Headroom bridge
- `mempalace`: MemPalace bridge

Validate Compose configuration:

```sh
make docker-config
```

Common Docker commands:

```sh
make setup
make docker-build
make docker-up
make docker-status
make docker-logs
make docker-tui
make docker-down
```

Parallel review controls:

- `WEBHOOK_WORKERS`: number of PR/MR review jobs the agent may process at once
- `WEBHOOK_QUEUE_SIZE`: accepted webhook backlog

For example, `WEBHOOK_WORKERS=2` lets two webhook review jobs run through the
pipeline concurrently. Model routing itself is controlled by `orchestrator.yaml`
or by `PROVIDER`, `REVIEW_MODEL`, and `SMALL_MODEL` overrides.

Run bridge tests:

```sh
python3 docker/headroom-bridge/app_test.py
python3 docker/mempalace-bridge/app_test.py
```

## Development

Format and test:

```sh
gofmt -w ./cmd/7review ./agent/...
go test ./...
```

Additional verification:

```sh
python3 -m py_compile docker/headroom-bridge/app.py docker/mempalace-bridge/app.py
docker compose config
```

Run the opt-in live model smoke through OpenRouter without starting Ollama:

```sh
make live-smoke-openrouter
```

This target reads `OPENROUTER_API_KEY` from the ignored local `.env` and forces
the reasoner and formatter to `openrouter/free`. The normal test suite and
`make compose-smoke` remain deterministic and do not require that secret.
