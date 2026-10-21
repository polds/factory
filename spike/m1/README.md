# M1 — real agent run

One command drives the unmodified upstream `oz-agent-worker` through
`cmd/m0-spike` and the `generic-openai` harness. The agent clones a repo,
changes `greet.Hello`, runs `go test`, and publishes the result.

```bash
MODE=offline ./spike/m1/run.sh   # default
MODE=live    ./spike/m1/run.sh
```

Offline needs Docker, Go, git, and curl. Live also needs `gh` and:

| Variable | Required | Meaning |
| --- | --- | --- |
| `OPENAI_BASE_URL` | yes | OpenAI-compatible endpoint, no vendor SDK |
| `M1_MODEL` | yes | Model id written into the task assignment |
| `GH_TOKEN` | yes | Fine-grained token that can push and open PRs |
| `OPENAI_API_KEY` | no | Sent only when set |
| `M1_REPO` | no | Default `polds/factory` |

Scratch (binaries, logs, the bare repo, the event archive) is `spike/m1/.work/`, which is gitignored.

## What each mode publishes

**Offline** (the CI path). `spike/m1/fixture` is committed into a local bare
repo and served with `git daemon`. A scripted model (`spike/m1/model-stub`,
script `offline-script.json`) clones it, adds the exclamation mark, runs the
tests, and pushes branch `m1-run`. That branch is the stand-in for a pull
request. Success ends with:

```
M1 OFFLINE PASS — unmodified worker <sha> pushed m1-run via m0-spike.
```

Before that line the script has already required three things: the upstream
worker tree is clean, `m0-spike` exited 0 (`task_completed`), and
`m1-run:greet/greet.go` contains `name + "!"` (the test expects `Hello, M1!`).

**Live.** The target is this repo, `polds/factory`, not a separate fixture
repository. The prompt tells the agent to edit `spike/m1/fixture`, push
`m1/run-<utc timestamp>`, and open a draft PR with `gh`. Success prints the
PR URL and then:

```
M1 LIVE PASS — unmodified worker <sha> opened <url>.
```

`run.sh` checks that URL with
`gh pr list -R "$M1_REPO" --head m1/run-<ts> --json url`.

## Offline networking

The worker's Docker backend creates the task container with binds and resource
limits only. It does not set host networking, and it does not add
`host.docker.internal`. Measured on this Linux Docker bridge:

| From the task container | Result |
| --- | --- |
| `127.0.0.1` on the host | Not the host. Loopback is the container. |
| `host.docker.internal` | Does not resolve. This daemon has no host-gateway entry. |
| Docker bridge gateway (`172.17.0.1` here) when the service listens on that address | Reachable. Confirmed with an HTTP listener and with `git clone` / `git push` against `git daemon --listen=<gateway>`. |

So the task container **can** reach a git daemon and a model stub on the host,
as long as those processes bind the bridge gateway and not `127.0.0.1`.
`run.sh` discovers the gateway with `docker network inspect bridge` and binds
both there. A named network is unnecessary: the worker cannot attach its task
container to one, and the default bridge already reaches the gateway.

`offline-script.json` contains the placeholder `__M1_GIT_HOST__`. The script
substitutes the gateway into `spike/m1/.work/offline-script.json` before
starting the stub.

## Secrets

Live writes `spike/m1/.work/m1.env` mode `0600` with `OPENAI_BASE_URL`,
`OPENAI_API_KEY` (when set), `GH_TOKEN`, `FACTORY_MAX_TURNS=40`, and a
single-line `FACTORY_TASK_PROMPT`.

`FACTORY_SECRET_VALUES` is the token values separated by a real newline. The
dotenv loader in `cmd/m0-spike` is one `KEY=VALUE` per line, so a newline
cannot live in that file. The script passes it with `-env`, which overrides
the file, and the harness splits that value on newlines.

Tool subprocesses still receive `GH_TOKEN` (they have to push). They do not
receive `OPENAI_API_KEY` or `FACTORY_SECRET_VALUES`. After the run, `run.sh`
greps the worker log, the server log, the offline helper logs, and
`spike/m1/.work/evidence/events.ndjson` for every token value. A match is a
failure, the logs are not printed, and the evidence directory is removed so
the live workflow does not upload it.

Offline uses a dummy `OPENAI_API_KEY` (`m1-offline-dummy-7f3c9a`) so that
scan has something to look for. The scripted commands never include it.

## Evidence to keep

- The clean-tree line: `built worker from <sha> — clean tree, no local modifications`.
- The `m0-spike` verdict block in the server log (`task_completed`).
- Harness lines (`factory-harness:`) from the worker log.
- `spike/m1/.work/evidence/events.ndjson`, the harness event log.
- Offline: `git --git-dir=spike/m1/.work/git/remote.git show m1-run:greet/greet.go`.
- Live: the printed PR URL.

The harness writes that NDJSON at `/var/run/factory/events.ndjson` inside the
task container. The worker config sets `cleanup: true`, which removes the
container when the task ends, so `run.sh` bind-mounts
`spike/m1/.work/runtime` at `/var/run/factory` and copies the file to
`spike/m1/.work/evidence/events.ndjson` afterwards. Both modes do this. The
worker log still keeps the `factory-harness:` and `[harness]` stderr lines;
those lines are the human-readable mirror, and the archive is the NDJSON
file from the mount.

`.github/workflows/m1-live.yml` uploads `spike/m1/.work/evidence/`
(`actions/upload-artifact` v7.0.2, `if: always()`, 30-day retention). The
path sits under the hidden `.work` directory, so the step sets
`include-hidden-files: true`.

The script exits non-zero if the worker tree is dirty, if `m0-spike` does not
report `task_completed`, if the offline branch was not pushed, if the live PR
URL is empty, if `events.ndjson` was not archived, or if a secret value
appears in the logs or in that file.

## Stopping a run

Ctrl-C or SIGTERM stops the processes and containers this invocation started.
The grace period is `SPIKE_STOP_GRACE` (default 5 seconds), then SIGKILL. A
process or container still running after that is a bug. Report `ps` and
`docker ps`.
