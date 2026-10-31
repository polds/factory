# 06 — Execution backends

A worker turns a task assignment into a running sandbox. Four backends, inherited from the
MIT worker (O1) and kept because they already cover the realistic deployment
space.

| Backend | Isolation | Operator runs | Choose when |
| --- | --- | --- | --- |
| **docker** (default) | One container per run | Worker daemon, host, Docker daemon, images | Docker is available; you want per-run isolation without Kubernetes |
| **kubernetes** | One Job per run | Worker Deployment, cluster, namespace RBAC, admission, capacity | You already operate Kubernetes, or need cluster-native policy |
| **direct** | Workspace directory on the host, shared OS/kernel | Worker daemon, host security, dependencies, cleanup | No container runtime, or runs need host resources (macOS, device access) |
| **command** | Whatever your runtime provides | A dispatch script and the target runtime | Dispatch to an existing queue, batch system, or VM fleet |

Only one backend per worker process. Run heterogeneous capacity by running several workers
in different pools.

## How runner settings land

A runner declares intent; the backend decides what the intent means. This matrix is
observed behavior, and it is the thing operators get wrong most often:

| Runner setting | docker | kubernetes | direct | command |
| --- | --- | --- | --- | --- |
| `platform.os` / `arch` | Docker daemon decides; use linux | Cluster + `pod_template` schedule nodes; use linux | **Ignored** — the host is the platform | Your runtime decides |
| `platform.linux.dockerImage` | Task container image | Task + setup container image | **Ignored** | Passed through in the payload |
| `platform.linux.registryCredentialSecretName` | **Ignored** — configure the worker's Docker config | **Ignored** — use `imagePullSecrets` | Ignored | Ignored |
| `instanceShape` | Container CPU/memory **limits** | Task container **requests and limits**, overriding `pod_template` | **Ignored** — task uses host resources | Passed through |
| `setupCommands` | Run in the container during preparation | Run in the task container | Run in the workspace | Your runtime runs them |

Two consequences worth stating plainly, because they look like bugs:

- **Private registries are a worker concern, not a definition concern.** A definition
  cannot grant image-pull credentials to self-hosted capacity; the operator configures the
  worker's Docker config or `imagePullSecrets`.
- **Runner settings never provision capacity.** `instanceShape: {vcpus: 32}` on a pool of
  4-core nodes yields an unschedulable task, not a larger node — unless the cluster
  autoscales, which is what `unschedulable_timeout` is for.

## Docker backend

```yaml
worker_id: local-1
cleanup: true
max_concurrent_tasks: 4
idle_on_complete: "15m"
backend:
  docker:
    image_pull_policy: IfNotPresent      # Always (default) | IfNotPresent | Never
    sidecar_image: ghcr.io/acme/factory-harness-claude:1.4.0
    volumes:
      - "/opt/shared-cache:/cache:ro"
    environment:
      - { name: NPM_TOKEN, value: "…" }
      - { name: GITHUB_TOKEN }           # inherited from the worker host
```

Needs the Docker socket (`-v /var/run/docker.sock:/var/run/docker.sock`), which grants the
worker container control of the host daemon — effectively host-root. Treat a Docker-backend
worker host as a single trust domain and do not co-locate it with unrelated workloads.

The harness arrives as a **volume-mounted sidecar at `/agent`**, not as a layer in the task
image. That is what lets one CI image serve every harness, and it is why
`sidecar_image` is overridable for registry mirrors.

Two levels of `-e` exist and are easy to confuse: Docker's `-e` sets variables on the
**worker container** (`FACTORY_API_KEY`), the worker's own `-e/--env` sets variables inside
the **task containers** it spawns.

## Kubernetes backend

