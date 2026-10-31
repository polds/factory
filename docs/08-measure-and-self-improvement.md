# 08 — Measure and self-improvement

A factory that cannot measure itself cannot be improved, and a factory that improves
itself without a gate cannot be trusted. This document specifies both halves: the
measurement substrate (scorers, benchmarks, metrics) and the closed loop that turns
measured failures into reviewable pull requests.

```
        ┌──────────── runs ────────────┐
        │                              │
        ▼                              │
   ┌─────────┐  sampled   ┌──────────┐ │
   │ scorers │───────────▶│ results  │ │
   └─────────┘            └────┬─────┘ │
                               │       │
             threshold reached │       │
                               ▼       │
                       ┌──────────────┐│
                       │ improvement  ││
                       │   batch      ││
                       └──────┬───────┘│
                              │        │
                   improvement run     │
                              │        │
                              ▼        │
                     ┌─────────────────┴──┐
                     │ PR: code · skills · │
                     │ definition          │──▶ human review ──▶ merge
                     └────────────────────┘                        │
                              ▲                                    │
                              └──── benchmark verifies ◀───────────┘
```

## Scorers

A scorer is an LLM judge that **classifies** a completed run against a rubric. It assigns
a label, not a numeric grade — because a label points back at a cause and a number does
not.

