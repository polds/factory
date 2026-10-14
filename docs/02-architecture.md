# 02 — Architecture

## Planes

Warp splits control plane (coordination, identity, observability, inference routing) from
execution plane (checkout, commands, tools) and keeps the control plane hosted — so
prompts, transcripts, artifacts and telemetry cross the boundary even in self-hosted mode
(evidence O10). This design collapses that: **you own both planes.** Nothing leaves your
network except model-provider calls you explicitly configure.

```
┌───────────────────────── your network ──────────────────────────────┐
│                                                                     │
│  ingress            factoryd (control plane)          workers       │
│  ┌──────────┐      ┌──────────────────────────┐    ┌─────────────┐  │
│  │ webhooks │─────▶│ trigger matcher          │    │ factory-    │  │
│  │ slack    │      │ automation engine        │◀──▶│  worker     │  │
│  │ linear   │      │ orchestrator (work items)│ WS │  (pool)     │  │
│  │ schedules│      │ scheduler + queue        │    └──────┬──────┘  │
│  └──────────┘      │ definition sync (GitOps) │           │ spawns  │
│                    │ inference gateway        │    ┌──────▼──────┐  │
│  ┌──────────┐      │ secrets broker           │    │ task sandbox│  │
│  │ console  │◀────▶│ event bus + SSE          │    │ repo+/agent │  │
│  │ factoryctl│     │ REST API · MCP server    │    └──────┬──────┘  │
│  └──────────┘      └───┬───────────┬──────────┘           │         │
│                        │           │                      │         │
│                   ┌────▼────┐ ┌────▼─────┐                │         │
│                   │Postgres │ │ S3/MinIO │◀───────────────┘         │
│                   └─────────┘ └──────────┘   transcripts, artifacts │
└─────────────────────────────────┬───────────────────────────────────┘
                                  │ only egress: model providers
                          ┌───────▼────────┐
                          │ Anthropic/     │
                          │ OpenAI/Bedrock/│
                          │ Vertex/local   │
                          └────────────────┘
```

## Components

### `factoryd` — control plane
One Go binary, horizontally scalable, stateless except for leader-elected loops. Modules:

| Module | Responsibility |
| --- | --- |
| **ingress** | Webhook receipt, signature verification, delivery dedup, provider event normalization |
| **trigger matcher** | Evaluates automation trigger filters against normalized events |
| **automation engine** | Starts runs from matched triggers and schedules |
| **orchestrator** | Owns work-item state, stage transitions, approval gates, agent dispatch |
| **scheduler** | Assigns runs to worker pools; handles reject/requeue, concurrency, priority |
| **worker gateway** | WebSocket endpoint; auth, heartbeat, assignment, terminal state intake |
| **definition sync** | Clones/validates the definition tree, reconciles into resources (GitOps) |
| **inference gateway** | Provider adapters, model routers, token accounting, redaction |
| **secrets broker** | Resolves per-agent secret allowlists into scoped, short-lived injections |
| **event bus** | Append-only run events; fans out to SSE, console, audit |
| **api** | REST + SSE; serves the console SPA; serves JSON Schemas; MCP endpoint |

Leader-elected singletons: scheduler tick, cron evaluator, self-improvement scheduler,
scorer sampler, definition poller.

### `factory-worker` — data plane
Fork of MIT `oz-agent-worker` (O1). Keeps its four backends and its protocol
(O3). Changes we make:

1. Add a native `factory` protocol mode alongside wire-compatible `oz` mode.
2. Replace `warp-agent` sidecar defaults with our harness images.
3. Add pool labels so the scheduler can target capability, not just a worker ID.

An unmodified upstream worker also works (O2) — useful for validating protocol
compatibility in CI.

### `factory-operator` — CRD reconciler
Only in Kubernetes mode. Watches `factory.dev/v1alpha1` resources and projects them into
the control plane's resource store, so a cluster can be the source of truth instead of a
Git tree. See [10](10-kubernetes-and-helm.md).

### `factoryctl` — CLI
Definition validation (offline, no server needed — upstream ships a Python validator
script; ours is in-process), run start/watch/cancel, worker registration, secret
management, device-code login.

## Storage

**Postgres** is the system of record *and* the queue. No Kafka, no Redis, no NATS in the
default topology — a deliberate constraint so `docker compose up` is honest and a
single-node deployment has three containers, not nine.

