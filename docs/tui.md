# TUI

The command-line interface is an operational TUI for setup and day-to-day checks.

## Commands

```sh
7review setup
7review status
7review status --server http://localhost:8080
7review tui --server http://localhost:8080
7review tui --refresh 5s --server http://localhost:8080
7review tui --once --server http://localhost:8080
7review sessions --server http://localhost:8080
7review sessions --status drafted --provider github --limit 10 --server http://localhost:8080
7review sessions --query validation --server http://localhost:8080
7review session <project!mr> --server http://localhost:8080
7review session <project!mr> --type chat_message --limit 5 --server http://localhost:8080
7review history <project!mr> --server http://localhost:8080
7review history <project!mr> --type chat_message --limit 20 --server http://localhost:8080
7review chat
7review chat <project!mr> --server http://localhost:8080
7review chat --run <project!mr> --server http://localhost:8080
```

`setup` writes a local `.env` file with `0600` permissions. It asks for:

- run mode: Docker or local
- SCM provider: GitLab or GitHub
- one model provider
- Headroom and MemPalace defaults
- HTTP port and webhook worker sizing
- HTTP server timeout defaults
- corpus root for target repository knowledge when running Docker

`status` renders local config state by default. With `--server`, it calls the
agent's authenticated `/ready` endpoint and renders pipeline, orchestrator,
run-store, queue, Headroom, and MemPalace readiness. Set `REVIEW_API_TOKEN`
when the server requires operator auth.

Canonical accounting controls are separate from those legacy run commands:

```sh
7review attempt <exact-attempt-id> --server http://localhost:8080 --json
7review cancel-attempt <exact-attempt-id> --command-id <stable-id> --expected-version <version> --revision <revision> --reason "operator stop" --server http://localhost:8080
```

Set `REVIEW_OPERATOR_TOKEN` to a credential explicitly mapped by the server;
`REVIEW_API_TOKEN` does not implicitly establish that identity. Canonical review
admission prints the exact attempt ID. Cancellation requires an explicit observed
version/revision and stable command ID; it never resolves a selected run alias or
retargets a conflict. Remote hosts require HTTPS; loopback HTTP is supported.
See [canonical operator setup and retry behavior](accounting.md#cancellation-commands).

`tui` runs as an interactive review workspace by default. The live screen keeps
a transcript/output pane on the left, a compact context rail on the right, and a
fixed composer at the bottom. Type slash commands directly in the composer:
`/status`, `/sessions`, `/run`, `/history`, `/history chat_message 20`, `/diff`,
`/draft`, `/memory`, `/approve --report-file final.md`, and
`/publish-final --report-file final.md`. Type a normal message when a run is
active to stream model chat through the running agent's `/chat/stream` endpoint.
`/attempt <exact-attempt-id>` and `/cancel-attempt ...` are also available without
a selected run. Their flags match the CLI except that the TUI's server is pinned.
Interrupting these requests cancels the HTTP context, not an already-committed
server action; an unknown outcome is reported for exact-command reconciliation.
Press `r` to refresh immediately, `?` to show or hide command help, and `q`,
`esc`, or `ctrl+c` to exit when the input is empty. Use `--once` for a single
non-interactive dashboard snapshot.

The live TUI reads `/ready`, `/tools`, `/tools/execute`, and run-chat streaming
endpoints. The `--once` snapshot prints recent run activity, current run
commands, dependency state, queue state, provider routing, review progress,
loaded skills, and tool count without entering the interactive workspace.

`sessions` renders a compact human-readable list of persisted review sessions
from the run store. It uses the same authenticated `list_runs` tool as the TUI,
orders sessions newest first, and shows the run id, provider, status, update
time, history count, title, and normalized change id. Filter with `--status`,
`--provider`, `--query`, and `--limit`; query search matches run id, provider,
project id, change id, title, status, and URL. `7review runs` remains available
for raw JSON output.

`session` renders one persisted review session as a readable operator summary.
It shows the same run fields used by chat `/run`, then prints exact follow-up
commands for chat, timeline inspection, TUI focus, approval, and final publish.
It includes the latest five events by default; use `--type` and `--limit` to
focus the embedded event slice.

`history` renders a run timeline from `/run?id=<run-id>`, including lifecycle
events and persisted run-chat events. Use `--type` to filter one event type and
`--limit` to show only the latest matching events.

`chat` is an interactive, streaming, model-driven operator surface for setup,
status, Docker, sidecars, webhooks, review iteration, and next steps. It uses
the configured orchestrator formatter role. OpenAI-compatible providers and
Ollama stream token chunks natively; providers without a streaming client use a
single compatibility response until their streaming APIs are implemented. If
configuration is invalid, chat falls back to a local message that tells the
operator to run setup and status first.

For iterative review, pass a run ID:

```sh
7review chat project!42 --server http://localhost:8080
7review chat --run project!42 --server http://localhost:8080
```

That mode streams through the running agent's `/chat/stream` endpoint, so the
model sees the stored draft report, validated findings, status, and SCM URL for
that specific review run. Inside chat, local commands stay in the terminal flow:
`/status` checks agent readiness, `/tools` lists implemented agent tools,
`/providers` shows model provider and role routing, `/config` shows redacted
runtime configuration, `/skills` lists loaded agent skills, `/sessions` lists
review sessions, `/sessions drafted 5` filters recent sessions, and
`/sessions validation 5` searches by text. `/run` shows the current session
summary, `/diff` shows changed files and patch summary, `/history` shows the
run timeline, and
`/history chat_message 20` shows the latest persisted chat messages without
sending that command to the model. `/memory` previews the approved MemPalace
proposal. `/draft` prints the current draft report, and `/draft final.md` writes
it to a local file for review or editing. HIL actions are explicit:
`/approve --report-file final.md` submits human approval and
`/publish-final --report-file final.md` retries final publishing for an already
approved run.

Operator HTTP commands use bounded clients: normal commands time out after 60
seconds, while streamed run chat has a longer bounded timeout for interactive
responses.
