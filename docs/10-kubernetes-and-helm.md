# 10 — Kubernetes CRDs and Helm

Two Kubernetes modes. They are not alternatives to each other so much as two answers to
"where does the definition live".

| Mode | Source of truth | Chart installs | Use when |
| --- | --- | --- | --- |
| **Chart-only** | Git (or local) definition tree | `factoryd`, workers | You want GitOps on a Git repo and a plain namespace-scoped install |
| **CRD mode** | Kubernetes API | + `factory-operator`, CRDs | You already drive everything through the cluster API, ArgoCD/Flux, or need cluster-native RBAC and admission on factory resources |

Upstream's chart deliberately creates **no CRDs and no cluster-scoped RBAC**
(O: observed). We keep that as the default, because it is the right default — a
namespace-scoped install needs no cluster-admin. CRD mode is additive and entirely our own
design, not a reconstruction.

## CRDs

API group `factory.dev`, version `v1alpha1`. The custom resources mirror the definition
documents one-for-one ([03](03-definition-schema.md)) so a tree and a set of manifests are
mechanically convertible — `factoryctl convert ./definition-tree` emits manifests, and
`factoryctl convert --from-cluster` goes the other way.

| Kind | Scope | Maps to |
| --- | --- | --- |
| `Factory` | Namespaced | `factory.yaml` |
| `FactoryAgent` | Namespaced | `agents/<n>/agent.md` |
| `Automation` | Namespaced | `automations/<n>/automation.md` |
| `Runner` | Namespaced | `runners/<n>.yaml` |
| `ModelRouter` | Namespaced | `routers/<n>.yaml` |
| `Scorer` | Namespaced | `scorers/<n>/scorer.md` |
| `Skill` | Namespaced | `skills/<n>/SKILL.md` |
| `FactoryWebhook` | Namespaced | `webhooks/<n>.yaml` |
| `BenchmarkSuite` | Namespaced | `benchmarks/<slug>/` |
| `AgentRun` | Namespaced | a run (imperative; created to start work) |
| `WorkerPool` | Namespaced | worker capacity declaration |

All namespaced. Nothing cluster-scoped except the CRDs themselves, so a team can own a
namespace without cluster-wide blast radius.

### `Factory`

```yaml
apiVersion: factory.dev/v1alpha1
kind: Factory
metadata:
  name: payments
  namespace: factory-payments
spec:
  displayName: payments-factory
  alias: payments
  codeForges: [GITHUB]
  repositories:
    - { codeForge: GITHUB, owner: acme, name: payments-service }
    - { codeForge: GITHUB, owner: acme, name: payments-docs, deferred: true }
  credentialStrategy: EXECUTOR
  agentDefaults:
    model: auto
    runner: linux-standard
    poolRef: { name: standard }
  inference:
    default: { provider: anthropic, credentialRef: anthropic-api-key }
    providers:
      anthropic: { kind: anthropic, credentialRef: anthropic-api-key }
      bedrock:   { kind: bedrock, region: us-west-2,
                   roleArn: arn:aws:iam::123456789012:role/factory-inference }
  pipeline:
    stages: [triage, planning, building, reviewing]
    agents: { triage: triage, planning: spec, building: implementer, reviewing: reviewer }
    approvals:
      spec:    { required: true, approvers: "role:editor" }
      handoff: { required: true, approvers: "role:user" }
    revisionLimit: 3
  selfImprovement:
    failedRunThreshold: 25
    staleFailureDays: 7
    reviewerType: admins
  secretRefs:                       # Kubernetes Secrets, by name
    - { name: sentry-auth-token, as: SENTRY_AUTH_TOKEN }
status:
  phase: Ready                      # Pending | Validating | Ready | Invalid | Degraded
  observedGeneration: 7
  resources: { agents: 5, automations: 3, runners: 2, scorers: 2 }
  conditions:
    - type: DefinitionValid
      status: "True"
      reason: Validated
      lastTransitionTime: "2026-10-12T19:04:11Z"
    - type: Ready
      status: "True"
      reason: Ready
      message: factoryd is serving this definition
```

### `FactoryAgent`

