# 03 — Definition schema (agents as code)

A factory is a **versioned tree of YAML and Markdown** rooted at `factory.yaml`. We adopt
the observed `v1alpha1` layout (O8) deliberately, so an existing Warp factory
definition ports with minimal edits, and add what self-hosting requires.

```
factory.yaml
agents/
  foreman/
    agent.md
    skills/
      incident-triage/SKILL.md
  reviewer/agent.md
automations/
  labeled-issue/automation.md
runners/
  linux-standard.yaml
routers/
  by-task.yaml
scorers/
  compliance/scorer.md
skills/
  repository-conventions/SKILL.md
webhooks/
  internal-ci.yaml
benchmarks/
  pr-review/
    suite.yaml
    tasks/broken-doc-link.yaml
```

Each resource takes its name from its path: `agents/reviewer/agent.md` defines an agent
named `reviewer`. Directory slugs are stable identity; renaming a `name:` field does not
move a resource.

## Sources of truth

| Mode | Behavior |
| --- | --- |
| **Git** (recommended) | `factoryd` clones a repo+ref+subdirectory, validates, applies on merge to the production ref |
| **Kubernetes** | CRDs are the source; the operator projects them ([10](10-kubernetes-and-helm.md)) |
| **Local** | A directory bind-mounted into `factoryd`, watched for changes — for development |

Applied **atomically**: if validation fails, the factory keeps its last valid definition.
A partially-applied definition is never observable.

## Validation

Three layers, all available before anything reaches the server:

1. **Schema** — JSON Schemas served from `factoryd` at
   `/api/v1/factory-files/schemas/v1alpha1/<document>` (mirroring O8), so editors
   get completion via the `yaml-language-server` modeline. Unknown keys are rejected.
2. **Offline** — `factoryctl validate ./path` resolves cross-file references
   (does `agentDefaults.runner` name a real runner? does an automation target a real
   agent? does every scorer label straddle `passingScore`?) with no server and no login.
3. **Pre-merge** — a `factory/config` status check on PRs to the production ref, reporting
   errors by file and line, or summarizing the resources a valid change creates, updates,
   or deletes. Require it in branch protection.

Markdown frontmatter is not covered by YAML schemas, so layers 2–3 are what actually
validate `agent.md`, `automation.md`, and `scorer.md`.

## `factory.yaml`

```yaml
# yaml-language-server: $schema=https://factory.internal/api/v1/factory-files/schemas/v1alpha1/factory.schema.json
schemaVersion: v1alpha1
name: payments-factory
description: Processes approved work for the payments service
alias: payments                     # @-mention handle for the foreman

codeForges: [GITHUB]
repositories:
  - codeForge: GITHUB
    owner: acme
    name: payments-service
  - codeForge: GITHUB
    owner: acme
    name: payments-docs
    deferred: true                  # attached, prepared only when required

credentialStrategy: EXECUTOR        # EXECUTOR | CREATOR

integrations:
  - type: slack
    slack:
      autoRespondToThreadReplies: true
      allowedUserIds: [U0123ABCDEF] # Slack user ids. Empty or absent denies every Slack user (fail-closed).
      channel: C0123ABCDEF          # Slack channel id outbound threads post to. Optional.
  - type: linear                    # linear and jira are mutually exclusive

secrets: [SENTRY_AUTH_TOKEN]        # granted to every agent
mcpServers:
  sentry:
    url: https://mcp.sentry.internal/sse
    headers:
      Authorization: "Bearer {{SENTRY_MCP_TOKEN}}"   # secret reference, not material

agentDefaults:
  model: auto                       # exactly one of model | harness
  runner: linux-standard
  pool: default                     # NEW — see Extensions
  computerUseModel: claude-5-sonnet-high

# --- NEW in this design ---
inference:
  default:
    provider: anthropic
    credentialRef: ANTHROPIC_API_KEY
  providers:
    anthropic: { kind: anthropic, credentialRef: ANTHROPIC_API_KEY }
    bedrock:   { kind: bedrock, region: us-west-2, roleArn: arn:aws:iam::…:role/factory }
    local:     { kind: openai-compatible, baseUrl: http://vllm.internal:8000/v1 }

pipeline:
  stages: [triage, planning, building, reviewing]
  agents:
    triage: triage
    planning: spec
    building: implementer
    reviewing: reviewer
  skip:
    planning: whenScopeClear         # orchestrator-evaluated predicate
    triage: whenIntakeHasScope
  approvals:
    spec:    { required: true,  approvers: role:editor }
    handoff: { required: true,  approvers: role:user }
    merge:   { required: false }     # enforced by branch protection, not here
  revisionLimit: 3                   # reviewing → building loops before escalation

selfImprovement:
  failedRunThreshold: 25             # 1–50
  staleFailureDays: 7
  reviewerType: admins               # admins | team | custom | none
  reviewerEmails: []

scorerDefaults:
  runner: linux-standard
  model: claude-4-5-haiku

benchmarkRepoSubstitutions:
  - source: { codeForge: GITHUB, owner: acme,      name: payments-service }
    target: { codeForge: GITHUB, owner: acme-bench, name: payments-service }

cloudProviders:
  aws: { roleArn: arn:aws:iam::123456789012:role/factory-runs }
  gcp:
    projectNumber: "123456789012"
    workloadIdentityFederationPoolId: factory-pool
    workloadIdentityFederationProviderId: factory-provider
    serviceAccountEmail: factory-runs@acme.iam.gserviceaccount.com
```

