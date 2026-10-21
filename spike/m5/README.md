# M5 — console day

Offline drives a day of factory work through the console. The model router is `spike/m5/router.py`, in front of `spike/m1/model-stub`. Live and live-ci use the real model server and do not start that router.

Offline copies `deploy/m5-factory` and sets the implementer's `idleTimeoutMinutes` to 1. The file in `deploy/m5-factory` stays at 5, and live and live-ci use that copy unchanged. Review still starts only after the idle. `run.sh` prints one `m5 phase <name> <seconds>s` line each for image build, stack up, Playwright, and evidence.

`M5_ROUTER_PORT` sets the offline router port. When it is unset, `run.sh` picks a port that binds on both `0.0.0.0` and `127.0.0.1`. After the router starts, `GET /v1/models` on `127.0.0.1` and from a container via `host.docker.internal` must return the `m5-*` model list. Anything else stops with `router port <port> is answered by another process`.

```bash
./spike/m5/run_test.sh
```

Scratch is `spike/m5/.work/`, which is gitignored.

Playwright receives `M5_MODE` and `M5_REVIEWING_MODEL`. `run.sh` sets `M5_REVIEWING_MODEL` to `m5-reviewing` offline, and to `M5_LIVE_MODEL_REVIEWING` when that is set, otherwise `M5_LIVE_MODEL`, in live and live-ci. The Done card asserts `reviewer · ` plus that model id. Step 5 enables Interrupt during turn 1 within 15s, inside the steer long-poll, because the harness emits `run.capabilities` before the first model call. Offline still requires the Interrupt transcript row. Live and live-ci still require that row when the turn is in flight. If the turn had already finished (an Assistant row, or no turn in flight) when the interrupt was accepted, those modes accept HTTP 202 and the steer shown as delivered. A discarded interrupt is acknowledged on the next steer poll and stored with `delivered_at` set. Offline does not use that exception. Step 7 keeps the offline question text "Which staging env?" and the resumed transcript text "staging noted". Live and live-ci wait for the m5-question inbox row by its question kind and run link, then for a second execution: an Assistant row after the Terminal row "waiting for an answer", and a Run ended row whose status is succeeded. The posted answer is not a transcript row.

Live and live-ci use soft expects for report-only checks, so one run lists every independent mismatch instead of stopping at the first. That covers card text, model ids, transcript text, badge labels, filter results, the dashboard subtitle, and the axe summary. Navigation, the element the next step clicks, and state transitions stay hard expects in every mode: the Factory sign-in and Dashboard headings, the factory button, creating and approving a work item, the running row, the transcript being present before it is read, interrupt HTTP 202, the follow-up field, the idle container, page headings, the Done card, the failure rows that get opened, the Failure panel before Raw details, the schema link, Apply, the delivery Close control before normalized-event text, and the question row leaving the inbox. Offline keeps hard expects for every check, including the report-only ones. A soft failure still fails the Playwright test. `run.sh` then prints that failure list and `M5 FAIL`, and exits non-zero.

When `run.sh` is inside a git checkout and the mode is offline or live, preflight runs `git fetch origin` and, if `HEAD` is behind `origin/main`, prints a warning with how many commits behind and `git pull`. The warning does not fail the run. A fetch error or a fetch that is still going after 15 seconds is skipped. `MODE=live-ci` and GitHub Actions do not fetch.

`MODE=live-ci` copies optional `M1_OPENAI_EXTRA_HEADERS` onto `FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS`. `MODE=live` reads that variable from the environment (including `deploy/.env`). Unset or empty leaves each request as `Authorization: Bearer` plus the API key when the key is set, and omits `Authorization` when the key is empty. The value is newline-separated `Name: value` lines. A name that matches a default header replaces it, so an `Authorization` line replaces the API-key bearer header. LiteLLM placeholder: `x-litellm-api-key: Bearer sk-...`. The catalog probe and every model call send these headers. Values are not printed.

## Fast rerun

A full offline run builds images and starts compose. To leave that stack up and rerun only Playwright:

```bash
KEEP_UP=1 ./spike/m5/run.sh
./spike/m5/run.sh specs
```

`./spike/m5/run.sh --manual` records the same stack and prints the console URLs. It does not run Playwright.

`specs` keeps the compose project and the database. Offline restarts the model stubs and the router from the recorded commands so their scripts start fresh. It then runs Playwright and the evidence check. It does not run `go build`, `docker build`, or `compose up`. `HEADED=1 ./spike/m5/run.sh specs` uses the host Playwright path. Live and live-ci read inference settings again from the environment and `deploy/.env`. They are not stored in the stack record.

With no recorded stack, `specs` prints this and starts nothing:

```text
M5 FAIL — no KEEP_UP stack (run KEEP_UP=1 ./spike/m5/run.sh first)
```

A recorded pid that is gone prints `M5 FAIL — stale stack: <name> pid <pid> is gone`. A different `MODE` prints `M5 FAIL — MODE is <mode>, recorded stack is <recorded>`. Compose or `/healthz` failures each print one line.

```bash
./spike/m5/run.sh down
```

stops the recorded stack, runs `compose down -v --remove-orphans`, and deletes the record. A second `down`, or `down` with no record, prints `nothing to stop` and exits 0.

A default run that finds a live record stops before it builds anything:

```text
M5 FAIL — KEEP_UP stack is still up (./spike/m5/run.sh down or ./spike/m5/run.sh specs)
```

## Stopping a run

Ctrl-C or SIGTERM stops the processes and containers this invocation started. The grace period is `SPIKE_STOP_GRACE` (default 5 seconds), then SIGKILL. A process or container still running after that is a bug. Report `ps` and `docker ps`.