```yaml
apiVersion: factory.dev/v1alpha1
kind: FactoryAgent
metadata:
  name: reviewer
  namespace: factory-payments
spec:
  factoryRef: { name: payments }
  agentType: REVIEW                 # CUSTOM|FOREMAN|TRIAGE|SPEC|IMPLEMENT|REVIEW|VERIFY
  description: Adversarial code review. Advisory verdict; humans merge.
  model: auto-genius
  spawnableBy: [foreman]
  idleTimeoutMinutes: 15
  runner: linux-standard
  requiredRepos:
    - { codeForge: GITHUB, owner: acme, name: payments-service }
  secretRefs:
    - { name: sonar-token, as: SONAR_TOKEN }
  mcpServers:
    sentry:
      url: https://mcp.sentry.internal/sse
      headers:
        Authorization: { secretKeyRef: { name: sentry-mcp, key: token } }
  instructions: |
    # Review
    You review a diff adversarially…
  instructionsFrom:                 # alternative to inline: a ConfigMap key
    configMapKeyRef: { name: reviewer-instructions, key: agent.md }
status:
  phase: Ready
  effectiveConfig:                  # after inheritance, for debuggability
    harness: { type: warp-agent, model: auto-genius }
    pool: standard
```

`instructionsFrom` matters more than it looks: agent prompts are long Markdown, and
embedding them in a CR makes `kubectl get -o yaml` unreadable and ArgoCD diffs noisy.
A ConfigMap keeps prompts reviewable as files while the CR stays configuration.

