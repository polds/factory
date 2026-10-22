# 04 — Protocols and APIs

Three interfaces: the **worker protocol** (control plane ↔ data plane), the **REST + SSE
API** (clients ↔ control plane), and the **MCP endpoint** (external coding agents ↔
factory).

---

## 1. Worker protocol

Wire-compatible with the observed `oz-agent-worker` protocol (O3), so an
unmodified MIT worker can serve a self-hosted control plane (O2).

### Transport

```
WebSocket   <scheme>://<host>/api/v1/selfhosted/worker/ws?worker_id=<id>
Auth        Authorization: Bearer <worker-api-key>      (upgrade request headers)
Heartbeat   worker → server PING control frame every 30s; server MUST pong
            within PongWait (60s) or the worker drops the connection
Reconnect   exponential backoff, 1s → 60s cap, reset on success
Envelope    {"type": "<message_type>", "data": { … }}
Client lib  gorilla/websocket
```

> **The heartbeat is a ping frame, not a message.** The worker's type enum declares a
> `heartbeat` constant, but liveness is implemented as `conn.WriteControl(PingMessage, …)`
> — see [01 · O3](01-evidence-and-provenance.md#o3--the-workerserver-protocol-is-six-json-messages-plus-a-ping).
> Most server libraries pong automatically; if yours does not, every worker will
> disconnect after 60 seconds. The M0 spike asserts one ping/pong exchange precisely so
> this cannot regress silently.

Server identifies the worker by the key's team scope plus the `worker_id` the worker
declares. Worker IDs prefixed `warp` are reserved upstream; we reserve `factory` and
refuse to start on a collision — the same guard, renamed.

Multiple worker processes **may share one `worker_id`** for load balancing. The scheduler
therefore treats `worker_id` as a routing label and `connection_id` as the identity of a
process, which is why `workers.pool` exists in the schema
([02](02-architecture.md#core-tables)).

### Messages

**Server → worker**

`task_assignment`
```jsonc
{
  "task_id": "run_01J…",          // logical run; stable across follow-ups
  "execution_id": "exec_01J…",    // this concrete launch
  "task": {
    "id": "run_01J…",
    "title": "Fix broken doc link",
    "task_definition": { "prompt": "…" },
    "created_at": "2026-10-12T19:00:00Z",
    "updated_at": "2026-10-12T19:00:00Z",
    "owner": { "Type": "TEAM", "Id": 42 },        // USER | TEAM
    "agent_conversation_id": "conv_01J…",
    "agent_config_snapshot": {
      "environment_id": "env_01J…",
      "base_prompt": "…",
      "model_id": "claude-5-sonnet-high",
      "profile_id": "prof_01J…",
      "skill_spec": "…",
      "mcp_servers": { "sentry": { /* opaque to the worker */ } },
      "computer_use_enabled": true,
      "computer_use_model_id": "claude-5-sonnet-high",
      "idle_timeout_minutes": 15,
      "harness": { "type": "claude" },
      "harness_auth_secrets": { "claude_auth_secret_name": "ANTHROPIC_API_KEY" },
      "inference_providers": {
        "aws": { "disabled": false, "role_arn": "arn:…", "region": "us-west-2" }
      },
      "session_sharing": { "public_access": "VIEWER" },   // VIEWER | EDITOR
      "snapshot_disabled": false,
      "snapshot_upload_timeout_secs": 300,
      "snapshot_script_timeout_secs": 120,
      "repository_head_overrides": [
        {
          "code_forge": "GITHUB", "repo_owner": "acme", "repo_name": "payments-docs",
          "head": { "type": "COMMIT_SHA", "value": "0123…4567" },
          "clone_from": { "code_forge": "GITHUB", "owner": "acme-bench", "repo": "payments-docs" },
          "preserve_origin": true
        }
      ]
    }
  },
  "docker_image": "ghcr.io/acme/payments-ci:2026-10",
  "sidecar_image": "ghcr.io/acme/factory-harness-claude:1.4.0",
  "additional_sidecars": [
    { "image": "ghcr.io/acme/toolbox:1", "mount_path": "/mnt/toolbox", "read_write": false }
  ],
  "env_vars": { "GITHUB_ACCESS_TOKEN": "…", "FACTORY_API_KEY": "…" },
  "additional_oz_args": ["--skill", "repository-conventions"],
  "instance_shape": { "vcpus": 4, "memory_gb": 8 },
  "telemetry_collection": {
    "endpoint": "https://otel.internal:4318",
    "tracing_enabled": true, "logging_enabled": true,
    "bootstrap_token": { "token": "…", "expires_at": "2026-10-12T20:00:00Z" }
  }
}
```

`task_cancellation` — `{"task_id": "run_01J…"}`

**Worker → server**

| Message | Payload | Meaning |
| --- | --- | --- |
| `task_claimed` | `{task_id, worker_id}` | Accepted; server marks assigned |
| `task_completed` | `{task_id, execution_id, message, task_state?}` | Agent process exited 0 |
| `task_failed` | `{task_id, execution_id, message, task_state?, failure_reason, exit_code, failure_details?}` | Launch or run failed |
| `task_rejected` | `{task_id, reason}` | **Requeue, do not fail** (O4) |

Liveness is the ping frame described above, not a row in this table.

`exit_code` is normalized to `128+signal`. `failure_details.kubernetes` carries a bounded,
privacy-filtered snapshot: `schema_version`, `observation_source`, `observed_at`, Job
(name/uid/conditions), Pod (name/uid/phase/reason/deletion_timestamp/conditions),
Container (kind/name/state/waiting_reason/termination_reason/raw_exit_code/
normalized_exit_code/signal/started_at/finished_at), Events (type/reason/count/first and
last observed), and `truncated`.

**Design rule:** keep `failure_reason` orthogonal to agent outcome. A run that fails
because a pod was evicted is not a quality signal, and scorers must never sample it.

**M6 applied.** Eligibility does not test `failure_reason == null`. The class
rule is in [08](08-measure-and-self-improvement.md#eligibility-and-sampling).
factoryd fills `repository_head_overrides` and `skill_spec` on the assignment
when the run has them. What the in-repo harness does with the override flag
and with skill text is in [05](05-harness-contract.md#invocation).

### Server obligations

1. Never assign to a worker whose heartbeat is older than 90 s.
2. On `task_rejected`, requeue with `attempts++` and no failure record.
3. On missing terminal message past the lease, requeue as a **new `execution_id`** and mark
   the prior execution `orphaned` — never reuse an execution ID.
4. Treat `env_vars` as secret-bearing: redact in logs, never persist, mint short-TTL
   values ([11](11-security-model.md)).
5. Accept terminal messages idempotently — a worker may retry after a reconnect.

### Native mode (protocol v2)

`factory-worker` adds a `factory` mode with three additions, negotiated by a
`protocol_version` field the upstream worker simply omits:

- `run_event` (worker → server): streams NDJSON harness events during the run instead of
  only at terminal state, so the console gets live transcript without polling
  ([09](09-web-console.md)).
- `capacity_report` (worker → server): pool labels, platform, in-flight count, and free
  slots, so the scheduler stops guessing.
- `task_steer` (server → worker): injects a follow-up turn or interrupt into a live
  session — the "attach for steering" capability.

A v1 worker loses live events and `task_steer`, keeps everything else.

**M5 steering (shipped).** Steering on the unmodified v1 worker is the harness
long-poll, described in [05](05-harness-contract.md#output-ndjson-events). The
harness probes `GET /api/v1/agent/runs/{id}/steer?wait=0` at run start and emits
`run.capabilities` with `"steer": true` once, before the first model call, when
that probe returns 200. `wait=0` returns whatever is queued, and an empty list
is a successful probe. A missing or positive `wait` long-polls, clamped to 25s.
A malformed `wait` is 400. A 404 or 501 leaves the capability unannounced and
stops polling. A 5xx or a network error leaves it unannounced; the harness backs
off and polls again. Later polls do not emit the event again. The `after` query parameter is the
acknowledgement of applied steer ids. `POST /agent/runs/{id}/interrupt` and
`POST /agent/runs/{id}/followups` return `202` `{steer_id}` or `409` with the
reason.

---

## 2. REST API

`/api/v1`, JSON, `application/problem+json` for errors. Bearer tokens (service account
keys) or OIDC access tokens. `X-Factory-Team` header scopes multi-tenant deployments.

The shape follows the observed Oz Agent API (O9) because it is a reasonable
decomposition and because matching it lets existing SDK-shaped tooling port.

### Factories and definitions

```
GET    /factory                             list (by name or alias, paginated)
POST   /factory                             create
GET    /factory/{uid}
PATCH  /factory/{uid}
GET    /factory/{uid}/definition            current applied tree + git sha
POST   /factory/{uid}/definition:validate   dry-run a tree, returns per-file diagnostics
POST   /factory/{uid}/definition:apply      apply atomically
GET    /factory-files/schemas               supported versions + current
GET    /factory-files/schemas/{version}     all documents, keyed by name
GET    /factory-files/schemas/{version}/{document}
```

Schema endpoints are **unauthenticated** — they are public contract, and requiring auth
breaks editor integration for exactly the people you want validating before they push.

### Work items and runs

```
GET    /factory/{uid}/tasks                 work items (filter: stage, status, source)
POST   /factory/{uid}/tasks                 create a work item (intake)
GET    /factory/{uid}/tasks/{task_uid}
POST   /factory/{uid}/tasks/{task_uid}/cancel
GET    /factory/{uid}/task-by-conversation  resolve by conversation id
GET    /factory/{uid}/task-by-run           resolve by run id
GET    /factory/{uid}/runs                  runs within a factory

POST   /agent/run                           start a single run (no work item)
GET    /agent/runs                          list (filter: agent, status, worker, model)
GET    /agent/runs/{runId}
POST   /agent/runs/{runId}/cancel
POST   /agent/runs/{runId}/interrupt        stop the current turn, keep the session
POST   /agent/runs/{runId}/followups        {text} → 202 {steer_id}, or 409 {reason}
GET    /agent/runs/{runId}/steer            run-token long-poll (?after=, ≤ 25s)
GET    /agent/runs/{runId}/transcript
GET    /agent/runs/{runId}/timeline         stage/event summary
GET    /agent/runs/{runId}/conversation     structured turns + tool calls
GET    /agent/runs/{runId}/harness-usage    tokens, cost, per-model breakdown
GET    /agent/run-by-external-reference     resolve by (provider, external id)
```

`run-by-external-reference` is what makes integrations idempotent: a Slack thread or a
Linear issue maps to one run, so a redelivered webhook continues the conversation instead
of starting a parallel one. Getting this wrong produces duplicate agents arguing in one
thread, which is the most visible failure mode of this product class.

### Approvals (new)

```
GET    /factory/{uid}/approvals             pending gates (the inbox query)
POST   /approvals/{id}:decide               {decision: approve|reject, note}
```

Not present upstream because upstream's gates are prompt-resident
([03](03-definition-schema.md#deviation-declarative-pipeline)). Declarative gates need an
API or they cannot be actioned outside the console.

```
POST   /work-items/{id}/input               {text} answers a pending question
```

A harness `question` event (`gate` `clarification`, `text` the question) sets the
work item to `awaiting_input` and releases the worker slot. This route records the
answer and queues the next execution of that run, with the question and the answer
appended to the prompt. The same text returns the stored row. A different text
conflicts. A terminal run is left unanswered.

### Schedules, identities, infrastructure

```
GET|POST       /agent/schedules
GET|PATCH|DEL  /agent/schedules/{id}
POST           /agent/schedules/{id}/pause | /resume
GET|POST       /agent/identities            non-human identity per agent
GET|PATCH|DEL  /agent/identities/{uid}
GET            /agent/environments          image + repo + setup definitions
GET            /agent/models                catalog available to this deployment
GET            /agent/connected-self-hosted-workers
GET            /agent/artifacts/{uid}       metadata
GET            /agent/artifacts/{uid}/download
GET            /agent/conversations/{id}/screenshots/{uid}/download
GET            /networking/egress-ranges    this deployment's egress CIDRs
```

`/networking/egress-ranges` exists so an operator can allowlist the factory in downstream
firewalls. Self-hosted, it reports your worker pools' egress addresses as configured —
which only you can know, so it is operator-declared rather than computed.

### Scoring and benchmarks (new surface)

```
POST   /agent/runs/{runId}/score            on-demand score with one scorer
GET    /factory/{uid}/scorers/{id}/results
POST   /factory/{uid}/benchmarks/{suite}:run
GET    /factory/{uid}/benchmarks/{suite}/trials
GET    /factory/{uid}/improvement-batches
POST   /factory/{uid}/improvement:run-now
```

**M6 applied.** Eleven routes are registered under `/api/v1`. A backend that
does not implement measure returns 501 with detail `measure backend not wired`.
`repetitions` below 1 on a benchmark run returns 422. An ineligible run or an
unknown scorer returns 422. An improvement batch already in flight returns 409.
`:run` and `:capture-benchmark-task` are suffix checks; a path that does not
end in the suffix is 404. Response bodies are the console goldens, not restated
here.

| Method | Path | Success | Golden |
| --- | --- | --- | --- |
| POST | `/api/v1/agent/runs/{runId}/score` | 202 | [`m6-score-accepted.json`](../internal/api/testdata/console/m6-score-accepted.json) |
| GET | `/api/v1/factory/{uid}/scorers/{id}/results` | 200 | [`m6-scorer-results.json`](../internal/api/testdata/console/m6-scorer-results.json) |
| POST | `/api/v1/factory/{uid}/benchmarks/{suite}:run` | 202 | [`m6-benchmark-accepted.json`](../internal/api/testdata/console/m6-benchmark-accepted.json) |
| GET | `/api/v1/factory/{uid}/benchmarks/{suite}/trials` | 200 | [`m6-benchmark-trials.json`](../internal/api/testdata/console/m6-benchmark-trials.json) |
| GET | `/api/v1/factory/{uid}/improvement-batches` | 200 | [`m6-improvement-batches.json`](../internal/api/testdata/console/m6-improvement-batches.json) |
| POST | `/api/v1/factory/{uid}/improvement:run-now` | 202 | [`m6-improvement-run-now.json`](../internal/api/testdata/console/m6-improvement-run-now.json) |
| GET | `/api/v1/factory/{uid}/scorers` | 200 | [`m6-scorers.json`](../internal/api/testdata/console/m6-scorers.json) |
| GET | `/api/v1/agent/runs/{runId}/scores` | 200 | [`m6-run-scores.json`](../internal/api/testdata/console/m6-run-scores.json) |
| GET | `/api/v1/factory/{uid}/benchmarks` | 200 | [`m6-benchmarks.json`](../internal/api/testdata/console/m6-benchmarks.json) |
| POST | `/api/v1/agent/runs/{runId}:capture-benchmark-task` | 200 | [`m6-capture-benchmark-task.json`](../internal/api/testdata/console/m6-capture-benchmark-task.json) |
| GET | `/api/v1/factory/{uid}/metrics` | 200 | [`m6-metrics.json`](../internal/api/testdata/console/m6-metrics.json) |

The last five rows were added in M6: list scorers, list one run's scores, list
benchmarks, capture a benchmark task, and metrics. Capture returns the task
YAML and writes nothing. `GET /api/v1/factory/{uid}/inbox` is unchanged and
now includes kind `improvement_pr`
([`m6-inbox-improvement.json`](../internal/api/testdata/console/m6-inbox-improvement.json)).

---

## 3. Event stream

Live run output over **SSE** at:

```
GET /api/v1/agent/runs/{runId}/events?from_seq=<n>
Accept: text/event-stream
```

(The byte scan found `/api/v1/agent/events/stream` in the Warp client — O13 —
but its payload shape is unknown (U1), so this is specified from first principles rather
than imitated.)

Events are an **append-only, monotonically sequenced** log per run, persisted in
`run_events`. `from_seq` makes reconnection exact: a console that drops its connection
resumes without gaps or duplicates. `from_seq` and `Last-Event-ID` are **exclusive**
cursors — the stream emits events with `seq` **greater than** the cursor (the event
at the cursor was already delivered); when both are present, `Last-Event-ID` wins.
Polling a transcript endpoint cannot give that.

```
event: run.status      {seq, status: queued|assigned|running|…, worker_id}
event: turn.started    {seq, turn, model_id}
event: message.delta   {seq, turn, role, text}
event: tool.call       {seq, turn, tool, input_digest, redacted_input}
event: tool.result     {seq, turn, tool, ok, duration_ms, output_digest}
event: skill.read      {seq, skill_path | bundled_skill_id}
event: file.changed    {seq, path, additions, deletions}
event: screenshot      {seq, artifact_uid}
event: approval.requested {seq, gate, work_item_id}
event: usage           {seq, model_id, input_tokens, output_tokens, cost_cents}
event: run.terminal    {seq, status, failure_reason?, exit_code?}
event: run.capabilities {seq, steer:true}   once, before the first model call
event: question         {seq, gate, text}
event: turn.interrupted {seq}
event: followup.received {seq, steer_id}
```

`tool.call` carries a **digest plus a redacted rendering**, never raw input. Tool inputs
routinely contain secrets, and an audit log that leaks them is worse than no audit log.

`skill.read` distinguishes `bundled_skill_id` from `skill_path` deliberately: usage
attribution for team-owned skills is only meaningful if platform-bundled reads are
excluded, and a path alone does not identify an owner
([07](07-integrations-and-webhooks.md#skills)).

Aggregate stream for dashboards: `GET /api/v1/factory/{uid}/events` — same envelope,
multiplexed, with `run_id` on every event.

**M6 applied.** Inserting a run emits factory event `run.created`. Updating
`runs.status` emits `run.status`. Setting status to the value it already has
emits nothing. The payload is `run_id`, `status`, and `stage` when stage is
not empty. It stores no prompt and no token. Rows live in
`run_lifecycle_events`. They share `run_events.gid` and
`run_events_factory_seq` with harness events, so the factory stream has one
cursor. The cursor is exclusive: `factory_seq` greater than the cursor. A row
that commits after a later gid is assigned a later `factory_seq` on the next
read and is not skipped. The trigger notifies `run_events` with the run id
after commit. A reader that drops, including across a restart, reads again
from the last `factory_seq` it applied. Lifecycle rows are not per-run `seq`
events. `runs.purpose` is separate: it is `work`, `scorer`, `benchmark`, or
`improvement`, it defaults to `work`, and it cannot change after insert.
Measurement runs are not a new `work_items.source`.

---

## 4. MCP endpoint

`factoryd` exposes an MCP server at `/api/v1/mcp/factory` (the path the byte scan
suggests upstream uses — O13; our tool surface is our own) so any MCP-capable
coding agent can use a factory as a tool:

| Tool | Purpose |
| --- | --- |
| `factory_send_work` | Create a work item and return its id |
| `factory_get_status` | Work item stage, runs, blocking gate |
| `factory_get_findings` | Review findings for a work item |
| `factory_reply` | Answer a question the factory asked |
| `factory_schema` | Fetch the definition JSON Schemas |
| `factory_validate` | Validate a definition tree |

This is the integration path for agents that cannot be a factory's execution host but
should be able to **exchange work with one** — a local Claude Code session handing a task
to the factory and polling for the PR.

---

## 5. Authentication

| Principal | Mechanism |
| --- | --- |
| Human (console) | OIDC authorization code + PKCE; Dex in the compose profile |
| Human (CLI) | Device-code flow: `POST /api/v1/oauth/device/auth` → poll `POST /api/v1/oauth/token` |
| Worker | Long-lived worker API key, team-scoped, `Bearer` on the WS upgrade |
| Service account | API key, scoped to a factory and a role |
| Agent (non-human identity) | Per-agent identity, short-TTL token minted per run ([11](11-security-model.md)) |

Device-code is chosen because the byte scan shows the real client uses it (O13)
and because it is the correct flow for a terminal tool — no loopback listener, works over
SSH.

Worker keys are the highest-value credential in the system: a key holder can receive task
assignments containing scoped repo tokens. They are therefore separately scoped from user
keys, rotatable without downtime (overlapping validity), and bound to a pool so a leaked
key cannot drain another pool's queue.
