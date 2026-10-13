# 12 — Build sequence

Ordered so that something real works at the end of every milestone, and so the riskiest
unknown is attacked first.

The riskiest unknown is **not** the control plane — that is ordinary distributed-systems
work with a published API shape to aim at. It is the **harness**: the one component with no
reusable prior art, since `oz` is proprietary (U2). So M1 builds the harness
against the *existing* MIT worker, before any of our own infrastructure exists.

## M0 — Protocol spike ✅ **done**

Prove the central assumption (O2) before building on it.

- Minimal Go WebSocket server at `/api/v1/selfhosted/worker/ws`: accept the bearer token,
  answer the heartbeat ping, send one hand-written `task_assignment`, log the terminal
  message. → [`cmd/m0-spike`](../cmd/m0-spike)
- Run **unmodified** `oz-agent-worker` against it with
  `--web-socket-url` / `--server-root-url`, Docker backend, an `alpine` task image and a
  stub `/agent` sidecar. → [`spike/m0`](../spike/m0)

**Exit criterion met.** Upstream worker `63dbb8a`, clean tree, authenticated → claimed →
ran our harness in a task container → reported `task_completed`, with one ping/pong
exchange observed. `./spike/m0/run.sh` reproduces it and exits non-zero on any regression.

It also paid for itself by falsifying three claims in this design before anything was
built on them: the flag is `--web-socket-url`, the heartbeat is a ping frame rather than a
JSON message, and the harness argv begins with `agent run` positionals. See
[`spike/m0/README.md`](../spike/m0/README.md).

## M1 — Harness Contract v1 (weeks)

- `generic-openai` adapter: a real agent loop (bash, read, write, edit, grep, glob) against
  any OpenAI-compatible endpoint. No vendor dependency.