`status.effectiveConfig` publishes the *result* of inheritance. Multi-level sparse
overrides ([03](03-definition-schema.md#inheritance)) are the single most confusing part
of this schema, so the operator computes and shows the answer rather than making operators
simulate it.

### `AgentRun`

The one imperative resource: create it to start work.

```yaml
apiVersion: factory.dev/v1alpha1
kind: AgentRun
metadata:
  generateName: hotfix-
  namespace: factory-payments
spec:
  factoryRef: { name: payments }
  agentRef:   { name: foreman }
  prompt: |
    The /health endpoint returns 500 after the 2026-10-11 deploy. Find the
    cause, fix it, add a regression test, and open a PR.
  priority: interactive             # interactive | pipeline | batch
  poolRef: { name: standard }
  externalRef: { provider: manual, id: INC-4471 }
  timeoutSeconds: 10800
status:
  phase: Running                    # Queued|Assigned|Running|AwaitingInput|Succeeded|Failed|Cancelled
  runId: run_01JA7Z…
  workItemId: wi_01JA7Z…
  workerId: k8s-prod
  executions: [{ id: exec_01JA7Z…, attempt: 1, state: running }]
  conditions: [{ type: Scheduled, status: "True" }]
  usage: { inputTokens: 184210, outputTokens: 9120, costCents: 212 }
  prUrls: ["https://github.com/acme/payments-service/pull/1182"]
```

Deleting an `AgentRun` cancels it (finalizer sends `task_cancellation` and waits for a
terminal message, with a bounded grace period). `kubectl delete` meaning "stop the agent"
is the behavior a Kubernetes operator expects, and the finalizer is what keeps it from
orphaning a running container.

### `WorkerPool`

```yaml
apiVersion: factory.dev/v1alpha1
kind: WorkerPool
metadata:
  name: standard
  namespace: factory-payments
spec:
  backend: kubernetes
  platform: { os: linux, arch: x86_64 }
  replicas: 2
  maxConcurrentTasks: 8
  labels: { tier: standard }
  harnessImages:
    warp-agent:  ghcr.io/acme/factory-harness-warp:1.4.0
    claude-code: ghcr.io/acme/factory-harness-claude:1.4.0
    codex:       ghcr.io/acme/factory-harness-codex:1.4.0
  kubernetesBackend:
    namespace: factory-runs
    defaultImage: ghcr.io/acme/ci-base:2026-10
    activeDeadlineSeconds: 28800
    workspaceSizeLimit: 20Gi
    unschedulableTimeout: 10m
    podTemplate:
      serviceAccountName: factory-task
      nodeSelector: { factory.dev/pool: standard }
status:
  connectedWorkers: 2
  inFlight: 3
  queueDepth: 0
  rejectRate1m: 0
```

The operator owns the worker Deployment for a `WorkerPool`, which is what makes capacity
declarative: scale a pool by editing `replicas`, not by finding the right Helm value.

### Validation

Three layers, each catching what the previous cannot:

1. **Structural** — OpenAPI v3 schemas on the CRDs, with `x-kubernetes-validations` (CEL)
   for intra-resource rules: exactly one of `model`/`harness`; `signatureScheme` present
   iff `authMode: signature`; `reviewerEmails` non-empty iff `reviewerType: custom`; at
   least one scorer label on each side of `passingScore`.
2. **Admission** — a validating webhook for cross-resource rules CEL cannot see: does
   `agentDefaults.runner` name an existing `Runner`? does an `Automation` target an
   existing `FactoryAgent`? is there exactly one `FOREMAN` per `Factory`? does a change
   weaken `pipeline.approvals` ([08](08-measure-and-self-improvement.md#guard-rails))?
3. **Reconcile** — the operator sets `DefinitionValid=False` with a reason for anything
   admission allowed but the control plane rejects, and **keeps serving the last valid
   generation**. Same atomicity guarantee as the Git path: a bad apply never leaves a
   factory half-configured.

## Helm chart

```
deploy/helm/factory/
├── Chart.yaml
├── values.yaml
├── crds/                 install-only CRDs (not under templates/)
└── templates/
    ├── render.yaml       includes the partials below
    ├── _helpers.tpl
    ├── _controlplane.tpl
    ├── _worker.tpl
    ├── _migrations.tpl
    ├── _dev.tpl
    └── _operator.tpl
```

### Values

```yaml
global:
  image: { registry: ghcr.io/acme, pullPolicy: IfNotPresent, tag: "" }  # tag: never "latest"
  imagePullSecrets: [{ name: ghcr-pull }]

controlPlane:
  enabled: true
  replicas: 2
  externalUrl: https://factory.acme.internal
  resources:
    requests: { cpu: 500m, memory: 512Mi }
    limits:   { memory: 2Gi }
  ingress:
    enabled: true
    className: nginx
    host: factory.acme.internal
    tls: { secretName: factory-tls }
  database:
    # external Postgres strongly recommended in production
    existingSecret: factory-postgres          # keys: host, port, user, password, dbname
    sslMode: require
  objectStore:
    endpoint: https://s3.us-west-2.amazonaws.com
    bucket: acme-factory
    region: us-west-2
    existingSecret: factory-s3                # or irsa: true
    irsa: false
  auth:
    oidc:
      issuer: https://login.acme.com
      clientId: factory
      existingSecret: factory-oidc
  inference:
    providers:
      anthropic: { existingSecret: anthropic-api-key }
  telemetry:
    otlpEndpoint: http://otel-collector.observability.svc:4318

postgresql:
  enabled: false          # true runs an in-chart Postgres — dev/eval only
minio:
  enabled: false          # true runs an in-chart MinIO — dev/eval only

operator:
  enabled: false          # CRD mode
  installCRDs: false
  watchNamespaces: []     # empty = own namespace only
  admissionWebhook:
    enabled: true
    certManager: false       # self-signed serving cert; true selects cert-manager

workers:
  - name: standard
    enabled: true
    backend: kubernetes
    replicas: 2
    workerId: k8s-standard
    pool: standard
    maxConcurrentTasks: 8
    logLevel: info
    cleanup: true
    terminationGracePeriodSeconds: 600        # pod grace only; not a 600s task drain
    apiKeySecret: { name: factory-worker, key: FACTORY_API_KEY, create: false }
    resources:
      requests: { cpu: 100m, memory: 128Mi }
    nodeSelector: {}
    tolerations: []
    affinity: {}
    metrics: { enabled: true, exporter: prometheus, port: 9464 }
    kubernetesBackend:
      namespace: factory-runs
      defaultImage: ubuntu:22.04
      imagePullPolicy: IfNotPresent
      useImageVolumes: false
      preflightImage: busybox:1.36
      activeDeadlineSeconds: 28800
      ttlSecondsAfterFinished: 86400
      workspaceSizeLimit: 20Gi
      unschedulableTimeout: 10m
      extraAnnotations: { karpenter.sh/do-not-disrupt: "true" }
      podTemplate: {}
```

### Chart decisions

- **`image.tag` must be pinned.** The chart fails rendering on an empty tag rather than
  defaulting to `latest`, because a worker that silently changes version mid-incident is a
  bad afternoon.
- **In-chart Postgres and MinIO are opt-in and labelled dev/eval.** They exist so
  `helm install` works in five minutes; shipping them as defaults would quietly encourage
  stateful workloads nobody is backing up.
- **Migrations run as a pre-install/pre-upgrade hook Job** with the chart's own
  ServiceAccount, and the control plane refuses to start against an unmigrated schema.
- **Worker `terminationGracePeriodSeconds` defaults to 600, not 30.** That is the
  pod's grace. The worker process at `63dbb8a` does not drain for 600s: its
  `BackendShutdownTimeout` is 10s, and the Kubernetes backend's `Shutdown` preserves
  Jobs instead of waiting for them. The AgentRun finalizer's cancel grace is 120s.
- **Namespace-scoped RBAC for workers**, exactly the upstream permission set: `jobs`
  (`create|get|list|watch|patch|delete`), `pods` (`get|list|watch`), `pods/log` (`get`),
  `events` (`list`). Nothing cluster-scoped.
- **A preflight hook** runs the worker's own RBAC/admission check at install time, so a
  cluster policy that blocks root init containers fails the install rather than the first
  real run.
- **CRDs are install-only.** Helm does not upgrade CRDs; `factoryctl crds apply` handles
  them so a CRD schema change is an explicit, versioned step.
- **Task namespace is separate from the control-plane namespace** by default
  (`factory-runs` vs `factory`), so agent workloads can carry NetworkPolicy and
  admission policy that would be wrong for the control plane
  ([11](11-security-model.md#egress)).

### M7 applied

Shipped behavior where it differs from the sketch above:

- **D1.** The operator publishes a Factory through
  `PUT /api/v1/factory/{name}/definition` (generation plus files). A git-backed
  factory returns 409. An invalid tree returns `valid: false` and leaves the
  last good generation serving. The control plane is API-backed only when
  `FACTORY_DEFINITION_SOURCE=api` and there is no git URL. See
  [04](04-protocols-and-apis.md#factories-and-definitions).
  The status condition the operator sets is `Ready`
  (`internal/operator/factory.go:31`, `condReady`), not `ControlPlaneSynced`.
  When the definition is valid, `factoryStatus` sets that condition's reason
  to `Ready` and its message to `factoryd is serving this definition`
  (`internal/operator/factory.go:216`).
- **D2.** Deleting an AgentRun cancels the run and waits up to 120s
  (`cancelGrace` in `internal/operator/agentrun.go`). If the run is still not
  terminal, the condition is `CancelTimedOut` and the finalizer is removed.
  `spec` is immutable after create.
- **D6.** The chart is `templates/render.yaml` plus the `_*.tpl` partials, not
  one directory per component. Worker RBAC is copied from upstream
  `charts/oz-agent-worker` at `63dbb8a`, including `patch` on jobs.
- **D7.** `admissionWebhook.certManager` defaults to false
  (`deploy/helm/factory/values.yaml`). The operator rotates its own serving
  certificate. `certManager: true` is the cert-manager path.
- **D8.** `factoryctl convert` covers the definition tree, `--from-manifests`,
  and `--from-cluster`. AgentRun and WorkerPool are never converted.
- **D9.** A WorkerPool reconciler owns that pool's Deployment, ConfigMap, and
  RBAC. Chart-only mode still uses `workers[]` in values. The M7 exit uses the
  chart worker, not a WorkerPool manifest.
- **D10.** CRDs live in `deploy/helm/factory/crds/`. `operator.installCRDs` does
  not gate that directory (Helm installs it; Helm does not upgrade CRDs).
  `factoryctl crds apply` is the upgrade path.
- **Replicas.** With `operator.enabled`, rendering fails unless
  `controlPlane.replicas` is 1 and autoscaling is off. API-backed definitions
  are per pod. A shared store for more than one factoryd replica is M8.
  Chart-only mode still defaults to 2 replicas.
- **Values.** `values.yaml` has a `worker:` image default and a `workers:` list.
  `terminationGracePeriodSeconds: 600` is the pod grace described above, not a
  600s process drain.

## Scan security (M10-S, opt-in)

G14/G18 **foundations** add optional Helm values only; defaults are unchanged:

| Value | Purpose |
| --- | --- |
| `controlPlane.secretProjection.enabled` | Projected Kubernetes secret files at `FACTORY_SECRET_MOUNT_DIR` for resolver mounts |
| `scanSecurity.enabled` | Emit synthetic enforcement helpers (NetworkPolicy template when egress is enabled) |
| `scanSecurity.egress.networkPolicy` | Baseline deny-metadata egress policy in task namespaces |

See [11-security-deployment.md](11-security-deployment.md). Helm template and kind smoke **do not** attest the live Docker acceptance host; M10-G owns that gate.

## GitOps

Both modes work under ArgoCD/Flux, with one caveat each:

- **Chart-only:** the chart is the app; the definition tree is a separate repo the control
  plane polls or receives webhooks for. Two sources, two review flows.
- **CRD mode:** manifests are the app, so the definition is reconciled by your existing
  GitOps controller and the operator only projects it. One review flow — which is the
  reason CRD mode exists.

In CRD mode, `AgentRun` resources are **not** managed by GitOps (they are imperative and
ephemeral); exclude them with an ignore rule, or ArgoCD will try to prune every finished
run.