Definition: [`scorers/<name>/scorer.md`](03-definition-schema.md#scorersnamescorermd).

### Eligibility and sampling

A run is scorer-eligible only if **all** hold:

1. Its agent is listed in the scorer's `agents`.
2. It reached a terminal state.
3. Its failure class is eligible. Cancelled runs are never scored. Class `agent`
   is eligible. An empty class is eligible when the run succeeded. Status `failed`
   with an empty class is not. Infrastructure and configuration classes are not.
   The earlier reading, `failure_reason` is null, is not what this build does:
   a reason stored on an agent-reported failure would hide the runs a scorer
   should see. Scoring an evicted pod still teaches you nothing about agent
   quality.
   [04](04-protocols-and-apis.md#messages) keeps `failure_reason` orthogonal to
   agent outcome.
4. Its purpose is `work`. A scorer, benchmark trial, or improvement run is not
   eligible. `runs.purpose` cannot change after insert, so a run cannot be
   retagged into or out of measurement.

Eligible runs are sampled at `samplingRate` (default 25%, `0` disables). Sampling is
deterministic so a given run's inclusion is reproducible and re-deriving history does
not reshuffle the sample.

On-demand scoring is always available for a single run and **replaces** that scorer's
previous result — the intended workflow for tuning judge instructions before raising the
rate.

**M6 applied.** `scorer.Eligible` applies the four rules above, and it checks
cancelled status before the class. The sample bucket is
`sha256(run_id ‖ 0x00 ‖ scorer name) mod 100`. Rate 0 samples nothing. Rate 100
or more samples every eligible run. The sampler leaves an existing `ok` or
`scorer_invalid` row alone. On-demand scoring replaces the row. The store allows
one row per run and scorer (`UNIQUE (run_id, scorer_name)`). The judge's
context is in the prompt only. There is no checkout. A scorer run is given
no factory secrets and no agent secrets. The prompt says only the inference
credential is available. Its final message is one JSON object
`{label, reasoning}`. An
undeclared label or no JSON object is `scorer_invalid`. The label is not coerced
to a failure.

### Judge execution

A scorer runs as an ordinary run on `scorerDefaults.runner` with the scorer's `model`,
given a read-only context: the work item, the transcript, the diff, tool calls, and
(when `includeDescendants: true`) child-run evidence. It gets no repository write access
and no execution secrets — the judge's job is to read, and a judge that can act is a judge
that can hide its own failures.

Output is validated against the declared labels. A judge returning an undeclared label is
a scorer error, recorded as `scorer_invalid` rather than silently coerced to a failure.

**M6 applied.** That validation is `scorer.ParseVerdict`. Score and pass/fail
come from the declared labels and `passingScore`, not from the model's JSON.

### What makes a good scorer

Enforced by the validator:

- At least one label at or above `passingScore`, at least one below. A scorer that cannot
  fail measures nothing.
- 1–20 labels, each with a unique `value` and a `score` in [0,1].

Not enforceable, so documented: one scorer answers **one** question whose failures point
at a single cause. "Was this a good PR?" is unactionable. "Did the agent run the tests
before opening the PR?" produces a fix.

## Benchmarks

Scorers tell you what happened in production. Benchmarks tell you what *would* happen
under a different configuration, on fixed tasks.

A suite pins one agent and 1–6 configurations, each a set of (agent → model | harness)
bindings, over an ordered list of tasks
([`benchmarks/`](03-definition-schema.md#benchmarkssuitesuiteyaml-and-benchmarkssuitetaskstaskyaml)).

### Replay is the mechanism

Benchmarks are not a separate execution path. A trial is a normal run with its starting
state **frozen**, using the observed wire field (O7):

```jsonc
"repository_head_overrides": [{
  "code_forge": "GITHUB", "repo_owner": "acme", "repo_name": "payments-service",
  "head": { "type": "COMMIT_SHA", "value": "0123…4567" },
  "clone_from": { "code_forge": "GITHUB", "owner": "acme-bench", "repo": "payments-service" },
  "preserve_origin": true
}]
```

Three properties this buys:

- **Full 40-char SHAs only.** A benchmark whose starting state can move is not a benchmark.
- **`clone_from` + `preserve_origin`** clone a scratch fork while keeping the original
  repository's identity, so the agent behaves as if working in the real repo but cannot
  touch it. This is what makes benchmarking safe against production repositories, via
  `benchmarkRepoSubstitutions`.
- **`sourceRunId`** records that a task came from a real historical run. The highest-value
  benchmark tasks are replays of production failures, so capturing a failing run as a task
  should be one click in the console.

**M6 applied.** factoryd fills `repository_head_overrides` on the assignment.
The in-repo harness does not apply `--repository-head-override-json`, so the
pinned SHA is not checked out by this harness. The flag and the skill path are
in [05](05-harness-contract.md#invocation). Capture returns YAML and writes
nothing ([09](09-web-console.md#scorers--benchmarks--improvement)).

### Trials and comparison

`configurations × tasks × repetitions` trials, run at `batch` priority. Each trial is
scored by the Correctness Scorer against the task's `successCriteria`, plus any scorer
that applies to the agent, plus cost and duration.

Reported per configuration: pass rate with a confidence interval, median cost, median
duration, and token usage by model. **Repetitions are required for a verdict** — agent
runs are stochastic, and a single trial per cell cannot distinguish a real improvement from
noise. The console refuses to render a winner below 5 repetitions and says why.
There is no setting for that floor.

**M6 applied.** Each trial is judged by the built-in `correctness` scorer
against the task's `successCriteria`, unless `scorers/correctness/scorer.md`
replaces it ([03](03-definition-schema.md#scorersnamescorermd)). An errored
trial is not a failed trial: infrastructure, configuration, and cancelled runs
are `errored` and are left out of `n`. Pass and fail come from a `correctness`
verdict of `ok`. `benchmark.Compare` always uses a minimum of 5
(`DefaultMinRepetitions`). A request may ask for a different repetition count
of at least 1; the winner floor stays 5. Any cell with `n` below 5 refuses,
and the reason names that configuration, task, and `n`. Otherwise a candidate
wins only when its Wilson 95% lower bound is strictly above the baseline's
upper bound. The baseline is the configuration named `Baseline` when one is
present, otherwise the first configuration. One errored trial that drops a
cell below 5 makes the report refuse.

## Dashboard metrics

| Metric | Definition | Trap it avoids |
| --- | --- | --- |
| **Total runs** | All runs, broken down by agent, status, source, model | Includes measurement runs — a rising run count with flat PRs may just be scoring |
| **PRs opened** | Counted once, in the period first observed | — |
| **PRs merged** | Of PRs opened in a period, how many merged later | Opened and merged come from different sources, so merged can exceed opened for a period |
| **Autonomy** | Share of merged PRs with no human code push. Opening the PR counts as a push | A human-authored PR later revised by an agent is not autonomous |
| **PR cycle time** | Median kickoff → PR → first review → merge, with per-stage medians | — |
| **Cost per PR** | Median cost of PRs opened in range; a run's cost includes children, split across its PRs | **Comparison metric, not a billing figure** |
| **Scorer cards** | Pass rate per scorer over time | — |
| **Improvement PRs** | Newest improvement PRs, regardless of date filter | — |

The dashboard's purpose is to **pick which runs to investigate**, never to
conclude what caused a change — which is what benchmarks are for.

**M6 applied.** Counts use runs created in `[from, to)`, including measurement
runs. The measurement figure is the scorer, benchmark, and improvement count
inside those buckets. A run with no work item counts under an empty source
name. PRs opened and PRs merged are separate counts, so merged can exceed
opened. Autonomy is the share of PRs merged in the window whose
`human_push_seen` is false. `n` is that merged count. A human actor is a
non-empty handle that does not contain `[bot]`. Opening a pull request stores
`human_push_seen` from that actor, so a human opener counts as a human push.
A later human push sets the same flag. An event that matches an improvement
batch (pull number, URL, or branch `improve/<batch id>`) is marked
`is_improvement`. Cycle time uses PRs merged in the window. Kickoff is the
work item's `created_at`. A stage median skips a PR that lacks either end.
Cost per PR is the median over PRs opened in the window. A run's cost includes
descendant runs that have no work item of their own, split by integer division
across the work item's PRs, and remainder cents are dropped. Size buckets are
S/M/L/XL at 100, 500, and 1,000 changed lines. Scorer cards are the `ok`-result
pass rate per UTC day. Improvement PRs are batches that have a URL, newest
first, with no date filter. `forge_connected_at` is never set: installation
rows have no connected-at timestamp, and the schema does not add one. The
console therefore shows the forge as not connected. The design sentence that
autonomy and cycle time start only after a connection timestamp is not what
this build computes.

## The self-improvement loop

### Trigger

A scheduled checker evaluates, per agent:

- `count(unreviewed failures) >= selfImprovement.failedRunThreshold` (default **25**,
  range 1–50), **or**
- `age(oldest unreviewed failure) >= staleFailureDays` (default **7**).

A failure is a scorer result below `passingScore` **from a scorer with
`selfImprovement: true`**. Opting in per scorer is deliberate: a scorer you are still
tuning must not be able to open PRs.

`POST /factory/{uid}/improvement:run-now` forces a check immediately, which may batch an
agent with a single unreviewed failure. The operator cannot choose which failures are
processed — batching is by agent, so the loop sees a *pattern* rather than being pointed at
one example it can special-case.

**M6 applied.** A threshold or stale-day value of 0 or less uses 25 and 7.
When `failedRunThreshold` is set in the tree, the loader requires 1–50. The
scheduled pass skips an agent that already has a batch in `queued`, `running`,
or `pr_open`. Run-now sets the threshold to 1 and does not skip those agents;
creating a second batch returns 409. Only scorers with `selfImprovement: true`
feed the list, and only from runs whose purpose is `work`.

### Improvement run

Failures for one agent form an `improvement_batch` dispatched to an improvement run. Its
input: each failing run's work item, transcript, diff, scorer label and reasoning — and the
current definition tree. Its task: find the **common cause** and propose the smallest change
that addresses it.

The proposal may target any of three layers, and choosing correctly is the whole skill:

| Failure pattern | Right layer |
| --- | --- |
| Agent didn't know a repo convention | a **skill** |
| Agent lacked a tool or repo access | **definition** (`mcpServers`, `secrets`, `required_repos`) |
| Agent's role was ambiguous | **agent instructions** |
| Model too weak for the stage | **definition** (`model`/`harness`/router) |
| The code is genuinely broken | **application code** |

Output is a pull request when the factory has a definition git URL. Nothing is
applied directly, including to the factory's own definition — which is exactly
why the definition lives in Git.

**M6 applied.** The prompt is the bound agent's instructions, built with
the same `handoff.BuildPrompt` a stage run uses, then the failure list.
Each failure line includes the run id, scorer, label, time, and reasoning.
It quotes each named scorer's rubric and up to three failing examples.
An example is that result's scorer reasoning and the tail of the run's
assistant output. Rubric text, reasoning, and the output tail are
size-capped. The whole prompt is redacted. The prompt requires the edit
to state the missing behaviour as an explicit imperative instruction in
the skill or instruction file, and to remove or rewrite any instruction
in the edited files that contradicts it. A bare appended marker line is
not enough. It adds a Workspace section, a file listing of the definition
tree, the layer table above, and prior rejection notes from closed batches
for the same agent. The Workspace section says `/workspace` starts empty,
names the `DEFINITION_GIT_URL` environment variable and not the URL value,
names the base ref, names the `improve/<batch>` branch to push, and says
`GH_TOKEN` is available for HTTPS push. It does not include the work item
or the diff. When the improvement run does not
succeed, the batch is marked `failed` once and the improve branch is not
cloned. When the run succeeds and the improve branch or the production
base ref is not advertised yet, factoryd records a 45 second deadline and
returns. It does not sleep on the worker read loop or under the settle
lock. The improvement leader retries that clone on its 5 second tick,
inside the deadline, and marks the batch `failed` once if the ref is still
missing. A factoryd restart starts a new deadline. Any other clone error,
and a pull-request create error other than HTTP 422, is retried up to 3
times and then the batch is marked `failed` once with the redacted error,
so a later batch for the agent can be scheduled. A factory with an empty `FACTORY_DEFINITION_GIT_URL` skips
self-improvement: the pass and run-now log that the factory has no definition
git URL and return no batches. They do not open a pull request. Benchmark
resolution of an empty git URL returns an empty SHA and uses the published
tree. The recorded layer is `skill` only when every changed path has a
`skills` segment, including `agents/<name>/skills/...`. Any other path, or an
empty diff, stays `definition`.

### Review

Each improvement PR carries a **Regressions addressed** section linking every failing run
and scorer result behind it, so a reviewer can trace the proposed change back to its
evidence instead of taking the diff on faith.

One reviewer is requested from the pool set by `reviewerType`:
`admins` (default), `team`, `custom` (from `reviewerEmails`), or `none`. Random selection
among eligible members spreads the load; `none` means the PR opens unassigned.

On merge, the batch's failures are marked reviewed and stop counting toward the threshold.
On close without merge, they are also marked reviewed — otherwise the loop would re-propose
a rejected change forever. The rejection is recorded so the next improvement run can see
that this cause was already proposed and declined.

**M6 applied.** The body is `## Regressions addressed`, one line per failing
run and scorer result, then `Layer changed:`, then the loop footer. Pools are
GitHub logins from `FACTORY_REVIEWERS_ADMINS`, `FACTORY_REVIEWERS_TEAM`, and
`FACTORY_REVIEWER_LOGINS` (`email=login` pairs for `custom`). `none` leaves
the pull request unassigned. `improve.Route` requests one login. The review
request is capped at 3. Rails 1 and 6 are in
[Guard rails](#guard-rails). A merged pull request marks the batch `merged`.
A close without merge marks it `closed` and stores the rejection note (or
`closed without merge` when the event has no text). When the improver opens
the pull request first, factoryd's create is a 422. `lookupOpenPR` finds
that request and factoryd PATCHes the body to the *Regressions addressed*
text. A patch error is logged and the batch still moves to `pr_open`, so a
rejected patch is not retried forever. The offline forge accepts a patch
only when `state` is `closed`, so that path keeps the opener's body.

### Guard rails

A system that edits its own configuration needs limits that are structural, not advisory:

1. **No self-merge.** The improvement run has no merge permission. Merging is branch
   protection plus repository permissions — enforced by your forge, not by us.
2. **Definition PRs get the config check.** factoryd validates the base and head
   trees, including `definition.GuardRails`, before it opens the pull request
   ([03](03-definition-schema.md#validation)). It then posts a `factory/config`
   check run on that head. This is not the Actions job of the same name in
   this repository.
3. **Measurement is excluded from measurement.** Improvement, scorer, and benchmark runs
   are never scorer-eligible, so the loop cannot feed on itself.
4. **One batch per agent in flight.** Prevents a stampede of overlapping proposals against
   the same cause.
5. **Scorers cannot enable themselves.** A PR that sets `selfImprovement: true` on a new
   scorer is a privilege escalation in this system and is flagged in review as such.
6. **Blast-radius limit.** An improvement PR touching more than 3 definition
   files is labelled `needs-human-design` and assigned to the admin pool rather
   than a random reviewer. There is no setting.
7. **Protect the gates.** A proposal that would weaken `pipeline.approvals` is rejected by
   the validator outright. The loop may not remove its own supervision — the single most
   important rule in this document, and the clearest argument for gates being declarative
   ([03](03-definition-schema.md#deviation-declarative-pipeline)) rather than sentences in
   a prompt the loop is free to rewrite.

**M6 applied.** One line per rail, as enforced:

1. No self-merge. The improvement-run token is `contents: write`,
   `pull_requests: write`, and `metadata: read`. The token factoryd uses for
   its own pull-request calls is `pull_requests: write`, `issues: write`,
   `checks: write`, and `metadata: read`. Neither map has a merge permission,
   and factoryd has no merge call
([`internal/server/m6_tokens.go`](../internal/server/m6_tokens.go)).
2. Before the pull request, `guardFindings` loads both trees and runs
   `definition.GuardRails`. Errors fail the batch and no pull request is
   opened. After the pull request exists, factoryd posts a check run named
   `factory/config` on the head
([`internal/server/m6_improve.go`](../internal/server/m6_improve.go)).
3. Measurement runs have purpose `scorer`, `benchmark`, or `improvement`.
   `scorer.Eligible` requires purpose `work`. `runs.purpose` is immutable
   after insert
([`internal/store/migrations/0010_m6.sql`](../internal/store/migrations/0010_m6.sql)).
4. Partial unique index `improvement_batches_one_inflight` on
   `(factory_id, agent_name)` where status is `queued`, `running`, or
   `pr_open`. `CreateBatch` maps a violation to `ErrBatchInFlight`.
5. A scorer that is new with `selfImprovement: true`, or flipped from false
   to true, is `SeverityWarning` from `definition.GuardRails`. The pull
   request is still opened. It gains the label `privilege-escalation` and a
   body line. It is not a rejection.
6. `improve.DefaultBlastLimit` is 3. More changed definition files than that
   forces the admins pool and the label `needs-human-design` (`improve.Route`).
7. `definition.WeakensApprovals` is `SeverityError`. Those errors fail the
   batch and no pull request is opened.

### Running the loop well

The practical sequence, which is also the order of the console's Measure tab:

1. **Define one scorer** for one observable failure mode. Score a few runs by hand and
   compare the judge's verdict to your own before trusting it.
2. **Collect a baseline.** Let automatic scoring run until results reflect normal work.
   Record settings, date range, and cost.
3. **Inspect failures.** Read the judge's reasoning and the underlying runs. Look for
   missing context, unclear instructions, or missing tools. Turn on `selfImprovement` only
   once the same cause repeats.
4. **Benchmark the candidate** against the baseline on fixed tasks, with enough repetitions
   to trust the difference.
5. **Review and adopt.** Hold improvement PRs to the standard you hold human ones.
6. **Keep monitoring.** Leave the scorer active and compare against the baseline. Revise
   it, or set `samplingRate: 0`, when its question stops mattering.

Change one measurable thing at a time. A factory with six simultaneous configuration
changes and a moving pass rate has learned nothing.
