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

## M1 — Harness Contract v1 ✅ **done**

- `generic-openai` adapter: a real agent loop (bash, read, write, edit, grep, glob) against
  any OpenAI-compatible endpoint. No vendor dependency. **Landed** in
  [`cmd/factory-harness`](../cmd/factory-harness).
- NDJSON event emission, redaction against the injected secret set, the exit-code taxonomy.
  **Landed.** `factoryctl harness verify` runs the cases that do not need a live vendor
  ([05](05-harness-contract.md#shipped-adapter)).
- `claude-code` adapter. **Not in this build.** `--harness claude-code` exits 20.
- A real agent run that clones a repo, changes a file, runs tests, and opens a PR,
  driven by the unmodified upstream worker and the M0 stub server. **Done.**
  [PR #12](https://github.com/polds/factory/pull/12), upstream worker `63dbb8a`.

**Exit criterion met.** Upstream worker `63dbb8a`, clean tree, driven by `cmd/m0-spike`
and the `generic-openai` harness: cloned `polds/factory`, changed
`spike/m1/fixture/greet/greet.go` and `greet/greet_test.go` so `Hello` adds `!`,
`go test` in the fixture passed, and opened draft
[PR #12](https://github.com/polds/factory/pull/12) (`m1/run-20261013T222336`).
Model `claude-sonnet-5-5`. Assignment `run_m1_20261013T222336` reached `task_completed`.
The [live workflow run](https://github.com/polds/factory/actions/runs/38477354495)
printed `M1 LIVE PASS`. At this point the hardest part is done and everything after it
is plumbing.

## M2 — Walking skeleton control plane ✅ **done**

- Postgres schema and migrations; `run_queue` with `SKIP LOCKED` + `LISTEN/NOTIFY`.
  **Landed** in [`internal/store`](../internal/store).
- Worker gateway: full protocol including requeue-on-`task_rejected`, lease expiry,
  orphaned executions. **Landed** in [`internal/gateway`](../internal/gateway) and
  [`internal/server`](../internal/server).
- Scheduler: pool + platform matching, concurrency caps, priority classes.
  **Landed** in [`internal/scheduler`](../internal/scheduler).
- Definition loader for a local directory: parse, validate, apply atomically.
  **Landed** in [`internal/definition`](../internal/definition).
- REST: factories, agents, runs, `/agent/run`, cancel. **Landed** in
  [`internal/api`](../internal/api).
- SSE event stream with `from_seq`. **Landed** in [`internal/events`](../internal/events).
- `factoryctl`: `validate`, `run`, `watch`, `cancel`. **Landed** in
  [`cmd/factoryctl`](../cmd/factoryctl).
- `docker compose up` works: `factoryd` + Postgres + MinIO + one Docker-backend worker.
  **Landed** in [`deploy/docker-compose.yml`](../deploy/docker-compose.yml).
  [`spike/m2`](../spike/m2) reproduces it offline, and live on a GitHub-hosted runner.

**CI live exit met.** GitHub-hosted `ubuntu-latest` (`m2-live.yml`,
`workflow_dispatch`) running `deploy/docker-compose.yml`.
`main` at `5a22262`. Upstream worker `63dbb8a`, clean tree, driven by
`factoryd`: `factoryctl run --prompt` created `run_01M4WBDKX75W4JT0G4VD94KW5M`,
which succeeded. `factoryctl watch` streamed transcript lines 19.2s before the
terminal event. The agent cloned `polds/factory`, changed
`spike/m1/fixture/greet/greet.go` and `greet_test.go`, and opened draft
[PR #30](https://github.com/polds/factory/pull/30)
(`m2/run-20261014T043848Z`, one commit by `Agent <agent@example.com>`). Model
`claude-sonnet-5-5`. Archived events for
`exec_01M4WBDKXWNRTJB8FAPYEVK44B` (artifact `m2-harness-events`, id 11870378347)
include `tool.call` `git clone` with `ok:true`. `go test` ran in the next
chained command, recorded `ok:false` because the following `git commit` had no
author identity; the next turn set `user.name` / `user.email` and opened the PR
(`ok:true`). On that PR, `go test ./...` in the fixture reports
`ok example.com/m1fixture/greet`. The
[live workflow run](https://github.com/polds/factory/actions/runs/38512432518)
printed `M2 LIVE PASS`. The job log and `events.ndjson` contain no GitHub
token and no model API key.

**Laptop exit met.** Peter's Mac, `MODE=live ./spike/m2/run.sh` running
`deploy/docker-compose.yml`. `main` at `8abb3ba`. Driven by `factoryd`:
`factoryctl run --prompt` created `run_01M4XFFHXQBVXQRW7Y3S4PRK1T`, which
succeeded. `factoryctl watch` streamed transcript lines ~28.7s before the
terminal event. The agent cloned `polds/factory`, changed
`spike/m1/fixture/greet/greet.go` and `greet_test.go`, and opened draft
[PR #33](https://github.com/polds/factory/pull/33)
(`m2/run-20261014T151017Z`, one commit by `Agent <agent@example.com>`).
The run used a manual `host.docker.internal` workaround; automatic rewrite
of that address is tracked in the pending fix PR.

## M3 — Factory semantics ✅ **done**

- Full definition schema: agents, runners, routers, skills; inheritance and sparse
  overrides; published JSON Schemas.
- Orchestrator: work items, the stage machine, `pipeline:` evaluation, `skip` predicates,
  `revisionLimit`.
- Approval gates: `approvals` table, API, blocking semantics.
- Multi-agent dispatch: four stage agents (triage, planning, building, reviewing),
  parent/child runs, context handoff, conversation continuity.
- Git-backed definitions: clone and poll sync, with an optional Waker. Webhook
  ingress is M4. Pre-merge config check.

**Exit criterion:** an issue flows intake → triage → spec → **approval gate** → implement →
review → PR, with each stage on a different model, and the gate actually blocking.

**Exit met.** Peter's Apple Silicon Mac, Docker Desktop,
`MODE=live APPROVAL_DECISION=approve ./spike/m3/run.sh`, `main` at `8fca87c`
(2026-10-14, about 14:46 PT). T14's combined verify on that commit passed.
Push CI for `8fca87c` is green, including `required` and `m3 offline e2e`:
[run 38630469688](https://github.com/polds/factory/actions/runs/38630469688).
`m3-live.yml` has no workflow runs. This environment has no live credentials,
so `MODE=live` is not re-run here.

`factoryctl task create` created `task_01M4Y5YQAPB6NJE1AXYQFZJRDK`
(`Emphasize Hello`). Planning stopped at `status=awaiting_approval` with
`gate=spec` and approval `apr_01M4Y5ZMYWFVX715GPC3TM4DA1`. The driver printed
`no building run while the spec gate is open`, decided that approval because
`APPROVAL_DECISION=approve`, then printed `building run was created after the
spec gate decision`. That line is the script's check that the building run's
`created_at` is after the approval's `decided_at`. The pasted log does not
include those two timestamps.

The building stage opened draft
[PR #50](https://github.com/polds/factory/pull/50). Head
`61afb3760459bd47f748ef2caf37996ec9d71724` on branch
`m3/run-20261014T214242Z`, one commit `M3: emphasize Hello` by
`Agent <agent@example.com>`. GitHub records the pull request as opened by
`polds` because that account owns the live token. The diff is
`spike/m1/fixture/greet/greet.go` and `greet_test.go`, the files and the title
named in the building prompt (`agents/implementer`,
`gh pr create --draft --title "M3: emphasize Hello"`). Triage and spec are
instructed not to open a pull request. The driver printed the pull request URL
after `Wait for implement and review`, then `M3 LIVE PASS`.

The log line `four runs linked parent to child on distinct model ids` is the
evidence that the four stage runs used different model ids and were linked
parent to child. The laptop `factoryd` is not reachable from here, and the
pasted log does not include the model id strings, so they are not repeated.

## M4 — Integrations ✅ **done**

- Ingress with signature verification, delivery dedup, normalization.
- GitHub first (App installation, scoped tokens, check runs), then Slack, then Linear.
- Trigger matcher with the full filter grammar, including webhook payload patterns with
  their bounds.
- Custom webhooks: three auth modes, signature schemes, immutability rules.
- Schedules: leader-elected cron with catch-up.
- Outbound: thread-per-work-item, progress editing, reply continuation.
- Unmatched-delivery visibility.

**Exit criterion met.** Label an issue `factory-ready` and a PR appears, with progress
in a Slack thread and a reply there continuing the work. The laptop record is
[`spike/m4` live exit](../spike/m4/README.md#live-exit).

Peter's personal Mac (OrbStack, VibeProxy on port 8317, a cloudflared quick tunnel),
`MODE=live ./spike/m4/run.sh`, reported `M4 LIVE PASS` at 2026-10-16 8:15 PM PT.
The run was local branch `polds/m4-slack-placeholder-check-eb47` at `02f2960`
([PR #79](https://github.com/polds/factory/pull/79)). Model `claude-sonnet-5-5`
via `M4_LIVE_MODEL`. This environment has no live credentials, so `MODE=live` is
not re-run here. `m4-live.yml` has no workflow runs.

[polds](https://github.com/polds) labeled
[issue #89](https://github.com/polds/factory/issues/89) `factory-ready`. Work item
`task_01M53XGMHEQYS587SZPZWBZHSV` went through triage, planning, building, and
reviewing, then `human_handoff`. The factory opened draft
[PR #90](https://github.com/polds/factory/pull/90) at `237f0a8`
(`M4: emphasize Hello`, 2026-10-17 03:12:14Z). Slack posted a thread root and
stage edits. Peter's thread reply started a revision that pushed `ca6e41c`
(`M4: add test for empty name in Hello`, 2026-10-17 03:15:05Z). The script saw
the head change, and its log secret scan was clean. The pasted record has no
delivery id, Slack permalink, reply timestamp, or continuation run id.

The first reviewer run, `run_01M53XJ9A6NZ62RF26AK9ZDNQK`, exited 10
(`container_exit`). The factory rebuilt and the second reviewer run passed.
That root cause is still open. An earlier attempt on
[issue #83](https://github.com/polds/factory/issues/83) opened draft
[PR #84](https://github.com/polds/factory/pull/84) and
[PR #85](https://github.com/polds/factory/pull/85).
[PR #91](https://github.com/polds/factory/pull/91) merged at `8c42691`
(`2026-10-17T04:26:44Z`, 9:26 PM PT on 2026-10-16) and keeps one building run
per work item. The same change
retries a transient GitHub token error that had shown up as `config_invalid`.
[PR #79](https://github.com/polds/factory/pull/79) merged at `138eb68`. Leftover
live-test issues and PRs #77–#90, except #79, were closed on 2026-10-16 with
Peter's approval. #90 is a closed draft.

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
