# M6 — measure and improve

Offline plays the agent, the judge, and the improver with the model stub.
`spike/m6/e2e/loop.spec.ts` (`m6 exit loop`) signs in, opens factory `m6`,
creates the seeded work items, waits for scorer results, opens the improvement
pull request from the inbox, checks the batch and the skill diff, and runs the
`convention` benchmark. Live and live-ci use that same spec with structural
assertions and a real model. They also push a run-scoped base, watch this
run's improvement pull request, and delete only this run's remote state. The
CI (`live-ci`) exit is recorded in
[docs/12](../../docs/12-build-sequence.md). No laptop `MODE=live` run is
claimed here.

`MODE` is `offline`, `live`, or `live-ci`. Offline needs none of the live
variables. A missing live variable prints `missing <NAME>` and exits 1 before
any container starts.

Live checks, in order: `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL`,
`M6_LIVE_MODEL`, `M6_DEFINITION_GIT_URL`, `M6_GITHUB_API_BASE`,
`FACTORY_GITHUB_APP_ID`, `FACTORY_GITHUB_APP_PRIVATE_KEY_FILE`.

Live-ci checks, in order: `M1_OPENAI_BASE_URL`, `M1_OPENAI_API_KEY`,
`M6_LIVE_MODEL`, `M6_DEFINITION_GIT_URL`, `M6_GITHUB_API_BASE`,
`FACTORY_GITHUB_APP_ID`, `FACTORY_GITHUB_APP_PRIVATE_KEY_FILE`.

[`m6-live.yml`](../../.github/workflows/m6-live.yml) is `workflow_dispatch`
only. Its header names the repository secrets `M1_OPENAI_BASE_URL`,
`M1_OPENAI_API_KEY`, `M1_OPENAI_EXTRA_HEADERS`, `M6_GITHUB_APP_ID`, and
`M6_GITHUB_APP_PRIVATE_KEY`, and the inputs `model`, `judge_model`,
`definition_git_url`, `github_api_base`, `reviewer`, and `repetitions`.
The CI dispatch is the live-ci exit in
[docs/12](../../docs/12-build-sequence.md). With those variables unset, each
live mode stops at its first missing name. That is as far as the commands
below were run.

```bash
bash spike/m6/run_test.sh
```

CI's `m6-offline` job runs that script, then `MODE=offline ./spike/m6/run.sh`.
Scratch is `spike/m6/.work/`, which is gitignored. The API key is written to
`spike/m6/.work/api-key` (mode 600) on every run. A default run does not
print the path. `KEEP_UP=1` and `--manual` each print `api key file:` and
the absolute `$WORK/api-key`. The value is not printed.
`M6_IMPROVER_OPENS_PR=1` is the live-only branch: the improver opens the
pull request first, and factoryd's create is then a 422 followed by a
lookup. Router ports 8317 and 8318 stay reserved for a host proxy. The
driver refuses them.

```bash
MODE=offline ./spike/m6/run.sh
```

## Fast rerun

`KEEP_UP=1 ./spike/m6/run.sh` (or `--manual`) leaves the stack up and records
it in `spike/m6/.work/stack.env`. `./spike/m6/run.sh specs` reruns Playwright
and the evidence check on that stack. It passes twice on one stack.
`./spike/m6/run.sh down` tears the stack down. A default run that finds a
live `stack.env` stops and names `down` and `specs`.

```bash
KEEP_UP=1 MODE=offline ./spike/m6/run.sh
./spike/m6/run.sh specs
./spike/m6/run.sh down
./spike/m6/run.sh down
```

`HEADED=1 ./spike/m6/run.sh specs` uses the host Playwright path. `--manual`
records the same stack and does not run Playwright.

A second `down`, or `down` with nothing recorded, prints `nothing to stop`
and exits 0.

## Stopping a run

INT and TERM use the shared trap. Offline teardown is not on a timer. Live
and live-ci stop the candidate watch and any in-flight remote seed, then
clean this run's remote state inside the cancel budget, then stop containers
and processes. The process exits 130 on INT and 143 on TERM. `KEEP_UP=1`
leaves the stack and prints the `specs` and `down` commands.

`M6_KEEP_REMOTE=1` leaves this run's remote refs and pull requests in place.
`./spike/m6/run.sh --cleanup-remote` repeats remote cleanup from the saved
ledger. It is for live and live-ci. If that cleanup does not finish, the
driver prints `remote cleanup incomplete; ledger …; run ./spike/m6/run.sh --cleanup-remote`.
The ledger argument is the absolute `$WORK/remote-ledger.json`.

Task containers labelled `oz-task-id` are outside this trap.

## Cancel on Actions

`m6-live` execs `./spike/m6/run.sh` so the runner's SIGINT reaches the driver.
If the job is cancelled, an `if: cancelled()` step runs
`MODE=live-ci ./spike/m6/run.sh --cleanup-remote` with the same env
([`m6-live.yml`](../../.github/workflows/m6-live.yml), the step "Clean up
remote state after cancel"). The driver `exec` is the comment at line 7 and
`MODE=live-ci exec ./spike/m6/run.sh` at line 177. `if: cancelled()` is line
179. The local SIGINT/SIGTERM cancel-window test is `case_cancel_window` in
`spike/m6/t76b_cases.sh` (line 451), run by `spike/m6/run_test.sh` in CI.
Normal-exit cleanup is
[run 41243409323](https://github.com/polds/factory/actions/runs/41243409323)
(ledger all `cleaned`). Actions cancel is
[run 41244172690](https://github.com/polds/factory/actions/runs/41244172690):
cancelled at 14:37:47 PT after
[factory-m6-definition#5](https://github.com/polds/factory-m6-definition/pull/5)
opened; the driver's SIGINT trap closed that pull request 3 s later; the
`if: cancelled()` step succeeded; all 4 ledger entries are `cleaned`; no
leftover refs or pull requests. At `fd046e8`, `m1-live`, `m2-live`,
`m3-live`, `m4-live`, and `m5-live` exec their drivers, so an Actions cancel
reaches the trap: `m1-live.yml` line 56 (`MODE=live exec ./spike/m1/run.sh`),
`m2-live.yml` line 61 (`MODE=live exec ./spike/m2/run.sh`), `m3-live.yml`
line 85 (`MODE=live exec ./spike/m3/run.sh`), `m4-live.yml` line 87
(`MODE=live-ci exec ./spike/m4/run.sh`), and `m5-live.yml` line 66
(`MODE=live-ci exec ./spike/m5/run.sh`). T77 (`953c887`) added that `exec`.
None of those five has an `if: cancelled()` cleanup step.
