# 01 — Evidence and provenance

Every load-bearing claim in this design is tagged by evidence class. Read this before
relying on anything else here.

| Class | Meaning |
| --- | --- |
| **observed** | Read directly from a shipped artifact or published contract |
| **derived** | Follows deterministically from an observation |
| **inferred** | Our reasoning; plausible, not established |
| **unknown** | Explicitly unresolved, with the probe that would resolve it |

No claim in this design asserts that Warp's running system was *executed* or
*intercepted*. Nothing was observed at runtime.

## Artifacts examined

| Artifact | Identity | Class of evidence |
| --- | --- | --- |
| `warpdotdev/oz-agent-worker` | `63dbb8ae8ccb3941392691f5c628e3365d8e248e`, MIT, Go | source (complete) |
| `docs.warp.dev/openapi.yaml` | OpenAPI 3.0.0, "Warp Platform API" v1.0.0, 246 KB, 40 paths | published contract |
| `docs.warp.dev/_llms-txt/factories.txt` | 489 KB, complete Factories doc set | published documentation |
| `docs.warp.dev/_llms-txt/automation-platform.txt` | 417 KB | published documentation |
| `warpdotdev/warp-factory-examples` | `main`, 8 example trees, schema `v1alpha1` | source (complete) |
| `Warp.app/Contents/MacOS/stable` | Mach-O 64-bit arm64, 458 MB, SHA-256 `fd156b28…95c4a1` | shipped binary (byte scan only) |

The worker image checks out `63dbb8ae8ccb3941392691f5c628e3365d8e248e`
(`ARG WORKER_REF` in `deploy/worker/Dockerfile`).

### Tooling boundary on the binary

REA opened the Mach-O and confirmed its identity, but **all three deep-analysis providers
were unavailable on this host**: Ghidra (`not_configured` — `GHIDRA_INSTALL_DIR` unset,
no Java), Hopper (`executable_missing`), IDA (`not_configured`). Only the portable
`inspect_macho` capability was available, and its complete response exceeded the MCP
transport budget (retained as evidence `ev_7a3b8b00…fadf551`).

No disassembler was installed to work around this — installing one would have been an
unrelated change to the machine.

Consequently the binary contributed exactly one thing: a **raw byte scan** (`strings`)
for endpoint candidates. That is weaker than provider-defined string objects — it proves
a byte sequence is present in the file, **not** that any code path references it, and
Rust binaries are known to embed string fragments that are never used. Treat every
binary-derived endpoint below as a *candidate*.

## Observations that drive the design

### O1 — The data plane is MIT licensed
`oz-agent-worker/LICENSE` is the MIT License, "Copyright (c) 2026 Denver Technologies,
Inc." **Class: observed.** → We may fork and redistribute with attribution.

### O2 — The worker's server endpoint is configurable
`main.go:32-33`:
```go
WebSocketURL  string `default:"wss://oz.warp.dev/api/v1/selfhosted/worker/ws" hidden:""`
ServerRootURL string `default:"https://app.warp.dev" hidden:""`
```
Kong struct tags: `hidden:""` suppresses help output but the flags remain functional.
Kong derives the flag name from the field, so they are **`--web-socket-url`** (not
`--websocket-url`) and `--server-root-url`. `worker.go:35` also reads
`WARP_SERVER_ROOT_URL`. **Class: observed, then confirmed by execution** —
[`spike/m0`](../spike/m0) runs an unmodified worker against our own server and it claims
and completes a task.
→ An unmodified worker can be aimed at a self-hosted control plane. This is the single
most consequential finding in the investigation.

### O3 — The worker↔server protocol is six JSON messages plus a ping
`internal/types/messages.go:11-19` declares seven message-type constants, but
`heartbeat` is **not sent as a JSON message**. `worker.go:436-458` implements liveness as
a WebSocket **ping control frame** via `conn.WriteControl(websocket.PingMessage, …)`,
with a comment explaining that `WriteControl` is the only write method safe to call
concurrently with the data writer. So the wire carries six JSON messages — server→worker
`task_assignment`, `task_cancellation`; worker→server `task_claimed`, `task_completed`,
`task_failed`, `task_rejected` — in a `{type, data}` envelope with `data` as raw JSON,
plus a protocol-level ping.