```yaml
worker_id: k8s-prod
max_concurrent_tasks: 8
backend:
  kubernetes:
    namespace: factory-runs
    default_image: ghcr.io/acme/ci-base:2026-10   # env image > default_image > ubuntu:22.04
    image_pull_policy: IfNotPresent
    use_image_volumes: false        # true avoids root init containers (needs runtime support)
    preflight_image: busybox:1.36
    preflight_resources:
      requests: { cpu: 50m, memory: 64Mi }
    sidecar_image: ghcr.io/acme/factory-harness-claude:1.4.0
    coding_cli_sidecars:
      claude-code: ghcr.io/acme/claude-cli:1.9.0
      codex:       ghcr.io/acme/codex-cli:0.7.2
    setup_command: /opt/scripts/pre-task.sh
    teardown_command: /opt/scripts/post-task.sh
    active_deadline_seconds: 28800      # 8h
    ttl_seconds_after_finished: 86400   # keep failed Jobs 24h for diagnosis
    workspace_size_limit: 20Gi
    unschedulable_timeout: "10m"        # 0s disables; keep > node provisioning time
    extra_labels:      { app.kubernetes.io/part-of: factory }
    extra_annotations: { karpenter.sh/do-not-disrupt: "true" }
    pod_template:
      serviceAccountName: factory-task
      imagePullSecrets: [{ name: ghcr-pull }]
      nodeSelector: { kubernetes.io/os: linux, factory.dev/pool: standard }
      tolerations:
        - { key: dedicated, operator: Equal, value: agents, effect: NoSchedule }
      containers:
        - name: task                    # this exact name customizes the main container
          resources:
            requests: { cpu: "2", memory: 4Gi }
            limits:   { memory: 8Gi }
          env:
            - name: GITHUB_TOKEN
              valueFrom: { secretKeyRef: { name: forge-creds, key: github-token } }
```

Requirements, all observed upstream and all load-bearing:

- **glibc images.** musl/Alpine is unsupported — the harness sidecar binaries are
  glibc-linked. Using Alpine produces a confusing loader error, so the preflight check
  tests for it explicitly.
- **Namespace-scoped RBAC:** `create|get|list|watch|delete` on `jobs`; `get|list|watch` on
  `pods`; `get` on `pods/log`; `list` on `events`.
- **Root init container** permission in the task namespace, unless `use_image_volumes: true`.
- A **startup preflight Job** validates RBAC and admission policy before any real task
  runs, so a misconfigured cluster fails at deploy time instead of on someone's PR.