`integrations[].slack.allowedUserIds` lists the Slack user ids that may continue
work by replying in a factory thread. An empty list, and omitting the key, both
deny every Slack user (fail-closed). `integrations[].slack.channel` is the Slack
channel id that outbound threads post to. Omit it when there is no default channel.

**M3 applied.** The loader applies `pipeline:`, routers, and skills. It no
longer warns that those keys are not applied in M2. Automations, scorers, and
webhooks are loaded and validated; their engines are later milestones. Unknown
keys are errors. The YAML JSON Schemas are in
[`schemas/v1alpha1`](../schemas/v1alpha1), and `factoryd` serves them with no
authentication.

### Inheritance

`factory.yaml` → `agentDefaults` → agent → automation, each a **sparse override** of the
one before. Two rules, both taken from observed behavior because they are the ones that
surprise people:

- `model` and `harness` are **mutually exclusive everywhere**. `model: auto` is exact
  shorthand for `harness: {type: oz, model: auto}` — in our system, `type: warp-agent`.
- A list or map set at a lower level **replaces** the inherited one (it does not merge),
  except factory-wide `secrets`/`mcpServers`, which always apply *in addition*. So an
  agent that declares `secrets: [X]` gets `X` plus the factory-wide set — and gets none
  of `agentDefaults.secrets`.

`required_repos` is the exception: an automation's list **extends** the target agent's.

## `agents/<name>/agent.md`

Frontmatter configures; the Markdown body is the agent's instructions.

```markdown
---
description: Adversarial code review. Advisory verdict; humans merge.
agentType: REVIEW          # CUSTOM | FOREMAN | TRIAGE | SPEC | IMPLEMENT | REVIEW | VERIFY
model: auto-genius
spawnableBy: [foreman]     # omit → foreman only; [] → none
credentialStrategy: EXECUTOR
idleTimeoutMinutes: 15     # 1–60; keeps session available for follow-ups
required_repos:
  - { codeForge: GITHUB, owner: acme, name: payments-service }
secrets: [SONAR_TOKEN]
mcpServers:
  sentry: { warpId: null, url: https://mcp.sentry.internal/sse }
runner: linux-standard
pool: default
---

# Review

You review a diff adversarially: treat it as if written by someone you do not
trust, and find the problems that must be corrected before a human accepts the
change. Your verdict is advisory.

You run on a different model than the one that implemented the change, which
reduces correlated blind spots.

## Output
A findings report to the foreman — not comments on the PR. Each finding carries
file and line, severity, the problem, its impact, and the correction. Classify
each as unambiguous or a judgment call.
```

Exactly one agent per factory has `agentType: FOREMAN`; it is the entry point and the
default automation target.

The five default roles, with the division of labor worth preserving: **Foreman**
coordinates and talks to the requester; **Triage** establishes scope and evidence;
**Spec** turns requirements into a plan; **Implement** makes and validates the change;
**Review** independently checks it on a different model. Running review on a different
model than implementation is the one role constraint we encode as a lint warning, because
correlated blind spots are the common failure of single-model pipelines.

## `automations/<name>/automation.md`

