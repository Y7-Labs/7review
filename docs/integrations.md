# Headroom and MemPalace Integration

Headroom and MemPalace are required production dependencies, but they are not embedded in the Go binary.

The Go service integrates with them through HTTP sidecar URLs:

- `HEADROOM_URL`
- `MEMPALACE_URL`

In local development these can point at localhost. In Docker or Kubernetes they should point at service/container DNS names on the same network, for example:

```text
HEADROOM_URL=http://headroom:8787
MEMPALACE_URL=http://mempalace:8788
```

The local Docker stack uses one private network named `review-agent` for all
three services. Only the Go agent publishes a host port; Headroom and MemPalace
are internal dependencies.

## Runtime Boundary

The Go agent owns:

- webhook validation
- SCM enrichment
- diff normalization
- skill/corpus selection
- review pipeline state
- model orchestration
- report publishing

Corpus discovery reads from `CORPUS_ROOT`. In Docker, the host path named by
`CORPUS_ROOT` is mounted read-only at `/workspace`, and the agent uses that
mount as the target repository knowledge source.

Headroom owns:

- reducing selected context before model review
- preserving section identity while shrinking payloads

MemPalace owns:

- semantic memory recall before review
- durable memory writes after final/HIL approval

No Python or TypeScript runtime is loaded into the Go process. The Docker deployment must run separate Headroom and MemPalace containers or services.

## Required Connectivity

`/ready` checks both sidecars, the run store, orchestrator wiring, and worker
queue state. It returns structured queue counters including depth, capacity,
enqueued jobs, completed jobs, and failed worker executions. If a required
dependency is unavailable, readiness returns `503`.

Review behavior is strict:

- MemPalace recall failure fails the review run.
- Headroom reduction failure fails the review run.
- MemPalace write failure fails the post-HIL path.

This is intentional for production consistency.

## Approval Channels

Enable the desired channel in `profiles/default.input-profile.json`. The
supported providers are Twilio WhatsApp, Telegram, SimpleX and the internal log
bridge. Configure an explicit sender allowlist for public provider webhooks.

### Telegram

Set `TELEGRAM_BOT_TOKEN` and `TELEGRAM_WEBHOOK_SECRET`; configure `chat_id`
and numeric Telegram user IDs in `authorized_senders`. Usernames are not
authorization identities. Register an HTTPS webhook with Telegram's
`setWebhook` method, supplying `url=https://<host>/channels/telegram/webhook`
and `secret_token` equal to the configured webhook secret.

The server checks `X-Telegram-Bot-Api-Secret-Token`, then authorizes `from.id`.
`CHANNEL_INBOUND_TOKEN` is only for the internal JSON bridge and is not a
second Telegram credential. Long reports are split into ordered messages; a
failed send is returned as an error, including unsuccessful Bot API responses.
Update IDs are recorded as external IDs.
Repeated callbacks with the same channel/external ID enqueue at most one action
within the running server's 24-hour deduplication window. Queue rejection releases
the claim so the provider can retry. This is in-process protection, not durable
exactly-once delivery across restarts.

Canonical finding controls additionally require
`settings.operator_token_env_by_sender`, a JSON object mapping each authorized
provider sender identity to the environment-variable name containing that
principal's operator credential. The environment variable must resolve through
`REVIEW_OPERATOR_GRANTS_JSON` to repository-scoped roles. The profile stores the
variable name, not the secret. Missing, duplicate, unauthorized or ambiguous
mappings fail closed; sender allowlisting alone never creates an operator actor.

The closed channel command is:

```text
/disposition-finding <attempt-id> <finding-id> <accepted|rejected|disputed> <command-id>
<reason>
```

The app authenticates the mapped principal, reads the exact terminal finding from
durable authority, and commits the attributed disposition before acknowledging the
response. It does not mutate a report, enqueue legacy suppression, replay a job or
authorize publication. A retry with a new provider delivery ID replays the same
durable command when its ID, decision and reason match.
Draft notifications include the canonical attempt ID and validated, deduplicated
finding IDs when that authority exists. Invalid or absent identities never produce
a canonical reply template; credentials and finding digests are not sent through
the channel.

### SimpleX

Start `simplex-chat -p 5225` with the bot's existing chat database. Set
`SIMPLEX_WS_URL=ws://127.0.0.1:5225`. Prefer a numeric `contact_id` in channel
settings and the same ID in `authorized_senders`. Alternatively, configure
`contact_name` with the contact's local display name; outbound lookup uses
`/contacts` and rejects missing or ambiguous names. With no explicit allowlist,
the configured contact ID or name is the sole authorized operator.

SimpleX CLI has no API authentication. Plaintext `ws://` is restricted to
loopback addresses. A remote connection must use `wss://` through a trusted TLS
proxy with access control; URL user information supports HTTP Basic auth.
Keep credential-bearing URLs in environment variables rather than committed
profiles. Gorilla WebSocket owns framing, ping/pong and message size limits.
Commands wait up to 30 seconds for a correlated acknowledgement. The listener
reconnects with backoff capped at 30 seconds and closes idle reads on cancellation.
Only `newChatItems` received direct text events are eligible; sent acknowledgements,
group messages and unknown event types cannot authorize publication.

### Commands And Qualification

Use `/approve <run_id>` to authorize publication. Revision and suppression
require a second line containing the instruction or reason:

```text
/revise <run_id>
Focus on authentication

/suppress <run_id> <finding_id>
Reason for suppression
```

Unprefixed commands, extra arguments and ambiguous replies do not publish.
Provider webhooks cannot be submitted through `/channels/<name>/inbound`.
That endpoint is reserved for log channels with a nonempty inbound token.

Before production use, qualify each enabled provider against a disposable review:
verify draft receipt, rejection of unauthorized senders, explicit approval,
revision, suppression and final confirmation. Normal Go tests use simulated
HTTP/WebSocket providers and do not establish live delivery.

References: [SimpleX bot API](https://github.com/simplex-chat/simplex-chat/blob/stable/bots/README.md),
[SimpleX command types](https://github.com/simplex-chat/simplex-chat/blob/stable/bots/api/COMMANDS.md),
and [Telegram Bot API](https://core.telegram.org/bots/api).
