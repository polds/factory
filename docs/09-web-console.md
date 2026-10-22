# 09 — Web console

A React/TypeScript SPA served by `factoryd` at `/`, talking to the same public REST API
([04](04-protocols-and-apis.md)) that `factoryctl` uses. No private console API — if the
console can do it, a script can.

Live data arrives over SSE, resumable by sequence number, so a dropped connection resumes
exactly rather than re-fetching.

## Surfaces

```
┌────────────┬──────────────────────────────────────────────────────┐
│ Factories  │  Dashboard · Work items · Runs · Inbox               │
│  payments  │  Definition · Scorers · Benchmarks · Improvement     │
│  platform  │  Automations · Webhooks · Workers · Secrets          │
└────────────┴──────────────────────────────────────────────────────┘
```

### Dashboard
Activity, cost, autonomy, and evaluation results over a selected range
([08](08-measure-and-self-improvement.md#dashboard-metrics)). Total runs with breakdowns;
PRs opened/merged; autonomy; PR cycle time with per-stage medians; cost per PR by component
and by PR size (S/M/L/XL split at 100/500/1,000 changed lines); most expensive PRs; scorer
cards; newest improvement PRs.

Every metric that can mislead carries its caveat inline, not in a docs page nobody opens:
cost per PR is labelled a comparison metric; merged-vs-opened explains why merged can
exceed opened in a period; total runs notes that it includes measurement runs.

**M6 applied.** The same dashboard keeps the operational tiles (Waiting on
human, Active runs, Queue wait p50, PRs opened, Unmatched deliveries, runs by
status, runs by failure class) and adds the
[08](08-measure-and-self-improvement.md#dashboard-metrics) set beside them.
Each 08 caveat is on its metric. `forge_connected_at` is never filled, so
autonomy and cycle time show "forge not connected". The definitions are in
08; they are not repeated here.

### Work items
The kanban view: columns are pipeline stages (Triage → Planning → Building → Reviewing →
Handoff), cards are work items showing source, age, current agent, model, and blocking
gate. A card in `AWAITING_APPROVAL` or `AWAITING_INPUT` is visually distinct because
those are the only two states where the system is waiting on a **person**, and a factory
stalled on a human gate nobody noticed is the most common way this product disappoints.

### Runs
List with filters (agent, status, source, model, worker, cost). Detail view is the
heart of the console:

- **Live transcript** — turns, messages, tool calls with redacted input and result status,
  file changes, screenshots. Streaming via SSE with `from_seq` resume.
- **Timeline** — stage and event summary, time per phase, queue wait vs. execution.
- **Usage** — tokens and cost per model call, so a router's decisions are visible.
- **Tree** — parent/child runs for an orchestrated work item.
- **Diff** — the change produced, with the PR link.
- **Failure detail** — for infrastructure failures, the structured diagnostics from the
  wire: Job/Pod/Container conditions, normalized exit code, signal, and
  privacy-filtered events ([04](04-protocols-and-apis.md#messages)). This turns
  "the run failed" into "the pod was OOMKilled at 4Gi", which is the difference between a
  ticket and a fix.

**Attach and steer.** A live run can be interrupted
(`POST /agent/runs/{id}/interrupt` — stop the turn, keep the session) or given a
follow-up turn (`POST /agent/runs/{id}/followups` with `{text}`). Each returns `202`
and a `steer_id`, or `409` with a reason. The harness long-polls
`GET /agent/runs/{id}/steer` with the run token
([04](04-protocols-and-apis.md#native-mode-protocol-v2),
[05](05-harness-contract.md#output-ndjson-events)). It emits `run.capabilities` with
`"steer": true` once, before the first model call, when the opening `wait=0` probe
returned 200. `wait=0` returns the queued steers immediately. A missing or positive
`wait` long-polls, clamped to 25s. A 404 or 501 leaves the capability unannounced.
The console enables the
controls when `steer.enabled` is true and `idle_until`, if set, is still ahead.
Otherwise it shows `steer.reason` ("worker/harness does not support steering", "run
has not started", or "run finished; idle window ended at …").

**M6 applied.** Run detail also has Scores, Score now, and Capture as benchmark
task. Score now posts the on-demand score. Capture asks for a suite and a name,
shows the returned YAML, and offers copy. An identical rescore leaves the
Scores row Pending: the scores response has no id or `created_at`, and the
pending overlay clears only when the row for that scorer changes. That is a
current limit.

`idleTimeoutMinutes` (1–60) is the idle window while the run is still `running`.
Once the run is terminal, interrupt and follow-up return the idle-window 409.

### Inbox
Everything waiting on *you*: approval gates, agent questions, improvement PRs awaiting
review, and failed runs retained for inspection. One list, oldest first, with the action
inline — approve, answer, review.

The inbox is a second surface for the same gates that appear in Slack or Linear, never the
only one. A gate reachable from exactly one place is a gate that blocks work when that
place is not where the person is.

An agent question is a `question` event (`gate` `clarification`). The work item
becomes `awaiting_input` and the worker slot is released. The inbox answer is
`POST /work-items/{id}/input` with `{text}`, which records the answer and queues
the next execution of that run.

**M6 applied.** An open improvement pull request is inbox kind
`improvement_pr`. The row's action is Review PR, an https link to that pull
request. The route is still `GET /api/v1/factory/{uid}/inbox`
([04](04-protocols-and-apis.md#scoring-and-benchmarks-new-surface)).

### Definition
A file tree and a textarea. Validate is a button; diagnostics then mark the file and
line. The Schema link loads those documents from factoryd, which embeds them for
images that have no schemas directory.

- **Git-backed factories:** read-only, with links to the forge and a prominent indication
  that changes go through a PR.
- **Local/managed factories:** editable. Validate, then atomic apply.

A diff preview shows which resources a change will create, update, or delete before it is
applied — the same summary the pre-merge check posts.

### Scorers / Benchmarks / Improvement
- **Scorers** — definitions, pass rate over time, result list with judge reasoning linked
  to the scored run. Score-this-run-now for tuning.
- **Benchmarks** — suites, trial matrix (configurations × tasks), pass rate with confidence
  intervals, cost and duration medians. Refuses to declare a winner below the fixed
  minimum of 5 repetitions and says so. "Capture as benchmark task" on a run returns
  YAML to copy. It does not write a task file.
- **Improvement** — batches, their failing runs, the PRs opened, and the
  *Regressions addressed* provenance. `Run now` to force a check.

**M6 applied.** Routes are `/f/{factory}/scorers`, `/f/{factory}/scorers/{name}`,
`/f/{factory}/benchmarks`, `/f/{factory}/benchmarks/{suite}`, and
`/f/{factory}/improvement`. An empty benchmark winner renders the refusal
reason and highlights no cell. Capture's copy path is the run-detail panel
above. API shapes are in
[04](04-protocols-and-apis.md#scoring-and-benchmarks-new-surface).

### Automations / Webhooks
Automation list with enable/disable, last fired, match count, and runs started. **Unmatched
deliveries** is a first-class view: recent provider deliveries that matched no automation,
with the normalized event, so a mistyped filter is diagnosable in seconds instead of being
indistinguishable from an outage.

Webhook management shows the ingress URL, auth mode, delivery history with verification
results, and a redelivery button. `authMode`/`signatureScheme` render as immutable with
the delete-and-recreate path explained.

### Workers
Connected workers: pool, platform, backend, in-flight vs. capacity, heartbeat age, version,
and reconnect count. Queue depth and time-to-assignment per pool. This is where
`task_rejected` rate is surfaced, since sustained rejection means the pool is too small —
a capacity problem that otherwise presents as "everything is slow".

### Secrets / Identities
Names, scopes, provider, version, and last rotation — **never values**. Which agents are
allowed each secret, and which runs used it. Per-agent non-human identities with their
subject and last use ([11](11-security-model.md)).

## Design principles

1. **Public API only.** Anything the console does, `factoryctl` and a script can do.
2. **Resumable streams, not polling.** `from_seq` on every stream.
3. **Caveats inline.** A metric that can mislead carries its limitation next to the number.
4. **Distinguish agent failure from infrastructure failure everywhere.** Different icon,
   different colour, different filter. Conflating them is how teams come to believe their
   agents are unreliable when their cluster is full.
5. **Never render a secret.** Values are not fetchable through the API at all, so the
   console cannot leak what it cannot request.
6. **Show the waiting.** Queue wait is displayed beside execution time; a 40-second run
   that waited 11 minutes is a capacity story, not a performance one.

## Accessibility and operational notes

Keyboard-navigable lists and dialogs, visible focus, and no colour-only status encoding —
status is always icon + text, which also survives a screenshot pasted into a ticket. Dark
and light themes from the same token set. The transcript view virtualizes: a long agent run
produces tens of thousands of events, and rendering them all is how a console becomes
unusable precisely when something has gone wrong.