```markdown
---
enabled: true
agent: foreman
displayName: Labeled issue intake
triggers:
  - provider: github
    event: issue_labeled
    filter:
      repos: [acme/payments-service]
      labels: [factory-ready]
      authors: { not_in: [dependabot] }
  - provider: schedule
    event: cron_fired
    schedule:
      name: weekday-mornings
      cron: "0 9 * * 1-5"           # 5-field, or @daily / @every 1h
# execution overrides (same keys as an agent)
model: auto
runner: linux-standard
required_repos:
  - { owner: acme, name: payments-docs }
---

Review the labeled issue and decide the next required stage. Preserve the
issue's acceptance criteria and return unresolved product questions to a human.
```

Trigger providers and filter semantics are detailed in
[07](07-integrations-and-webhooks.md). One event may match several automations, and
**each match starts its own run** — so overlapping automations duplicate work, which is
why `factoryctl validate` warns on trigger overlap.

Filters decide *when work starts*, never *what a running agent can reach*. Access comes
from `secrets`, `mcpServers`, and `required_repos`. This distinction is the single most
common security misunderstanding in this class of system, and the validator states it in
the warning text rather than assuming it is known.

## `runners/<name>.yaml`

```yaml
description: Linux runner for payments builds and tests
setupCommands:
  - corepack enable
  - pnpm install --frozen-lockfile
instanceShape:
  vcpus: 4            # powers of two on linux/windows
  memoryGb: 8
platform:
  os: linux           # linux | macos | windows
  arch: x86_64        # x86_64 | aarch64
  linux:
    dockerImage: ghcr.io/acme/payments-ci:2026-10
    registryCredentialSecretName: GHCR_PULL
failureSessionRetentionMinutes: 30
```

A runner selects OS, arch, image, and compute shape. What each field *does* depends on the
execution backend — on the Direct backend the platform fields are ignored because the host
determines the platform, and on Kubernetes the image becomes the task container image.
The full matrix is in [06](06-execution-backends.md).

Runner settings **do not provision capacity.** Worker configuration controls resources,
registry auth, volumes, and placement. A runner that asks for 32 vCPUs on a worker pool of
4-core nodes produces an unschedulable task, not a bigger node — unless the cluster
autoscales, which is why `unschedulable_timeout` exists.

## `routers/<name>.yaml`

Factory-owned model routers. Use the file name anywhere a model is accepted.

```yaml
name: By task
type: prompt          # prompt | complexity
default: claude-4-6-sonnet-high
routing:
  - description: Routine documentation changes
    model: claude-4-5-haiku
  - description: Complex implementation or debugging
    model: claude-4-8-opus-high
```

For `type: complexity`, `routing.easy|medium|hard` map to concrete models. Targets must be
concrete models — not `auto`, not another router. Routers are how you get the documented
"cost per PR from \$80 to \$30" effect without hand-tuning each agent: route the cheap
stages cheap and spend on implementation and review.

## `scorers/<name>/scorer.md`

```markdown
---
name: compliance
description: Did the run deliver what the work item asked for, and only that?
agents:
  - name: foreman
    includeDescendants: true
  - implementer
output: classification
labels:
  - { value: compliant,     description: Delivers the request, scoped to it, score: 1 }
  - { value: partial,       description: Subset, or unrequested scope,        score: 0.5 }
  - { value: non_compliant, description: Does not deliver the request,        score: 0 }
passingScore: 1
samplingRate: 25        # 0 disables automatic scoring
model: claude-4-5-haiku
selfImprovement: true   # failures may feed the improvement loop
---

Compare the pull request against the work item the run was dispatched with.
Judge scope from the issue text, not from the PR's description of itself.
```

Constraint enforced by the validator: at least one label must meet `passingScore` and at
least one must fall below it. A scorer that cannot fail is a scorer that measures nothing.

## `webhooks/<name>.yaml`

```yaml
authMode: token               # token | url_token | signature
signatureScheme: github       # required iff authMode: signature
secretName: INTERNAL_CI_WEBHOOK_SECRET
deliveryIdHeader: X-CI-Run-Id # used to deduplicate retries
enabled: true
```

`secretName` references a managed secret, never material. `authMode` and
`signatureScheme` are immutable once created — changing authentication on a live ingress
URL is a security-relevant event, so the flow is delete-and-recreate under a new name.
Details in [07](07-integrations-and-webhooks.md).