Transport: WebSocket to `…?worker_id=<id>` with `Authorization: Bearer <api-key>`
(`worker.go:290-297`), ping every 30 s (`worker.go:30`), `PongWait` 60 s, exponential
reconnect backoff (`worker.go:237-275`). Client library is `gorilla/websocket`.
**Class: observed, then confirmed by execution** — the M0 spike logs one ping/pong
exchange against a real worker. Full schema in [04](04-protocols-and-apis.md).

This correction matters: a control plane that expected a JSON `heartbeat` and never
answered the ping would have every worker disconnect after 60 seconds.

### O4 — `task_rejected` is backpressure, not failure
`messages.go:172-177`: "The server should keep the task queued rather than marking it
failed." **Class: observed.** → Our scheduler must implement requeue-on-reject; a worker
at `max_concurrent_tasks` rejects rather than fails.

### O5 — The harness is already pluggable in the protocol
`messages.go:195-214` carries `harness.type` (`oz`, `claude`, `codex`, `gemini`, …) and
`harness_auth_secrets`. `config.go:92-99` maps harness name → image via
`coding_cli_sidecars`, mounted at `/mnt/{harness}-cli-sidecar`. `docker.go:162` executes
`/agent/entrypoint.sh` with assembled args; `worker.go:696` mounts the Warp agent sidecar
at `/agent`. **Class: observed.** → Swapping the `/agent` image swaps the harness without
touching the worker.

### O6 — The agent CLI flag surface
Extracted from worker source (occurrence counts across backends): `--task-id`, `--model`,
`--harness`, `--profile`, `--skill`, `--mcp`, `--environment`, `--share`, `--sandboxed`,
`--bare`, `--computer-use`, `--computer-use-model`, `--no-computer-use`,
`--idle-on-complete`, `--skip-initial-turn`, `--snapshot-upload-timeout`,
`--snapshot-script-timeout`, `--no-snapshot`, `--repository-head-override-json`,
`--remove-repository-origins`, `--bedrock-inference-role`, `--bedrock-role-region`,
`--server-root-url`, `--session-sharing-server-url`, `--global`, `--add`.
**Class: observed.** → This is the vocabulary Harness Contract v1 accepts.

### O7 — Benchmark replay works by freezing repository heads
`messages.go:240-249`: `RepositoryHeadOverride{code_forge, repo_owner, repo_name, head,
clone_from, preserve_origin}`, commented "Currently only populated for benchmark trials.
The server has already validated and frozen these values." **Class: observed.**
→ Benchmarks are implemented as runs with pinned starting state and optional repo
substitution, not as a separate execution path. `clone_from` + `preserve_origin` is how a
trial clones a fork while keeping the original repo's identity.

### O8 — The definition tree and its schema endpoint
Resources: `factory.yaml`, `agents/<n>/agent.md`, `automations/<n>/automation.md`,
`runners/<n>.yaml`, `routers/<n>.yaml`, `scorers/<n>/scorer.md`, `skills/<n>/SKILL.md`,
`webhooks/<n>.yaml`, `benchmarks/<slug>/suite.yaml` + `tasks/<slug>.yaml`.
JSON Schemas are served unauthenticated at
`app.warp.dev/api/v1/factory-files/schemas/v1alpha1/<document>`, and OpenAPI confirms the
paths `/factory-files/schemas`, `/{schema_version}`, `/{schema_version}/{document}`.
**Class: observed.** Field-level detail in [03](03-definition-schema.md).

### O9 — The published API surface
40 paths. Factories (`/factory`, `/factory/{uid}/tasks`, `/runs`, `/cancel`,
`/task-by-conversation`, `/task-by-run`), runs (`/agent/run`, `/agent/runs`,
`/runs/{id}/transcript`, `/timeline`, `/conversation`, `/followups`, `/interrupt`,
`/harness-usage`), schedules (CRUD + `/pause`, `/resume`), identities, environments,
models, artifacts, `/agent/connected-self-hosted-workers`, `/networking/egress-ranges`.
**Class: observed.** → Our API mirrors this shape; see [04](04-protocols-and-apis.md).

### O10 — Control/execution plane split and what crosses it
"Repository clones and execution workspaces stay on your infrastructure"; "Agent context
and model requests still travel through Warp's services"; self-hosted workers send
"results, transcripts, attachments, artifacts, and telemetry" which "can contain code
context". **Class: observed (documentation).**
→ This is precisely the residual dependency a self-hosted design eliminates. Our control
plane is yours, so no boundary crossing exists.