- Queue: `SELECT … FOR UPDATE SKIP LOCKED` over `run_queue`, plus `LISTEN/NOTIFY` to wake
  idle schedulers. Good to several thousand runs/minute, far past the point where agent
  compute dominates.
- Optional NATS JetStream adapter behind the same interface for multi-region.

**Object store** (S3 API; MinIO in compose) holds transcripts, tool-call logs, artifacts,
screenshots, and environment image cache metadata. Postgres stores pointers and digests,
never blobs.

### Core tables

```
factories(id, name, alias, schema_version, definition_source, production_ref, …)
definitions(id, factory_id, git_sha, validated_at, applied_at, status)
agents(id, factory_id, name, agent_type, spec_json, definition_id)
automations(id, factory_id, name, enabled, triggers_json, prompt, overrides_json)
runners(id, factory_id, name, platform_json, instance_shape_json, setup_commands)
routers(id, factory_id, name, type, default_model, routing_json)
webhooks(id, factory_id, name, auth_mode, signature_scheme, secret_ref, delivery_id_header)
scorers(id, factory_id, name, labels_json, passing_score, sampling_rate, model, self_improve)
skills(id, factory_id, agent_id NULL, name, path, content_digest)

work_items(id, factory_id, source, source_ref, title, stage, status, created_by, …)
runs(id, work_item_id, agent_id, parent_run_id, conversation_id, status, worker_id,
     harness_json, model_id, instance_shape_json, cost_cents, started_at, ended_at)
executions(id, run_id, attempt, worker_id, state, failure_reason, exit_code,
           failure_details_json)         -- run ≠ execution; see lifecycle below
run_events(id, run_id, seq, ts, type, payload_json)   -- append-only, SSE source
approvals(id, work_item_id, gate, requested_at, decided_at, decided_by, decision, note)

workers(id, pool, labels_json, platform, last_heartbeat_at, max_concurrent, status)
run_queue(run_id, factory_id, pool, priority, enqueued_at, lease_until, attempts)

scorer_results(id, run_id, scorer_id, label, score, passed, reasoning, reviewed_at)
improvement_batches(id, factory_id, agent_id, status, failure_run_ids[], pr_url)
benchmark_suites / benchmark_tasks / benchmark_trials(…)

secrets(id, scope, name, provider, provider_ref, version)       -- never the value
identities(id, factory_id, agent_id, kind, subject, credentials_ref)
audit_events(id, actor, action, subject, ts, payload_json)
```

### Run vs. execution

The worker protocol distinguishes `task_id` (the logical run, reusable across follow-ups
and handoffs) from `execution_id` (the concrete launch) — `messages.go:121-125`,
evidence O3. We model that as `runs` ⟵ `executions`, which is what makes
follow-up turns, retries after infrastructure failure, and idle-timeout reattachment
expressible without inventing a second identifier later.

## Work-item lifecycle

