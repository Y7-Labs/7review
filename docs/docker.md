# Docker Deployment

The runtime stack runs three containers on one private Compose network:

- `7review`: Go webhook server and review pipeline.
- `headroom`: HTTP bridge around Headroom context compression.
- `mempalace`: HTTP bridge around MemPalace memory recall and writes.

Only `7review` publishes a host port. Headroom and MemPalace stay private on the Compose network and are reached through:

```text
HEADROOM_URL=http://headroom:8787
MEMPALACE_URL=http://mempalace:8788
```

The agent image embeds the default input profile, review skills, instructions,
and orchestrator configuration under `/app`. The repository corpus remains an
external read-only mount at `/workspace`.

## Run

Create the local environment file with the setup wizard:

```sh
go run ./cmd/7review setup
```

For guided operational questions, use:

```sh
go run ./cmd/7review chat
```

Or export at least one SCM provider secret and one explicit model provider configuration, then start the stack:

```sh
export GITLAB_URL=https://gitlab.example.com
export GITLAB_TOKEN=...
export GITLAB_WEBHOOK_SECRET=...
export REVIEW_API_TOKEN=$(openssl rand -hex 32)
export OPENAI_API_KEY=...
export CORPUS_ROOT=/path/to/repository/context
make docker-up
```

GitHub can be used instead of GitLab by setting `GITHUB_TOKEN` and
`GITHUB_WEBHOOK_SECRET`.

For local Ollama with Docker Compose, run Ollama on the host and expose it on an
address reachable from containers. The agent should use the Compose network
gateway, not `127.0.0.1`, because localhost inside the agent container is the
container itself.

```sh
sudo systemctl edit ollama
```

Set:

```ini
[Service]
Environment="OLLAMA_HOST=0.0.0.0:11434"
Environment="OLLAMA_MODELS=/usr/share/ollama/.ollama/models"
```

Then restart Ollama and use the Compose network gateway:

```sh
sudo systemctl daemon-reload
sudo systemctl restart ollama
OLLAMA_GATEWAY=$(docker network inspect files_review-agent --format '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)
export PROVIDER=ollama
export OLLAMA_BASE_URL=http://${OLLAMA_GATEWAY:-172.23.0.1}:11434
export REVIEW_MODEL=deepseek-coder-v2:16b
export SMALL_MODEL=qwen2.5-coder-7b-16k:latest
export EMBEDDING_MODEL=nomic-embed-text:latest
docker compose up --build
```

With `ORCHESTRATOR_CONFIG=/app/orchestrator.yaml`, the local harness routes
review reasoning to `deepseek-coder-v2:16b`, formatter/operator chat to
`qwen2.5-coder-7b-16k:latest`, formatter fallback to
`qwen2.5-coder:7b-instruct-q4_K_M`, and embeddings to
`nomic-embed-text:latest`.

For local `go run` commands outside Docker, keep using
`OLLAMA_BASE_URL=http://127.0.0.1:11434`.

If host port `8080` is already used, set `HTTP_PORT`, for example:

```sh
HTTP_PORT=18080 make docker-up
```

## Make Targets

The Makefile wraps the common Docker operations:

```sh
make setup           # interactive .env wizard
make setup-force     # rewrite an existing .env
make docker-config   # validate Compose
make docker-build    # build images
make docker-up       # build and start in the background
make docker-status   # run the agent status command inside the container
make docker-ready    # call /ready through the published host port
make docker-review-gitlab PROJECT_ID=25 MR=19
make docker-review-github REPO=owner/repo PR=7
make docker-logs     # follow agent logs
make docker-tui      # open the operator TUI inside the agent container
make docker-down     # stop the stack
make compose-smoke   # build, wait for health, run status, then clean up
```

`docker-ready` and the manual review targets use `REVIEW_API_TOKEN`. The review
targets execute the authenticated `7review review` command inside the agent
container and send work through the same bounded worker queue as webhooks.

For a repeatable local deployment smoke test that builds the images, waits for
all three services to become healthy, checks `/ready`, exercises Headroom
`/reduce`, writes and recalls a MemPalace vector through `/write` and `/recall`,
and then removes the isolated smoke stack and volumes:

```sh
make compose-smoke
```

## Repository Context Mount

`CORPUS_ROOT` is the local repository or documentation tree that 7review should
scan for review context: `AGENTS.md`, rules, PRD/SRS, ADRs, API specs, threat
models, design tokens, runbooks, and delivery docs. Compose mounts it read-only
at `/workspace` and sets the agent's internal `CORPUS_ROOT=/workspace`.

If `CORPUS_ROOT` is not set, Compose mounts the current directory. For real
reviews, point it at the target repository checkout or a prepared context-pack
directory so the model does not review with the agent image's own files.

Operator endpoints such as `/ready`, `/runs`, `/run`, `/chat/stream`,
`/approve`, `/publish/final`, and `/tools` require `Authorization: Bearer
$REVIEW_API_TOKEN` or `X-7review-Token: $REVIEW_API_TOKEN`. Webhook endpoints
remain protected by their provider-specific webhook secrets.