### O11 — Stage and approval policy lives in a prompt
"The first two are workflow policy, written into the foreman's instructions; edit them to
change when the factory checks in." (Approving the spec, answering questions. Merging is
enforced by repository branch protection.) **Class: observed (documentation).**
→ Motivates our declarative `pipeline:` block.

### O12 — Self-improvement thresholds and review routing
Default: a scheduled run triggers after **25 distinct unreviewed failures** for an agent,
or when the oldest unreviewed failure is **7 days** old. `selfImprovement.failedRunThreshold`
accepts 1–50. `reviewerType` ∈ {`admins` (default), `team`, `custom`, `none`}; one eligible
reviewer is requested at random. Failing scorers feed the loop only when the scorer sets
`selfImprovement: true`. Proposals always arrive as pull requests, and may target the
factory definition itself. **Class: observed.**

### O13 — Endpoint candidates from the binary byte scan
`/api/v1/agent/events/stream`, `/api/v1/mcp/factory`, `/api/v1/harness-support/usage`,
`/api/v1/oauth/device/auth`, `/api/v1/oauth/token`, plus `https://app.warp.dev` and a
`wss` scheme fragment. **Class: observed byte presence; function inferred.**
None appear in the published OpenAPI. Their *shape* is unknown (U1).
→ They tell us which capabilities a control plane must offer beyond the documented REST
API: a live event stream, an MCP endpoint, harness usage reporting, and device-code OAuth.
We design those from first principles rather than copying.

## Residual unknowns

Recorded in the REA ledger as `unk_8e0a083f…baa52a7` (severity: medium, domain: protocol).

**U1 — Payload shapes of the undocumented endpoints.**
What are the request/response bodies and transport semantics of
`/api/v1/agent/events/stream`, `/api/v1/mcp/factory`, `/api/v1/harness-support/usage`,
`/api/v1/oauth/device/auth`, `/api/v1/oauth/token`, and the `wss` session-sharing channel?
*Resolve by:* `search_strings` + xrefs once a deep-analysis provider is configured, or
`inspect_web_network_capture` on a HAR of an authenticated console session.
*Design impact:* none blocking — we specify our own, since compatibility with Warp's
client is not a goal. It would only matter if we wanted the Warp desktop app to attach
to `factoryd`.

**U2 — The `oz` CLI's own server contract.**
What does the agent binary call under `--server-root-url` during a run (transcript
streaming, tool-call reporting, snapshot upload)? Unknown; the CLI was not analyzed.
*Design impact:* we define Harness Contract v1 instead, so this is moot unless someone
wants to run the real `oz` against `factoryd`.

**U3 — Scheduler internals.** Fair-share, priority, and worker-selection policy are not
observable from the worker side. Our scheduler design in [02](02-architecture.md) is
**inferred** and should be treated as a first draft to be measured.

**U4 — Work-item state machine.** Stage names are documented (Intake, Triage, Planning,
Building, Reviewing, Human handoff, Complete/Cancelled) and `factory` /
`work_item_stage_changed` exists as a trigger, but the precise transition guards are not
published. Ours is **inferred**.

## Negative results (bounded)

- **No CRDs upstream.** The Warp Helm chart "creates no CRDs or cluster-scoped RBAC
  resources" — it is namespace-scoped only. **Observed.** The CRD/operator model in
  [10](10-kubernetes-and-helm.md) is therefore entirely our own, not a reconstruction.
- **No `warp-agent` sidecar source.** The harness image
  (`docker.io/warpdotdev/warp-agent`) was not pulled or analyzed; Docker was not running.
  Its internals remain unknown, which is why we specify a contract rather than clone one.
- **"Oz" is absent from the marketing page.** The product page describes Factories with no
  mention of Oz; "Oz" appears throughout the docs, the API (`openapi.yaml` is titled the
  "Oz Agent API" in `llms.txt`), the worker repo, and `oz.warp.dev`. Derived reading: Oz
  is the cloud-agent platform layer; Factories is the multi-agent SDLC product built on
  it. This design treats them as two layers of one system, which is how the artifacts are
  organized.