- NDJSON event emission, redaction against the injected secret set, the exit-code taxonomy.
- `claude-code` adapter.
- `factoryctl harness verify` conformance suite ([05](05-harness-contract.md#conformance)).

**Exit criterion:** a real agent run — clone a repo, change a file, run tests, open a PR —
driven by the unmodified upstream worker and the M0 stub server. At this point the hardest
part is done and everything after it is plumbing.

## M2 — Walking skeleton control plane (weeks)

- Postgres schema and migrations; `run_queue` with `SKIP LOCKED` + `LISTEN/NOTIFY`.
- Worker gateway: full protocol including requeue-on-`task_rejected`, lease expiry,
  orphaned executions.
- Scheduler: pool + platform matching, concurrency caps, priority classes.
- Definition loader for a local directory: parse, validate, apply atomically.
- REST: factories, agents, runs, `/agent/run`, cancel.
- SSE event stream with `from_seq`.
- `factoryctl`: `validate`, `run`, `watch`, `cancel`.
- `docker compose up` works: `factoryd` + Postgres + MinIO + one Docker-backend worker.

**Exit criterion:** `factoryctl run --prompt "…"` on a laptop produces a PR, and
`factoryctl watch` streams the transcript live.

## M3 — Factory semantics (weeks)

- Full definition schema: agents, runners, routers, skills; inheritance and sparse
  overrides; published JSON Schemas.
- Orchestrator: work items, the stage machine, `pipeline:` evaluation, `skip` predicates,
  `revisionLimit`.
- Approval gates: `approvals` table, API, blocking semantics.
- Multi-agent dispatch: foreman → stage agents, parent/child runs, context handoff,
  conversation continuity.
- Git-backed definitions: clone, poll/webhook, pre-merge config check.

**Exit criterion:** an issue flows intake → triage → spec → **approval gate** → implement →
review → PR, with each stage on a different model, and the gate actually blocking.

## M4 — Integrations (weeks)

- Ingress with signature verification, delivery dedup, normalization.
- GitHub first (App installation, scoped tokens, check runs), then Slack, then Linear.
- Trigger matcher with the full filter grammar, including webhook payload patterns with
  their bounds.
- Custom webhooks: three auth modes, signature schemes, immutability rules.
- Schedules: leader-elected cron with catch-up.
- Outbound: thread-per-work-item, progress editing, reply continuation.
- Unmatched-delivery visibility.

**Exit criterion:** label an issue `factory-ready` and a PR appears, with progress in a
Slack thread and a reply there continuing the work.

## M5 — Console (weeks)

Dashboard, work items board, run detail with live transcript and attach/steer, inbox,
definition viewer/editor, automations, workers, secrets.

**Exit criterion:** a day of factory work is operable without `kubectl` or `curl` — and a
failed run's cause is identifiable from the UI alone (agent failure vs. infrastructure).

## M6 — Measure and improve (weeks)

- Scorers: eligibility (including the `failure_reason == null` rule), deterministic
  sampling, judge runs, on-demand scoring.
- Benchmarks: suites, tasks, head-override replay, repo substitution, trial matrix,
  confidence intervals, the minimum-repetition refusal.
- Metrics: the dashboard set, with inline caveats.
- Self-improvement: failure ledger, batching, improvement runs, PRs with
  *Regressions addressed*, reviewer routing, and **every guard rail**
  ([08](08-measure-and-self-improvement.md#guard-rails)) — especially the rule that a
  definition change may not weaken `pipeline.approvals`.

**Exit criterion:** a scorer flags a recurring failure, the loop opens a PR editing a skill,
and a benchmark shows the fix improves the pass rate.

## M7 — Kubernetes (weeks)

- Helm chart: control plane, workers, operator, migration hook, preflight hook.
- Kubernetes-backend worker hardening: preflight RBAC validation, Job watching, structured
  failure capture, `unschedulable_timeout`, graceful drain with a 600 s grace period.
- CRDs + operator + admission webhook; `factoryctl convert` both directions.

**Exit criterion:** `helm install` on a fresh cluster, `kubectl apply` a `Factory` and an
`AgentRun`, get a PR; `kubectl delete agentrun` actually stops the agent.

## M8 — Hardening (ongoing)

Secret providers beyond the default; egress policy templates; per-run-VM backend via
one-shot Direct; gVisor/Kata RuntimeClass support; audit export; computer use with Xvfb and
screenshot artifacts; MCP endpoint; macOS Direct pool; multi-tenancy.

---

## Sequencing rationale

- **Harness before control plane.** It is the only component without prior art. Building a
  beautiful orchestrator that then cannot run an agent is the obvious way to waste a
  quarter.
- **Protocol compatibility from day zero.** Keeping the upstream worker working in CI is a
  free, continuous test of the most intricate interface in the system — and it means four
  execution backends arrive essentially for free.
- **Gates before integrations.** Once untrusted intake is connected, an unenforced approval
  gate is a security problem rather than a missing feature.
- **Measurement before self-improvement.** A loop built on a scorer nobody validated
  proposes confident nonsense.
- **Kubernetes last.** It is the best-understood part and the least uncertain. Doing it
  early feels productive and teaches you nothing.

## Testing strategy

| Layer | Approach |
| --- | --- |
| Protocol | Golden-file tests of every message, both directions, plus a conformance run against the **unmodified upstream worker** in CI |
| Harness | `harness verify` fixture suite per adapter; a planted-secret redaction test |
| Scheduler | Deterministic simulation: inject reject/timeout/disconnect/eviction sequences, assert no run is lost or double-run |
| Definition | Property tests on inheritance (sparse override, replace-not-merge, `required_repos` extension); corpus of valid and invalid trees |
| Orchestrator | Table-driven stage-machine tests, including revision loops hitting `revisionLimit` and gates blocking |
| Ingress | Replayed real provider payloads; signature verification; duplicate-delivery idempotency |
| Security | Negative tests: secret never in a `tool.call` event or an API response; weakening `approvals` is rejected; expired forge token cannot push |
| End-to-end | A fixture repository with seeded issues; a nightly run of the full pipeline on a local model so e2e does not depend on a paid API |

The deterministic scheduler simulation is the highest-value test in the list. Every
failure mode in this architecture that actually loses work — lease expiry, reject storms,
worker disconnects mid-run, duplicate terminal messages — is a concurrency bug that will
not reproduce under manual testing.

## Open questions to resolve with measurement

- Scheduler policy (U3) is inferred. Instrument time-to-assignment and reject
  rate from M2 and revisit once there is real load.
- The stage-skip predicates (`whenScopeClear`, `whenIntakeHasScope`) need definition. Start
  conservative — never skip — and loosen with evidence from scorer results.
- Is `generic-openai` good enough to be the reference harness, or does a credible factory
  require a frontier-model adapter? Decide with a benchmark, not an opinion.
