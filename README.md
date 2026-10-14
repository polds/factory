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

**Status.** The design is in `docs/`. M0, M1, and M2 are done: the protocol
spike ([`spike/m0`](spike/m0)), the `generic-openai` harness
([`cmd/factory-harness`](cmd/factory-harness)), and a laptop `factoryd` that
opens a pull request. The `claude-code` adapter is not in this build.
[`docs/12-build-sequence.md`](docs/12-build-sequence.md) is the order of work.

## Quickstart

Laptop profile: `factoryd`, Postgres, MinIO, and one unmodified Docker-backend
worker.

```bash
cp deploy/.env.example deploy/.env
docker compose -f deploy/docker-compose.yml up -d --build
go build -o bin/factoryctl ./cmd/factoryctl
FACTORY_API_KEY=m2-dev-api-key ./bin/factoryctl run --prompt "…" --watch
```

Compose reads `deploy/.env` (do not commit it). `m2-dev-api-key` is the compose
default for `FACTORY_API_KEY`. The task and harness images are local tags that
compose does not build, and `deploy/m2-factory` ships with model
`m2-offline-stub`. Offline, live, and a laptop run that fills `deploy/.env`
are in [`spike/m2/README.md`](spike/m2/README.md).

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
