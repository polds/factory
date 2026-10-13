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
3. `failure_reason` is null — i.e. the agent finished, rather than the infrastructure
   failing. Scoring an evicted pod teaches you nothing about agent quality and poisons
   every trend line. This is why
   [04](04-protocols-and-apis.md#messages) keeps `failure_reason` orthogonal to agent
   outcome.
4. It is not itself a measurement run (scorer, benchmark trial, or improvement run).

Eligible runs are sampled at `samplingRate` (default 25%, `0` disables). Sampling is
deterministic on `hash(run_id, scorer_id)` so a given run's inclusion is reproducible and
re-deriving history does not reshuffle the sample.

On-demand scoring is always available for a single run and **replaces** that scorer's
previous result — the intended workflow for tuning judge instructions before raising the
rate.

### Judge execution

A scorer runs as an ordinary run on `scorerDefaults.runner` with the scorer's `model`,
given a read-only context: the work item, the transcript, the diff, tool calls, and
(when `includeDescendants: true`) child-run evidence. It gets no repository write access
and no execution secrets — the judge's job is to read, and a judge that can act is a judge
that can hide its own failures.

Output is validated against the declared labels. A judge returning an undeclared label is
a scorer error, recorded as `scorer_invalid` rather than silently coerced to a failure.

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
([`benchmarks/`](03-definition-schema.md#benchmarkssuitesuiteyaml-and-taskstaskyaml)).

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

### Trials and comparison

`configurations × tasks × repetitions` trials, run at `batch` priority. Each trial is
scored by the Correctness Scorer against the task's `successCriteria`, plus any scorer
that applies to the agent, plus cost and duration.

Reported per configuration: pass rate with a confidence interval, median cost, median
duration, and token usage by model. **Repetitions are required for a verdict** — agent
runs are stochastic, and a single trial per cell cannot distinguish a real improvement from
noise. The console refuses to render a winner below a configurable minimum (default 5
repetitions) and says why.

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

Autonomy and cycle time require a connected code forge and only cover activity after it is
connected. The dashboard's purpose is to **pick which runs to investigate**, never to
conclude what caused a change — which is what benchmarks are for.

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

Output is always a pull request. Nothing is applied directly, including to the factory's
own definition — which is exactly why the definition lives in Git.

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

### Guard rails

A system that edits its own configuration needs limits that are structural, not advisory:

1. **No self-merge.** The improvement run has no merge permission. Merging is branch
   protection plus repository permissions — enforced by your forge, not by us.
2. **Definition PRs get the config check.** A proposed definition change is validated by
   the same pre-merge check as a human's ([03](03-definition-schema.md#validation)), so
   the loop cannot land an invalid tree.
3. **Measurement is excluded from measurement.** Improvement, scorer, and benchmark runs
   are never scorer-eligible, so the loop cannot feed on itself.
4. **One batch per agent in flight.** Prevents a stampede of overlapping proposals against
   the same cause.
5. **Scorers cannot enable themselves.** A PR that sets `selfImprovement: true` on a new
   scorer is a privilege escalation in this system and is flagged in review as such.
6. **Blast-radius limit.** An improvement PR touching more than a configurable number of
   definition files (default 3) is labelled `needs-human-design` and assigned to the admin
   pool rather than a random reviewer.
7. **Protect the gates.** A proposal that would weaken `pipeline.approvals` is rejected by
   the validator outright. The loop may not remove its own supervision — the single most
   important rule in this document, and the clearest argument for gates being declarative
   ([03](03-definition-schema.md#deviation-declarative-pipeline)) rather than sentences in
   a prompt the loop is free to rewrite.

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