A **work item** is one request, keeping identity from intake to handoff however many
agents touch it (O: documentation). Stages are orchestrator-owned, not
prompt-owned ([03](03-definition-schema.md#deviation-declarative-pipeline)).

```
                      ┌──────────────┐
  intake ────────────▶│   TRIAGE     │
  (webhook/chat/      └──────┬───────┘
   issue/schedule/           │ scope known?
   api/mcp/manual)     no ───┴─── yes
                       │          │
                 ┌─────▼─────┐    │
                 │ PLANNING  │    │
                 └─────┬─────┘    │
                       │          │
              ┌────────▼────────┐ │   gate: approvals.spec
              │ AWAITING_APPROVAL│ │   (default: required)
              └────────┬────────┘ │
                       └────┬─────┘
                       ┌────▼─────┐
                       │ BUILDING │◀──────┐
                       └────┬─────┘       │ revision requested
                       ┌────▼─────┐       │
                       │ REVIEWING├───────┘
                       └────┬─────┘
                       ┌────▼─────────┐  gate: approvals.handoff
                       │ HUMAN_HANDOFF│  (PR open; merge is the repo's call)
                       └────┬─────────┘
                    ┌───────┴────────┐
               COMPLETE          CANCELLED
```

Any stage may also enter `AWAITING_INPUT` when an agent asks a question; that surfaces in
the inbox ([09](09-web-console.md)) and does not consume a worker slot.

Transition guards are **inferred** (U4). They are expressed as data in the
`pipeline:` block so they can be changed without touching orchestrator code.

## Run lifecycle across the planes

```
 factoryd                                   worker                task sandbox
    │                                          │                       │
    │  1. enqueue run (pool, priority)         │                       │
    │  2. lease from run_queue (SKIP LOCKED)   │                       │
    │  3. resolve: agent spec → runner →       │                       │
    │     image, instance shape, harness,      │                       │
    │     MCP servers, secrets, identity       │                       │
    │  4. mint scoped credentials (short TTL)  │                       │
    │ ───────── task_assignment ──────────────▶│                       │
    │ ◀──────── task_claimed ───────────────── │                       │
    │                                          │ 5. pull images        │
    │                                          │ 6. mount /agent       │
    │                                          │ ─── spawn ───────────▶│
    │ ◀═══════ run events (NDJSON → SSE) ══════╪═══════════════════════│
    │                                          │                       │
    │ ───────── task_cancellation ────────────▶│ ─── SIGTERM ─────────▶│
    │ ◀──────── task_completed | task_failed ──│                       │
    │  7. persist transcript/artifacts to S3   │                       │
    │  8. score (sampled), meter cost, audit   │                       │
```

On `task_rejected` the run returns to `run_queue` with `attempts++` and is **not** marked
failed (O4). On lease expiry without a terminal message, the run is retried on
another worker; `execution_id` distinguishes the attempts.

## Scheduler

**Inferred design (U3)** — measure before trusting it.

Selection: a run declares a pool (from `workerHost` or a pool selector) and a platform
requirement (from its runner's `platform`). Candidate workers are those whose pool and
platform match, that have heartbeat within 90 s (3× the 30 s interval, O3), and
whose in-flight count is below `max_concurrent_tasks`.

Among candidates: least-loaded, tie-broken by longest-idle, to spread image cache warmth.

Fairness: per-factory and per-agent concurrency caps, plus a weighted round-robin across
factories so one busy factory cannot starve another. Priority classes: `interactive`
(human waiting — console run, Slack mention) > `pipeline` (stage advance) >
`batch` (scheduled, scorer, benchmark, self-improvement). Self-improvement and benchmark
runs are deliberately lowest so measurement never competes with delivery.

Backpressure: if no worker can accept, the run stays queued and the console shows
`queued (no capacity)` rather than failing — the behavior the protocol's
`task_rejected` semantics imply.

## Deployment profiles

| Profile | Components | Store | Notes |
| --- | --- | --- | --- |
| **laptop** | `factoryd` + 1 worker (docker backend) | Postgres + MinIO | [`deploy/docker-compose.yml`](../deploy/docker-compose.yml); [spike/m2](../spike/m2/README.md); [06](06-execution-backends.md) |
| **single VM** | same, systemd units | Postgres + MinIO/S3 | TLS via reverse proxy |
| **k8s (chart)** | `factoryd` Deployment, worker Deployment(s) | managed PG + S3 | no CRDs needed |
| **k8s (CRDs)** | + `factory-operator` | managed PG + S3 | cluster is source of truth; [10](10-kubernetes-and-helm.md) |
| **hybrid** | `factoryd` central, workers in many networks | — | workers dial out only; no inbound port |

The hybrid profile is the one Warp's architecture makes awkward and ours makes natural:
because the control plane is yours, a worker in a restricted network and a worker in a
cloud VPC can serve the same factory with no third party in the path.

## Failure behavior

| Failure | Behavior |
| --- | --- |
| Worker disconnects mid-run | Lease expires → requeue as new `execution`; prior execution marked `orphaned` |
| `factoryd` restarts | Workers reconnect with backoff (O3); leases recovered from Postgres |
| Postgres unavailable | Workers hold connections; no new assignments; runs in flight continue and report on reconnect |
| Object store unavailable | Run proceeds; transcript upload retried with backoff, then marked `transcript_unavailable` |
| Model provider 429/5xx | Inference gateway retries with jitter, then fails the run with `model_unavailable` (distinct from agent failure) |
| Task pod unschedulable | Worker fails the task after `unschedulable_timeout` (default 10 m, upstream default) |

Failure reasons are carried on the wire (`failure_reason`, `exit_code` normalized to
128+signal, and structured `failure_details.kubernetes` with Job/Pod/Container conditions
and privacy-filtered Events — O3). We keep that taxonomy because it is the
difference between "the agent was wrong" and "the cluster was full", and conflating those
makes every quality metric meaningless.
