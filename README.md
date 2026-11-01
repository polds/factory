# factory

A design for a **self-hostable software factory orchestrator** — an open equivalent of
Warp's Oz (cloud agent platform) and Factories (multi-agent SDLC pipelines), with no
dependency on a vendor control plane.

Target operating modes, all from one definition:

| Mode | Shape |
| --- | --- |
| Laptop / single node | `docker compose up` — control plane, Postgres, MinIO, one worker |
| Self-hosted cloud | Helm chart: control plane + operator + worker pools |
| Cloud-native | Kubernetes CRDs (`factory.dev/v1alpha1`) reconciled by an operator |

The design docs are rendered at [factory.polds.dev](https://factory.polds.dev)
(`web/site`, published by `.github/workflows/site.yml`).

**Status.** The design is in `docs/`. M0 and M1 are done
([`spike/m0`](spike/m0), [`cmd/factory-harness`](cmd/factory-harness)).
M2 and M3 are done: the control plane, the work-item pipeline, and the
blocking spec gate. Laptop exits are `MODE=live ./spike/m2/run.sh` and
`MODE=live APPROVAL_DECISION=approve ./spike/m3/run.sh`. The M3 run on
Peter's Mac opened draft
[PR #50](https://github.com/polds/factory/pull/50). M5 is done. Peter's Mac,
`MODE=live ./spike/m5/run.sh`, printed `M5 LIVE PASS` (2 passed, 1 skipped)
on 2026-10-19, `main` at `dd186bd`
([#120](https://github.com/polds/factory/pull/120)). Evidence is in
[`docs/12-build-sequence.md`](docs/12-build-sequence.md). `m5-live.yml` is
still open. M6 code is merged. The measure and improve section is in
[`docs/12`](docs/12-build-sequence.md#m6--measure-and-improve-weeks), and the
driver is [`spike/m6`](spike/m6/README.md). The CI (`live-ci`) exit was met
([run 41243409323](https://github.com/polds/factory/actions/runs/41243409323)).
No laptop `MODE=live` run was done.
The `claude-code` adapter is not in this build.

## Quickstart

Laptop profile: `factoryd`, Postgres, MinIO, and one unmodified Docker-backend
worker. `deploy/m2-factory` is factory `m2` with one agent, so `run` needs
`--factory m2`.

`cp deploy/.env.example deploy/.env` leaves several values empty. An empty
line is set, not unset, so compose will not fill it. Before `up`, set:

| Variable | Required | Meaning |
| --- | --- | --- |
| `FACTORY_SECRET_GH_TOKEN` | yes | Allowlisted as `GH_TOKEN`. Empty fails assignment (`secret GH_TOKEN is not configured`). A dummy is enough to assign a run. A push needs a real token. |
| `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL` | for a model call | Copied into the task as `OPENAI_BASE_URL`. Empty: the harness exits `config_invalid`. |
| `FACTORY_INFERENCE_OPENAI_API_KEY` | when the endpoint requires a key | Copied into the task as `OPENAI_API_KEY`. |
| `FACTORY_TASK_SERVER_URL` | on Docker Desktop | `http://host.docker.internal:8080`. Linux default `http://172.17.0.1:8080` applies when this is empty. |

`spike/m2/run.sh` reads `deploy/.env` when that file exists. `MODE=live`
accepts the shell names (`OPENAI_BASE_URL`, `OPENAI_API_KEY`, `M2_MODEL`,
`GH_TOKEN`) and the compose names in the table above. A non-empty shell
variable wins. Empty assignments are ignored. Assignments are literal text,
not shell commands. The script does not print those values. `M2_REPO` defaults to `polds/factory`. The shipped model is
`m2-offline-stub` (`deploy/m2-factory/factory.yaml`). `FACTORY_API_KEY`
empty still becomes `m2-dev-api-key`.

The harness binary is executed inside a Linux task container. Build it for
the Docker daemon's architecture (`linux/arm64` or `linux/amd64`), not the
host OS. A macOS binary exits 126 there.

```bash
cp deploy/.env.example deploy/.env
awk '
  $0 == "FACTORY_SECRET_GH_TOKEN=" { print "FACTORY_SECRET_GH_TOKEN=m2-offline-dummy"; next }
  { print }
' deploy/.env > deploy/.env.new && mv deploy/.env.new deploy/.env

case "$(docker info --format '{{.Architecture}}')" in
  aarch64 | arm64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "unsupported docker architecture" >&2; exit 1 ;;
esac
platform="linux/${arch}"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
docker build --platform "$platform" -t factory-harness:dev harness/generic-openai
task_dir="spike/m1/task"
task_dir="${task_dir}-image"
docker build --platform "$platform" -t factory-m1-task:dev "$task_dir"
DOCKER_DEFAULT_PLATFORM="$platform" docker compose -f deploy/docker-compose.yml up -d --build --wait
go build -o bin/factoryctl ./cmd/factoryctl
FACTORY_API_KEY=m2-dev-api-key ./bin/factoryctl run --prompt "…" --factory m2 --watch
```

Do not commit `deploy/.env`. `--wait` returns after factoryd is healthy.
`up --build` builds factoryd and the upstream worker only. The full offline
pass, including the model stub, is `MODE=offline ./spike/m2/run.sh`. With only
the dummy token set, the run is accepted and assigned. `--watch` prints
`terminal status=config_invalid summary=OPENAI_BASE_URL is not set` and exits 1.
Details are in [`spike/m2/README.md`](spike/m2/README.md).

[`spike/m3`](spike/m3/README.md) is the pipeline. On Docker Desktop set
`FACTORY_TASK_SERVER_URL=http://host.docker.internal:8080`, point
`OPENAI_BASE_URL` at `host.docker.internal`, and set
`APPROVAL_DECISION=approve` only when the operator is ready to pass the spec
gate.

---

## How this design was derived

It is reverse-engineered from shipped artifacts and published contracts, not guessed.
The evidence ledger — what is *observed*, what is *derived*, what is *inferred*, and what
remains *unknown* — is in **[`docs/01-evidence-and-provenance.md`](docs/01-evidence-and-provenance.md)**.
Read that first if you intend to rely on any claim here.

Primary sources:

- `warpdotdev/oz-agent-worker` @ MIT — the real self-hosted data plane, in Go.
  Gave us the worker↔server WebSocket protocol, the task payload, and four
  execution backends.
- `docs.warp.dev/openapi.yaml` — the published Oz Agent API (3,000+ lines, 40 paths).
- `docs.warp.dev/_llms-txt/factories.txt` — the complete definition-file schema.
- `warpdotdev/warp-factory-examples` @ v1alpha1 — working definition trees.
- `Warp.app` 0.2026.09.30 arm64 Mach-O — raw byte scan for undocumented endpoints.

## The three decisions that shape everything

### 1. Reimplement the control plane; reuse the data plane

The data plane (`oz-agent-worker`) is MIT-licensed and good: four backends
(Docker, Kubernetes, Direct, Command), preflight RBAC validation, Job watching,
structured failure capture, OTel metrics. The control plane is the closed part, and
it is the part that makes the product.

So: **build `factoryd`, keep the worker.** Our control plane is wire-compatible with
the observed worker protocol, which means an *unmodified* upstream worker can be pointed
at a self-hosted `factoryd` with one hidden flag:

```bash
oz-agent-worker --worker-id local-1 \
  --web-socket-url ws://factoryd:8080/api/v1/selfhosted/worker/ws \
  --server-root-url http://factoryd:8080
```

**This is verified, not asserted.** [`spike/m0`](spike/m0) runs the unmodified upstream
worker against a 400-line control plane and confirms the whole handshake end to end:

```
  PASS  worker authenticated         m0-spike
  PASS  task assignment accepted     task_assignment written to socket
  PASS  task claimed                 m0-spike
  PASS  heartbeat answered           1 ping/pong exchange(s)
  PASS  terminal state               task_completed: Task completed successfully
VERDICT: confirmed — an unmodified worker ran our harness against our server.
```

See [`docs/04-protocols-and-apis.md`](docs/04-protocols-and-apis.md) for the protocol and
[`spike/m0/README.md`](spike/m0/README.md) for what the spike corrected.

### 2. The harness is a contract, not a binary

Warp's worker shells out to `oz`, a proprietary CLI that authenticates to Warp. That is
the one piece we cannot reuse — and the one place a self-hosted system must be *more*
open, not merely equivalent.

We define **Harness Contract v1**: a container image mounted at `/agent` exposing
`/agent/entrypoint.sh`, fed a task spec, emitting NDJSON events. Adapters ship for Claude
Code, Codex, and Warp Agent CLI. The contract deliberately accepts the observed `oz` flag
vocabulary, so an unmodified worker can drive our harness.
See [`docs/05-harness-contract.md`](docs/05-harness-contract.md).

### 3. Pipeline policy is declarative, not prompt-resident

Warp documents that its stage routing and two of its three human gates are "workflow
policy, written into the foreman's instructions." Prompt-resident policy cannot be
validated, diffed, or enforced.

We lift it into `factory.yaml` as a `pipeline:` block with explicit `approvals:` gates,
enforced by the orchestrator rather than requested of a model. This is the design's main
intentional divergence; rationale in
[`docs/03-definition-schema.md`](docs/03-definition-schema.md#deviation-declarative-pipeline).

## Compatibility, verified

The definition schema is not merely "inspired by" `v1alpha1` — it is checkable against it.
Running [`examples/acme-factory`](examples/acme-factory) through Warp's own published
validator (`validate_factory_files.py`, which uses the `warp-server` parser) reports
unknown-field errors for **exactly** the six keys this design documents as extensions, and
nothing else:

```
5 problem(s) in examples/acme-factory:
  factory.yaml:33: FF_UNKNOWN_FIELD: unknown field "pool"
  factory.yaml:36: FF_UNKNOWN_FIELD: unknown field "inference"
  factory.yaml:48: FF_UNKNOWN_FIELD: unknown field "pipeline"
  factory.yaml:66: FF_UNKNOWN_FIELD: unknown field "staleFailureDays"
  factory.yaml:71: FF_UNKNOWN_FIELD: unknown field "model"      # scorerDefaults.model
Validated with the warp-server parser for v1alpha1
```

Five agents, a three-provider automation, a runner, a scorer, a skill, and a webhook all
validate clean. A Warp definition therefore ports by *addition*, not translation — and
ours degrades to a valid Warp definition by deleting six keys.

## Component map

```
                 ┌─────────────────────────────────────────────┐
  Slack/Linear   │                 factoryd                    │
  GitHub/GitLab ─┤  ingress · automations · orchestrator        │
  Webhooks       │  definition sync · inference gw · console    │
  Schedules      └───┬──────────────┬─────────────────┬─────────┘
                     │ WS           │ SQL             │ S3
              ┌──────▼──────┐  ┌────▼─────┐   ┌───────▼────────┐
              │   workers   │  │ Postgres │   │ object store   │
              │ docker/k8s/ │  │ runs·defs│   │ transcripts·   │
              │ direct/cmd  │  │ queue    │   │ artifacts      │
              └──────┬──────┘  └──────────┘   └────────────────┘
                     │ spawns
              ┌──────▼───────────────────────┐
              │ task container               │
              │  repo checkout + /agent      │ ← Harness Contract v1
              │  (claude-code | codex | …)   │
              └──────────────────────────────┘
```

Binaries: `factoryd` (control plane + console), `factory-worker` (data plane),
`factory-operator` (CRD reconciler), `factoryctl` (CLI).

## Documents

| Doc | Covers |
| --- | --- |
| [01 Evidence & provenance](docs/01-evidence-and-provenance.md) | What is observed vs inferred; residual unknowns |
| [02 Architecture](docs/02-architecture.md) | Planes, components, data model, run lifecycle |
| [03 Definition schema](docs/03-definition-schema.md) | Agents as code: factory.yaml, agents, automations, runners, routers |
| [04 Protocols & APIs](docs/04-protocols-and-apis.md) | Worker WebSocket protocol, REST API, event stream |
| [05 Harness contract](docs/05-harness-contract.md) | The `/agent` sidecar ABI and harness adapters |
| [06 Execution backends](docs/06-execution-backends.md) | Docker, Kubernetes, Direct, Command; isolation |
| [07 Integrations & webhooks](docs/07-integrations-and-webhooks.md) | Slack, Linear, GitHub; trigger filters; webhook ingress |
| [08 Measure & self-improvement](docs/08-measure-and-self-improvement.md) | Scorers, benchmarks, the improvement loop |
| [09 Web console](docs/09-web-console.md) | Surfaces, live attach, inbox |
| [10 Kubernetes & Helm](docs/10-kubernetes-and-helm.md) | CRDs, operator, chart values |
| [11 Security model](docs/11-security-model.md) | Credential boundaries, identity, egress, audit |
| [12 Build sequence](docs/12-build-sequence.md) | Milestones, with a walking skeleton first |

## Repo layout (proposed)

```
docs/                  this design
deploy/
  docker-compose.yml   single-node stack
  crds/                CustomResourceDefinitions
  helm/                chart skeleton
examples/
  acme-factory/        a complete definition tree
```

## Licensing note

`oz-agent-worker` is MIT (Copyright 2026 Denver Technologies, Inc.). A fork must retain
that notice. Warp's *documentation* and *API shape* are not code; the definition-file
schema is reused here as an interoperability format, which is the same reason we publish
JSON Schemas at our own endpoint. Nothing in this design copies Warp source beyond the
MIT-licensed worker.
