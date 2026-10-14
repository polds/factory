# M2 — laptop control plane

`spike/m2/run.sh` starts the compose stack and proves the walking skeleton.

```bash
MODE=offline ./spike/m2/run.sh   # default; ends with "M2 OFFLINE PASS"
MODE=live    ./spike/m2/run.sh   # real model + GitHub; prints the PR URL
```

Offline needs Docker, Go, git, and curl. Live also needs `gh` and:

| Variable | Required | Meaning |
| --- | --- | --- |
| `OPENAI_BASE_URL` | yes | OpenAI-compatible endpoint reachable from a task container |
| `M2_MODEL` | yes | Model id written into the definition before factoryd boots |
| `GH_TOKEN` | yes | Fine-grained token that can push and open PRs |
| `OPENAI_API_KEY` | no | Sent only when set |
| `M2_REPO` | no | Default `polds/factory` |

Scratch (logs, the bare repo, watch transcripts, the event archive) is
`spike/m2/.work/`, which is gitignored.

## Laptop (`deploy/.env`)

`run.sh` does not read `deploy/.env`. To boot the stack and drive it yourself:

```bash
cp deploy/.env.example deploy/.env
# Live agent: set FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL,
# FACTORY_INFERENCE_OPENAI_API_KEY, and FACTORY_SECRET_GH_TOKEN.
# deploy/m2-factory ships with model m2-offline-stub; point agentDefaults.model
# at the model you want. Docker Desktop also needs
# FACTORY_TASK_SERVER_URL=http://host.docker.internal:8080

CGO_ENABLED=0 go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
docker build -t factory-harness:dev harness/generic-openai
docker build -t factory-m1-task:dev spike/m1/task-image
docker compose -f deploy/docker-compose.yml up -d --build
go build -o bin/factoryctl ./cmd/factoryctl
FACTORY_API_KEY=m2-dev-api-key ./bin/factoryctl run --prompt "…" --watch
```

Compose interpolates `deploy/.env` from the compose file's directory. Do not
commit that file. `up --build` builds `factoryd` and the upstream worker. It
does not build `factory-harness:dev` or `factory-m1-task:dev`; the worker
pull policy is `IfNotPresent` and expects those local tags.

`factoryd` sets `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`,
and `GIT_COMMITTER_EMAIL` on every task (`Agent` / `agent@example.com`) unless
an allowlisted secret already has that name. The recorded live run's first
`git commit` failed without them ([docs/12](../../docs/12-build-sequence.md)).

## What the stack is

`docker compose -p factory-m2 up -d --build --wait` from `deploy/` starts
factoryd, Postgres, MinIO, a bucket init, and one unmodified upstream worker
(`factory-upstream-worker:dev`). Images are built locally. MinIO is
`bitnamilegacy/minio:2025.7.23-debian-12-r5` with the image's default
entrypoint (no `server /data`). The root user is at least 3 characters.

`factoryd serve` migrates on boot. Compose `up --wait` treats an exited
container as failure, so `migrate up` is the `migrate` profile. The script
runs it before `up --wait`.

## Offline networking

The worker's Docker backend attaches task containers to the host bridge.
`127.0.0.1` inside the task is not the host, and this daemon has no
`host.docker.internal`. A listener on the bridge gateway is reachable.
On this Linux engine that gateway is `172.17.0.1`, which is also
`FACTORY_TASK_SERVER_URL`'s default (`http://172.17.0.1:8080`).

`run.sh` reads the gateway with `docker network inspect bridge` and:

- binds `git daemon` and the model stub to that address
- exports `FACTORY_TASK_SERVER_URL=http://<gateway>:8080`
- points `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL` at the stub

Docker Desktop does not publish the Linux bridge into task containers the
same way. Set `FACTORY_TASK_SERVER_URL=http://host.docker.internal:8080`
there.

A loopback `OPENAI_BASE_URL` in live mode is rewritten to the same gateway,
keeping the port. Any other host is left unchanged and must already be
reachable from the task container.

## What each mode checks

**Offline.** The M1 fixture is served with `git daemon`. The scripted model
clones it, adds the exclamation mark, runs `go test`, and pushes `m1-run`.
`factoryctl run --watch` must print a transcript line (`tool.call`,
`message.delta`, `tool.result`, or `usage`) at least one second before the
terminal line. The run status is `succeeded`, `m1-run` contains the edit,
and `mc stat` sees `runs/<run_id>/<execution_id>/events.ndjson` in the
`factory` bucket. A second run sleeps inside the task; `factoryctl cancel`
ends it `cancelled` (the unmodified worker may answer `task_cancellation`
itself, or factoryd's 60s grace will).

**Live.** The prompt matches the M1 live task, on branch `m2/run-<utc>`.
Success prints the PR URL from `gh pr list --head m2/run-<ts>`. The recorded
pass is [run 38512432518](https://github.com/polds/factory/actions/runs/38512432518),
which opened [PR #30](https://github.com/polds/factory/pull/30).

Both modes grep compose logs, the watch transcript, and the archived events
for every injected secret. A match fails the run and withholds those files.
Before the run, the script plants a dummy value in a scratch file and
requires the same grep to fail, then deletes the file.