The HTTP server uses bounded production defaults: `HTTP_READ_HEADER_TIMEOUT_MS`,
`HTTP_READ_TIMEOUT_MS`, `HTTP_WRITE_TIMEOUT_MS`, and `HTTP_IDLE_TIMEOUT_MS`.
The defaults are suitable for webhook/API traffic while allowing long enough
streaming responses for chat.

Headroom and MemPalace calls use separate defaults because compression and
memory indexing can exceed ordinary HTTP request latency:

```sh
HEADROOM_TIMEOUT_MS=30000
MEMPALACE_TIMEOUT_MS=240000
MEMPALACE_CLI_TIMEOUT_SECONDS=180
```

Readiness is available at `/ready`. The response includes dependency status and
worker queue counters so operators can see backlog and failed worker executions.
In environments where host loopback is restricted, check it from inside the
Compose network:

```sh
curl -H "Authorization: Bearer $REVIEW_API_TOKEN" http://localhost:${HTTP_PORT:-8080}/ready
```

## Parallel Reviews

7review has two different concurrency controls:

- `WEBHOOK_WORKERS`: how many PR/MR review jobs can run at the same time.
- reasoner `max_parallel` in `orchestrator.yaml`: how many diff batches one
  review may fan out to model calls.

The default Docker setup uses:

```sh
WEBHOOK_WORKERS=4
WEBHOOK_QUEUE_SIZE=32
WEBHOOK_REVIEW_MODE=manual_first
REVIEW_LABEL_INCLUDE=7review,ready-for-review
REVIEW_LABEL_EXCLUDE=no-review,wip,draft
POLICY_V2_MODE=legacy
POLICY_V2_PATH=.7review/review.yaml
POLICY_RUNTIME_CAPABILITIES=repo.read,model.review
```

With `manual_first`, webhook deliveries are accepted but only enqueue a review
when include policy matches and no exclude/allowlist rule rejects the event. Use
`WEBHOOK_REVIEW_MODE=auto` for the previous always-review webhook behavior, or
`WEBHOOK_REVIEW_MODE=off` to accept valid webhook payloads without enqueueing.

`POLICY_V2_MODE=preview` loads `.7review/review.yaml` from the verified PR/MR
base SHA and records the resulting policy without changing legacy admission.
After inspecting preview results, `POLICY_V2_MODE=enforce` applies repository
triggers and fails closed on missing or invalid trusted policy. Runtime capability
inventory comes from `POLICY_RUNTIME_CAPABILITIES`; required policy capabilities
must be present in that inventory.

`WEBHOOK_WORKERS=2` allows two review jobs to be active at the same time.
`max_parallel` controls batch fan-out inside a single review.

For a cloud override, use environment model overrides or a custom
`ORCHESTRATOR_CONFIG`:

```sh
export WEBHOOK_WORKERS=2
export PROVIDER=openrouter
export OPENROUTER_API_KEY=...
export REVIEW_MODEL=openrouter/free
export SMALL_MODEL=openrouter/free
make docker-up
```

That keeps the current pipeline behavior while moving model execution to the
configured cloud provider.

For an isolated live pipeline check, keep the key in the ignored local `.env`
and run `make live-smoke-openrouter`. This path uses fake SCM, publisher, and
memory boundaries, calls `openrouter/free`, and does not require Ollama.

## Network Shape

There is one Docker network, `review-agent`. That is enough for this stage:

- The agent calls Headroom and MemPalace over private service DNS.
- External webhook traffic enters only through the agent's published HTTP port.
- MemPalace persists durable data in the `mempalace-data` volume.

Add separate networks later only if deployment policy requires stricter isolation, for example a public ingress network plus a private dependency network.

## Python Integration

Python is not embedded in the Go process. It exists only in sidecar images:

- `docker/headroom-bridge` pins `headroom-ai==0.36.5`. The `all` extra is avoided
  because it pulls GPU, benchmark, OCR, and local embedding dependencies that
  are not required by this agent's context-reduction contract.
- `docker/mempalace-bridge` pins `mempalace==3.8.0` and initializes with
  `--no-llm` plus closed stdin, so startup cannot probe an external model or
  block on an interactive prompt.

The Go service depends only on the strict HTTP contracts documented in `docs/integrations.md`.

Both Python sidecars run as UID `10001`; the Go image uses Distroless `nonroot`.
Compose drops Linux capabilities, enables `no-new-privileges`, and mounts root
filesystems read-only. Writable state is limited to named volumes and bounded
temporary filesystems. JSON logs rotate at 10 MB with three retained files.

## Durable State

The agent persists run state, draft reports, HIL approval state, and final
reports under `MEMORY_DIR/runs`. In Docker this is mounted as the `review-data`
volume at `/data/7review`, so review iteration survives container restarts.
MemPalace keeps its own durable memory in the separate `mempalace-data` volume.
Raw approved memory is written under `/data/source`; MemPalace indexes it into
`/data/palace`. Keeping source and index separate prevents recursive mining of
the database itself.
Headroom model/cache artifacts use `headroom-cache`; they are operational cache,
not approved review memory, and can be recreated.

## Continuous Verification

`.github/workflows/runtime.yml` runs `make verify` and then `make compose-smoke`
for pull requests and pushes to `main`. The second job validates the installed
upstream packages inside their built images, so API drift cannot be hidden by
the bridge unit-test doubles.