## `benchmarks/<suite>/suite.yaml` and `tasks/<task>.yaml`

```yaml
# suite.yaml
name: Pull request review
description: Compare configurations for the review agent.
agent: reviewer
configurations:                 # 1–6
  - name: Baseline
    agents: [{ agent: reviewer, model: auto }]
  - name: Candidate
    agents: [{ agent: reviewer, harness: { type: claude-code, model: claude-5-opus-high,
                                           auth: { source: managedSecret,
                                                   secretName: ANTHROPIC_API_KEY } } }]
tasks: [broken-doc-link]
```

```yaml
# tasks/broken-doc-link.yaml
title: Fix a broken documentation link
prompt: Find the broken internal documentation link and update it.
successCriteria: The destination resolves and the link text names the destination.
sourceRunId: run_01J…            # provenance; may refer to a deleted run
tags: [docs, low-risk]
startingRepoRefs:
  - github.com:acme/payments-docs@0123456789abcdef0123456789abcdef01234567
```

`startingRepoRefs` must be full 40-character SHAs, never branches or tags — a benchmark
whose starting state can move is not a benchmark. This maps onto the observed
`RepositoryHeadOverride` wire field (O7), including `clone_from` for repo
substitution and `preserve_origin` to keep the original remote identity.

## Skills

A skill is a directory containing `SKILL.md` — not a YAML key.

- `skills/<name>/SKILL.md` — every agent in the factory.
- `agents/<name>/skills/<name>/SKILL.md` — that agent only.

Custom skills **extend** the role and integration baseline rather than replacing it, and a
skill changes what an agent *knows how to do*, never what it can *reach*. Full treatment,
including the three skill sources and the bundled-vs-team-owned attribution problem, is in
[07](07-integrations-and-webhooks.md#skills) — and the improvement loop can propose edits
to a skill exactly as it would to application code
([08](08-measure-and-self-improvement.md)).

## Extensions over the observed schema

Everything above marked NEW, and why:

| Addition | Rationale |
| --- | --- |
| `inference:` | No vendor-managed inference exists in a self-hosted system, so provider selection must be first-class rather than an account setting. Supports BYO endpoints and local models. |
| `pipeline:` | See below. |
| `pool:` (alongside `workerHost`) | Targeting a single worker ID couples the definition to infrastructure. A pool + platform selector lets capacity change without a definition change. `workerHost` stays supported as a pool of one. |
| `approvals.*.approvers` | Warp's gates are policy-by-prompt with no approver model. Gates need an authorization subject to be enforceable. |
| `revisionLimit` | An unbounded reviewing↔building loop is the most expensive failure mode in this architecture. |
| `staleFailureDays` | The 7-day staleness trigger is documented behavior (O12) but not a configurable key; it should be one. |
| `scorerDefaults.model` | Upstream `scorerDefaults` carries only `runner`, `secrets`, and `mcpServers`, so every scorer must repeat its judge model. A default is the obvious missing key; scorers still override it. |

This list is **exhaustive and verified**: running the example tree
([`examples/acme-factory`](../examples/acme-factory)) through Warp's own published
`validate_factory_files.py` — which uses the `warp-server` parser for `v1alpha1` — reports
unknown-field errors for exactly these six keys and nothing else. Every other resource in
the tree (five agents, a three-provider automation, a runner, a scorer, a skill, a webhook)
validates clean against the upstream parser. That is the concrete measure of how
compatible this schema is, and it means a Warp definition ports by addition rather than
translation.

### Deviation: declarative pipeline

Warp documents that stage routing and two of three human gates live in the foreman's
prompt (O11). We move them into `pipeline:`.

**Cost:** less flexibility. A foreman that reasons about routing can handle a shape the
schema did not anticipate.

**Benefit:** the policy becomes reviewable in a diff, checkable by the validator,
enforceable by the orchestrator, and consistent across model upgrades. A gate written in
a prompt is a *request*; a gate in the orchestrator is a *guarantee*. For the specific
case of "must a human approve the spec before code is written", a request is not good
enough — and that is exactly the kind of control an organization self-hosts in order to
hold.

The foreman keeps genuine judgment over *content*: what to ask, what evidence is
sufficient, when to send work back. It loses authority only over *whether a gate can be
skipped*. `skip:` predicates are named, orchestrator-evaluated, and themselves reviewable.