- `karpenter.sh/do-not-disrupt` blocks consolidation but **not** expiration, interruption,
  or node repair. A long agent run can still be evicted; that is why orphaned executions
  requeue ([02](02-architecture.md#failure-behavior)).

Capacity planning: set `max_concurrent_tasks` to match real cluster headroom, reserve room
for init containers and memory spikes, and keep `unschedulable_timeout` longer than node
provisioning or autoscaling will look like failure.

### Deliberate addition: Job vs. operator

The upstream chart creates **no CRDs and no cluster-scoped RBAC** (O: observed).
We keep that mode as the default, and add an optional operator path
([10](10-kubernetes-and-helm.md)) for teams that want the cluster to be the source of
truth. The worker's Job-creating path is unchanged in both; the operator adds declarative
resources above it, not a second executor.

## Direct backend

```yaml
worker_id: macos-build-1
max_concurrent_tasks: 2
backend:
  direct:
    workspace_root: /var/lib/factory/workspaces
    oz_path: /usr/local/bin/factory-harness
    setup_command: /opt/scripts/setup.sh
    teardown_command: /opt/scripts/teardown.sh
    environment:
      - { name: DEVELOPER_DIR, value: /Applications/Xcode.app/Contents/Developer }
```

Runs in a workspace directory on the host, sharing its OS and kernel. This is the only
path for **macOS builds** (iOS/macOS toolchains cannot be containerized on Linux) and for
runs needing host devices.

Isolation is the host's, which means: a dedicated machine, a dedicated unprivileged user,
no other workloads, and a teardown that actually removes the workspace. `setup_command`
receives `FACTORY_WORKSPACE_ROOT`, `FACTORY_RUN_ID`, `FACTORY_ENVIRONMENT_FILE`, and
`FACTORY_WORKER_BACKEND`, which is enough to implement **git worktree isolation** for a
monorepo — the standard trick for making Direct runs cheap: one bare clone, a worktree per
run, teardown prunes it.

`--one-shot` (Direct only, forces concurrency 1) exits after a single task reaches a
terminal state. That is the primitive for an external scheduler that allocates a fresh VM
per run: boot, run one task, destroy. Per-run VM isolation with no container runtime.

## Command backend

Dispatches to any runtime over any transport. The worker invokes `dispatch_command` with a
**stable, versioned JSON payload on stdin** (O: `DispatchPayloadVersion = 1`):

```jsonc
{
  "version": 1,
  "run_id": "run_01J…",
  "execution_id": "exec_01J…",
  "server_root_url": "http://factoryd.internal:8080",
  "worker_id": "command-1",
  "docker_image": "ghcr.io/acme/ci-base:2026-10",
  "base_args": ["--task-id", "run_01J…", "--model", "…"],
  "env": { "GITHUB_ACCESS_TOKEN": "…" },
  "sidecars": [{ "image": "…", "mount_path": "/agent", "read_write": false }],
  "task": { /* full task object */ }
}
```

```yaml
backend:
  command:
    dispatch_command: "python3 /opt/factory/dispatch.py"
    cancel_command:   "python3 /opt/factory/cancel.py"
    dispatch_timeout: "60s"
    environment:
      - { name: DISPATCH_URL, value: https://runtime.internal/agent-runs }
      - { name: DISPATCH_AUTH_HEADER }     # inherited
```

**Credentials travel only inside `env` in the stdin payload** — never in the dispatch
subprocess's own environment or argv, where they would be visible in `ps` output. Any
dispatch script must preserve that property; the reference script does, and the conformance
test checks it.

The dispatch command's job is to hand the payload to a runtime and return quickly;
`dispatch_timeout` bounds the handoff, not the run.

## Workspace and image lifecycle

- **Checkout.** The control plane resolves `repositories` + `required_repos` into a
  prepared checkout list, mints a scoped forge token, and the harness clones into
  `/workspace/<repo>`. `deferred: true` repos are skipped unless required.
- **Head overrides.** A benchmark trial puts frozen SHAs on the assignment
  (`repository_head_overrides`, including `clone_from` and `preserve_origin`).
  The worker forwards that JSON as `--repository-head-override-json`. The in-repo
  harness accepts the flag and does not check the pinned SHA out
  (`internal/harness/argv.go`).
- **Origin stripping.** `--remove-repository-origins` removes remotes after clone, so an
  agent cannot push to a repository the run was not meant to touch. Cheap, effective, and
  the right default for untrusted intake.
- **Images.** Precedence is environment image → `default_image` → `ubuntu:22.04`. Build and
  cache environment images out of band; a cold `pnpm install` on every run dominates
  latency and cost.
- **Workspace volume.** `emptyDir` on Kubernetes (bounded by `workspace_size_limit`), a
  container volume on Docker, a directory on Direct. Never a shared network volume — two
  concurrent runs sharing a checkout corrupt each other.

## Monitoring

OpenTelemetry on the worker, configured by environment (`OTEL_METRICS_EXPORTER` ∈
`prometheus|otlp|console|none`, `OTEL_EXPORTER_PROMETHEUS_HOST/PORT` default
`localhost:9464`, `OTEL_EXPORTER_OTLP_ENDPOINT/PROTOCOL`). In Kubernetes set the bind host
to `0.0.0.0` or nothing scrapes it.

Minimum useful signals: worker connection state and reconnect count; tasks
claimed/rejected/completed/failed by `failure_reason`; in-flight vs. `max_concurrent_tasks`;
queue depth and time-to-assignment; image pull duration; unschedulable duration. Alert on
`task_rejected` rate (capacity too small) and time-to-assignment (pool starved), not on
individual run failures.
