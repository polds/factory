# M0 — protocol spike

Proves the assumption the whole design rests on: that the unmodified, MIT-licensed
`oz-agent-worker` can be pointed at a control plane we run
([`docs/01` · O2](../../docs/01-evidence-and-provenance.md#o2--the-workers-server-endpoint-is-configurable)).

```bash
./run.sh          # ~40s warm, a few minutes cold (builds the upstream worker)
FAST=1 ./run.sh   # skip the 35s heartbeat verification
```

Exits 0 only if a real worker authenticated, claimed our task, launched our harness
sidecar inside a task container, and reported `task_completed`.

## Result

```
── M0 protocol spike ──────────────────────────────────────────
  PASS  worker authenticated         m0-spike
  PASS  task assignment accepted     task_assignment written to socket
  PASS  task claimed                 m0-spike
  PASS  heartbeat answered           1 ping/pong exchange(s)
  PASS  terminal state               task_completed: Task completed successfully
──────────────────────────────────────────────────────────────
VERDICT: confirmed — an unmodified worker ran our harness against our server.
```

Upstream worker `63dbb8a`, clean tree (`git status --porcelain` empty — the script prints
this, because "unmodified" is the entire claim). Task image `alpine:3.20`, harness sidecar
built from [`harness/`](harness), Docker backend on `linux/arm64`.

Captured argv inside the task container:

```
agent run --task-id run_m0_20261013T032059 --sandboxed
  --server-root-url http://127.0.0.1:8080 --model m0-stub-model
  --computer-use --idle-on-complete ''
```

## What it corrected

The spike existed to be cheap and to fail early. It did, three times — each one a claim
this design asserted and would have built on:

| Design said | Reality | Consequence if unfixed |
| --- | --- | --- |
| flag is `--websocket-url` | **`--web-socket-url`** — kong kebab-cases the `WebSocketURL` field | Worker exits immediately on an unknown flag. Nothing connects. |
| `heartbeat` is a worker→server JSON message | **a WebSocket ping control frame** (`WriteControl(PingMessage)`); the enum constant is unused on the wire | Server never pongs; every worker disconnects after `PongWait` (60s). Would have looked like a flaky network. |
| harness argv starts with flags | starts with **`agent run` positionals**, and `--idle-on-complete` can arrive with an **empty value** | A strict harness argv parser rejects every task. |

All three are now fixed in [`docs/01`](../../docs/01-evidence-and-provenance.md),
[`docs/04`](../../docs/04-protocols-and-apis.md),
[`docs/05`](../../docs/05-harness-contract.md), the README and the compose file.

This is the argument for doing M0 before M1–M2 rather than after: three protocol-level
mistakes, found in an afternoon, in a component small enough to throw away.

## Parts

| Path | What |
| --- | --- |
| [`../../cmd/m0-spike`](../../cmd/m0-spike) | The control plane: one WebSocket endpoint, one task, one verdict. One dependency (`gorilla/websocket`, the same library the worker uses). |
| [`harness/`](harness) | Stub harness sidecar. Its root filesystem becomes `/agent`, so `entrypoint.sh` lands at `/agent/entrypoint.sh`. |
| [`run.sh`](run.sh) | Preflight, build harness image, clone + build upstream worker, run both, assert, print logs. |
| `.work/` | Gitignored: cloned worker, binaries, logs. |

### Two non-obvious mechanics

**Sidecar materialization.** The worker creates a container from the sidecar image,
exports its whole filesystem as a tar, then extracts it onto a volume *using the sidecar
image itself* with the entrypoint overridden. So the image must contain `tar` — which is
why the stub sits on `busybox` rather than `scratch`. A real harness binary must also be
glibc-linked; musl/Alpine is unsupported for the task image.

**Image pull policy.** The Docker backend defaults to `Always`, which would try to pull
our locally-built `factory-m0-harness:dev` from a registry and fail. `run.sh` writes a
worker config with `image_pull_policy: IfNotPresent` so both images resolve locally.

## What this does *not* prove

- Nothing about the real `oz` CLI's own server contract
  ([`docs/01` · U2](../../docs/01-evidence-and-provenance.md#residual-unknowns)) — the stub
  harness replaces it, which is the point.
- Nothing about scheduling, concurrency, or `task_rejected` requeue behavior. The spike
  assigns exactly one task and has nowhere to requeue to. That is M2, and the deterministic
  scheduler simulation named in [`docs/12`](../../docs/12-build-sequence.md#testing-strategy)
  is where it gets tested.
- Nothing about cancellation. `task_cancellation` is implemented in the worker but the
  spike never sends it.

## Keep it running

`run.sh` is designed to be a CI job, not a one-off. Keeping the **unmodified upstream
worker** green against our server is a continuous test of the most intricate interface in
the system, and it is how we would find out that a protocol detail changed upstream —
rather than discovering it when a production worker stops claiming work.
