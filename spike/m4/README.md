# M4 — labeled intake, Slack thread, reply continuation

`spike/m4/run.sh` starts the laptop compose stack with `deploy/m4-factory`. Offline posts a signed `issues.labeled` delivery at the local fakes, checks dedup, an unmatched delivery, the check run, and a Slack thread. A signed Slack reply starts a second building run on the same conversation.

```bash
go build -o bin/factoryctl ./cmd/factoryctl
./bin/factoryctl validate deploy/m4-factory
MODE=offline ./spike/m4/run.sh
```

Offline is the default. It ends with `M4 OFFLINE PASS`. Live uses a real GitHub App, Slack, and a local model server:

```bash
MODE=live ./spike/m4/run.sh
```

That command needs the live credentials described below. CI and this environment do not have them, so it is not re-run here. The laptop result is the [live exit](#live-exit). `run.sh` reads `deploy/.env` when that file exists. A non-empty shell variable wins. Empty assignments are ignored and values are not executed. The script does not print secret values.

Scratch is `spike/m4/.work/`, which is gitignored.

## OrbStack

Peter's Mac runs OrbStack. `docker info --format '{{.OperatingSystem}}'` prints `OrbStack`.

macOS probes use `127.0.0.1`. Containers use `host.docker.internal`. That name resolves inside a container. On the Mac, the host process uses `127.0.0.1` for the same port. Task containers reach factoryd at `http://host.docker.internal:8080` (`FACTORY_TASK_SERVER_URL`). The model server from a container is `http://host.docker.internal:8317/v1` (`FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL`). The script's catalog probe dials `127.0.0.1` on that port and requests `/models`. That probe, `wait_http`, factoryd `/healthz`, the bridge-container health curls, and the live work-item polls use `--connect-timeout 2 -m 5`.

`M4_LIVE_MODEL` must be a model id taken from the server's `/models` list. The local proxy forwards an id it does not list, and that upstream response is HTTP 403. Offline stub ids (`m4-triage`, `m4-planning`, `m4-building`, `m4-reviewing`) stay in `deploy/m4-factory`. Live rewrites those four ids in the mounted copy from `M4_LIVE_MODEL`. Optional `M4_LIVE_MODEL_TRIAGE`, `M4_LIVE_MODEL_PLANNING`, `M4_LIVE_MODEL_BUILDING`, and `M4_LIVE_MODEL_REVIEWING` override one role. An empty override uses `M4_LIVE_MODEL`.

## GitHub App private key

Leave `FACTORY_GITHUB_APP_PRIVATE_KEY_FILE` empty in `deploy/.env`. The key file is `spike/m4/secrets/github-app.pem`. `spike/m4/.gitignore` ignores `secrets/`.

`run.sh` copies that file to `spike/m4/.work/live/github-app.pem` at mode 644 and mounts the copy into factoryd. The container path in `spike/m4/compose.yml` is the mount inside factoryd. It is not a value for `deploy/.env`. A `deploy/.env` line that sets `FACTORY_GITHUB_APP_PRIVATE_KEY_FILE` to `/run/secrets/github-app.pem` points the host script at a path that exists only inside the container. Leave the variable empty so the script keeps the default `spike/m4/secrets/github-app.pem`.

Offline generates its own key under `spike/m4/.work/` and overwrites `spike/m4/.work/github-app.pem`. Keep the live key under `secrets/`.

## Live variables

`gh auth login` as the human who will apply `factory-ready`. The script creates the issue with the App installation token and applies the label with that user. Intake drops a `[bot]` actor, so the label has to come from a `User`.

| Name | Required | Meaning |
| --- | --- | --- |
| `FACTORY_PUBLIC_URL` | yes | `https` origin of the tunnel, with no userinfo. The script prints the ingress paths under that origin. |
| `FACTORY_GITHUB_APP_ID` | yes | GitHub App id. The App is installed on `polds/factory` only. |
| `FACTORY_GITHUB_WEBHOOK_SECRET` | yes | App webhook secret. |
| `FACTORY_GITHUB_TOKEN_MODE` | no | Empty becomes `app`. |
| `FACTORY_GITHUB_APP_PRIVATE_KEY_FILE` | leave empty | Host path of the App private key. Empty uses `spike/m4/secrets/github-app.pem`. |
| `FACTORY_SLACK_BOT_TOKEN` | yes | Bot token. The script does not print it. |
| `FACTORY_SLACK_SIGNING_SECRET` | yes | Slack signing secret. |
| `FACTORY_SLACK_TEAM_ID` | yes | Slack team id (`T` plus uppercase letters and digits). |
| `M4_SLACK_CHANNEL` | yes | Channel id (`C` or `G` plus uppercase letters and digits). |
| `M4_SLACK_ALLOWED_USER` | yes | Slack user id allowed to continue the thread (`U` plus uppercase letters and digits). |
| `OPENAI_BASE_URL` | yes | Also `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL`. From a container: `http://host.docker.internal:8317/v1`. A loopback host is refused. |
| `M4_LIVE_MODEL` | yes | Model id from the server's `/models` list. |
| `M4_LIVE_MODEL_TRIAGE` | no | Replaces the triage id. Empty uses `M4_LIVE_MODEL`. |
| `M4_LIVE_MODEL_PLANNING` | no | Replaces the planning id. Empty uses `M4_LIVE_MODEL`. |
| `M4_LIVE_MODEL_BUILDING` | no | Replaces the building id. Empty uses `M4_LIVE_MODEL`. |
| `M4_LIVE_MODEL_REVIEWING` | no | Replaces the reviewing id. Empty uses `M4_LIVE_MODEL`. |
| `FACTORY_TASK_SERVER_URL` | on OrbStack | `http://host.docker.internal:8080`. |
| `OPENAI_API_KEY` | no | Also `FACTORY_INFERENCE_OPENAI_API_KEY`. Sent only when set. |
| `M4_REPO` | no | Default `polds/factory`. |
| `REPLY_TEXT` | no | With `SLACK_TEST_USER_TOKEN`, the script posts this reply as that user. |
| `SLACK_TEST_USER_TOKEN` | no | User token for the scripted reply. Absent: the script waits for a reply in the thread. |

After the draft PR opens, reply in the Slack thread. The script's suggested text is `also add a test for an empty name`.

## Live exit

Peter ran `MODE=live ./spike/m4/run.sh` on his personal Mac and reported `M4 LIVE PASS` at 2026-10-16 8:15 PM PT. The machine was OrbStack, with VibeProxy on port 8317 and a cloudflared quick tunnel. The checkout was local branch `polds/m4-slack-placeholder-check-eb47` at `02f2960` ([PR #79](https://github.com/polds/factory/pull/79)). `M4_LIVE_MODEL` was `claude-sonnet-5-5`.

[polds](https://github.com/polds) labeled [issue #89](https://github.com/polds/factory/issues/89) (`M4: emphasize Hello`) `factory-ready` at 2026-10-17 03:12:00Z. The issue was opened by `factory-dev-polds[bot]`. Work item `task_01M53XGMHEQYS587SZPZWBZHSV` went through triage, planning (spec), building (implementer), and reviewing (reviewer), then `human_handoff`. Draft [PR #90](https://github.com/polds/factory/pull/90) opened with `237f0a8` (`M4: emphasize Hello`) at 2026-10-17 03:12:14Z, authored by `factory-dev-polds[bot]`. Slack posted the thread root and stage edits. Peter's thread reply started a revision. That push is `ca6e41c` (`M4: add test for empty name in Hello`) at 2026-10-17 03:15:05Z. The script saw the head change. Its log secret scan was clean.

The pasted record does not include a delivery id, a Slack permalink, a reply timestamp, or a continuation run id. Those fields are omitted here rather than filled in.

Incidents:

- The first reviewer run, `run_01M53XJ9A6NZ62RF26AK9ZDNQK`, exited 10 (`container_exit`). The factory rebuilt and the second reviewer run passed. The root cause is not investigated yet.
- An earlier attempt on [issue #83](https://github.com/polds/factory/issues/83) produced two draft PRs, [#84](https://github.com/polds/factory/pull/84) and [#85](https://github.com/polds/factory/pull/85). [PR #91](https://github.com/polds/factory/pull/91) (`M4 T35a: one building run per work item`) merged at `8c42691` (`2026-10-17T04:26:44Z`, 9:26 PM PT on 2026-10-16).
- That earlier attempt also hit a GitHub token timeout, which showed up as `config_invalid`. PR #91 retries transient token errors.
- [PR #79](https://github.com/polds/factory/pull/79) (`spike/m4: fix live slack placeholder check`, including a failure when more than one draft PR matches) merged at `138eb68`.

Leftover live-test issues and PRs #77–#90, except #79, were closed on 2026-10-16 with Peter's approval. #89 and #90 are closed. #90 was not merged.

`m4-live.yml` has no workflow runs. The laptop run is the exit evidence. The same record is summarized in [`docs/12-build-sequence.md`](../../docs/12-build-sequence.md).

## Stopping a run

Ctrl-C or SIGTERM stops the processes and containers this invocation started. The grace period is `SPIKE_STOP_GRACE` (default 5 seconds), then SIGKILL. A process or container still running after that is a bug. Report `ps` and `docker ps`.
