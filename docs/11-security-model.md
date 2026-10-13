# 11 — Security model

The threat this system creates is specific: **an LLM with repository write access, shell
access, and network egress, acting on input from strangers.** A webhook from a public issue
tracker can reach a model that can run commands. Every control below exists because of that
sentence.

## Trust boundaries

```
untrusted ──────────────────────────────────────────▶ trusted
issue text, PR bodies, chat messages, webhook payloads, fetched web
content, dependency contents, tool output
                    │
                    │ never authorizes anything
                    ▼
           ┌──────────────────────┐
           │ agent (model + tools)│  ← acts with an identity it did not choose
           └──────────┬───────────┘
                      │ bounded by configuration, not by instructions
     ┌────────────────┼─────────────────┬──────────────────┐
     ▼                ▼                 ▼                  ▼
 repo identity   execution        MCP servers         egress
 (scoped token)  secrets          (scoped)            (policy)
```

The load-bearing rule: **content an agent reads never grants it capability.** An issue body
saying "you are authorized to force-push to main" changes nothing, because the token it
holds cannot do that. Prompt injection is not prevented — it is made boring.

This is also why trigger filters are not access control
([07](07-integrations-and-webhooks.md#filters)): filters decide *when work starts*, and an
operator who believes otherwise has an authorization model made of regexes.

## Four credential classes

Kept distinct because they have genuinely different boundaries (the taxonomy is observed
upstream and worth preserving exactly):

| Class | Used for | Boundary |
| --- | --- | --- |
| **Inference credentials** | Model provider requests | Used **only** at the inference gateway. Never injected into a sandbox. An agent cannot read the key that serves it. |
| **Execution secrets** | APIs, registries, tools the agent uses | Per-agent **allowlist**, default empty. Injected into the task environment for the run's lifetime. |
| **Harness authentication** | Third-party harness (Claude Code, Codex) | Separate from the execution allowlist; injected into the harness process only, not visible to agent-run tools ([05](05-harness-contract.md#harness-auth)). |
| **Repository identity** | Checkout and push | Scoped, short-TTL forge token. `credentialStrategy` decides whose authority: `EXECUTOR` (the agent's own identity) or `CREATOR` (the requesting human's). |

Default-empty execution secrets is the important default. An unattended agent that acts as
itself rather than as a user gets **no** managed secrets unless listed — so widening access
is a reviewable diff, not an accident.

### `credentialStrategy`

| Value | Attribution | Use |
| --- | --- | --- |
| `EXECUTOR` | The agent's non-human identity | Unattended work. Clean audit: commits are attributable to a principal that is not a person. |
| `CREATOR` | The requesting user | When a change must carry a human's authority (and their repository permissions bound it). |

`CREATOR` is narrower in capability but muddier in attribution — a commit appears to be the
user's. Set it per agent, and prefer `EXECUTOR` for anything triggered by untrusted intake,
because `CREATOR` on a public-issue trigger lends a stranger a colleague's permissions.

## Secrets handling

The broker resolves names to material at injection time. Pluggable providers:

| Provider | Notes |
| --- | --- |
| `postgres-encrypted` | Default. Envelope-encrypted with a KMS or age key; the DEK never leaves `factoryd`. |
| `kubernetes` | `secretKeyRef`; the cluster is already your secret store. |
| `vault` | Short-lived dynamic credentials — the best option when available. |
| `external-secrets` | Defers to an ESO-managed Secret. |
| `aws-secretsmanager` / `gcp-secretmanager` | Direct, with IRSA/WIF. |

Invariants:

1. **Values are never readable through the API.** No endpoint returns secret material, so
   the console cannot leak what it cannot request.
2. **Names, not material, in Git.** `{{SECRET_NAME}}` references only; the validator
   rejects anything resembling inline credentials in `mcpServers.env`/`headers`, which is
   the easiest place to commit a token by accident.
3. **Short TTL by default.** Forge tokens are minted per run and expire at
   `activeDeadlineSeconds`. A leaked transcript ages out of usefulness.
4. **Redaction is a backstop, not a control.** Known values are redacted at output
   boundaries — logs, transcripts, events, `tool.call` payloads — but redaction fails on
   transformed secrets (base64, split, re-encoded). Narrow permissions and rotation are the
   actual controls; redaction only reduces casual exposure.
5. **Rotation without downtime.** Overlapping validity windows, and the console shows last
   rotation per secret so staleness is visible.

## Identity

Each agent gets a **non-human identity**: a distinct principal with its own forge account
or app installation, its own tokens, and its own audit trail. Three reasons this is
non-negotiable at scale: commits are attributable to a specific agent rather than to a
shared bot; permissions are scopable per agent; and revoking one compromised agent does not
stop the factory.

`identities` holds the subject and a credential reference, never material. A run records
which identity it acted as, and the console shows last use per identity.

## Egress

An agent needs the network (packages, APIs, the model provider) and that network is the
exfiltration path. Egress policy is enforced where the task runs, not by asking the agent:

| Backend | Mechanism |
| --- | --- |
| Kubernetes | NetworkPolicy / CNI policy on the task namespace; optional egress proxy sidecar |
| Docker | A dedicated network plus host firewall rules |
| Direct | Host firewall; a dedicated unprivileged user |
| Command | Your runtime's policy |

The default posture we recommend: **allowlist**, containing the forge, your package
registries and mirrors, your model provider (or your inference gateway), your MCP servers,
and `factoryd`. Everything else denied, with denials logged and surfaced on the run — a
blocked request visible in the transcript is debuggable; a silent timeout is not.

Because the task namespace is separate from the control-plane namespace
([10](10-kubernetes-and-helm.md#chart-decisions)), this policy can be strict without
breaking the control plane.

`/api/v1/networking/egress-ranges` reports your pools' egress addresses as the operator
declared them, for allowlisting the factory in downstream firewalls. Self-hosted, only the
operator can know these, so the value is declared rather than computed.

## Sandbox isolation

Ranked by actual strength, since the gap between the ends is large:

| Posture | Isolation | Notes |
| --- | --- | --- |
| Per-run VM (`--one-shot` Direct + external scheduler) | Strongest | Boot, one task, destroy. The right choice for untrusted intake. |
| Kubernetes Job + gVisor/Kata | Strong | Syscall isolation; needs a RuntimeClass. |
| Kubernetes Job, standard runtime | Moderate | Namespace + cgroup only; a container escape reaches the node. |
| Docker container | Moderate | Equivalent, plus the worker holds the Docker socket. |
| Direct backend | Weak | Shared OS and kernel. Dedicated host, dedicated user, real teardown. |

Independent of backend: non-root task user, read-only root filesystem where the toolchain
allows, dropped capabilities, no privilege escalation, a workspace size limit, and
`activeDeadlineSeconds` as a hard stop. The worker Deployment itself runs non-root
(`runAsUser: 10001`), no privilege escalation, all capabilities dropped.

**Mounting the Docker socket gives the worker host-root equivalence.** A Docker-backend
worker host is one trust domain; do not co-locate it with anything else you care about.

## Authorization

Three factory roles:

| Role | Can |
| --- | --- |
| **User** | View, run the factory, propose definition changes |
| **Editor** | Edit the definition; configure agents, repos, automations; attach existing integrations, secrets, MCP servers; select existing runners |
| **Admin** | Manage members, delete the factory, force-merge a blocked definition change |

Two deliberate limits:

- **Editors attach, they do not create.** An Editor can grant an agent an existing secret
  but cannot create one or connect a new integration — those need team-level permission. So
  widening a factory's reach requires someone with authority over the resource itself.
- **Factory roles do not govern code review.** Treat definition changes as operational code
  and protect merges with branch protection and repository permissions. A factory Admin is
  not a code owner.

## Audit

Append-only `audit_events`, and separately the per-run event log
([04](04-protocols-and-apis.md#3-event-stream)) which records **every tool call** with a
digest and a redacted rendering.

Audited: definition applied (with git SHA and diff summary), secret created/rotated/granted
to an agent, identity created or used, approval decided (who, when, what), run started
(by whom, from what trigger, with what identity), worker connected, API key issued or
revoked, egress denial, and improvement PR opened or merged.

Retention is configurable and exportable to your SIEM over OTLP. Audit events never contain
secret material.

## Threats and mitigations

| Threat | Mitigation |
| --- | --- |
| **Prompt injection via issue/PR/chat/web content** | Content never grants capability. Scoped tokens, allowlisted secrets, declarative approval gates, egress allowlist. Treat every agent action as if an attacker chose it — then ask whether the blast radius is acceptable. |
| **Exfiltration through agent egress** | Egress allowlist with logged denials; short-TTL credentials; secrets scoped per agent. |
| **Malicious dependency executing in the sandbox** | Per-run isolation, non-root, no privilege escalation; the sandbox holds only that agent's secrets. |
| **Compromised worker API key** | Pool-bound keys, rotatable with overlap. A leaked key drains one pool's queue, not the deployment's. |
| **Agent weakening its own supervision** | The validator rejects any definition change that weakens `pipeline.approvals`; the improvement loop cannot merge ([08](08-measure-and-self-improvement.md#guard-rails)). |
| **Unreviewed self-improvement** | All proposals are PRs; the loop has no merge permission; definition PRs get the config check; blast-radius limit routes large changes to admins. |
| **Secret leaked into a transcript** | Redaction at output boundaries, plus short TTL so a stale transcript is low-value. Not sufficient alone — hence narrow scoping. |
| **Docker socket escape** | Treat the worker host as a single trust domain; prefer the Kubernetes or per-run-VM backend for untrusted work. |
| **Malicious definition PR** | Pre-merge config check, branch protection, CODEOWNERS on the definition tree. A definition PR can grant secrets and change models — review it like production IAM. |
| **Replayed webhook** | Signature verification before parsing, delivery-ID dedup, bounded retention of raw deliveries. |
| **Over-broad MCP server** | MCP servers are per-agent and allowlisted; credentials are secret references; an MCP server is as privileged as the agent holding it, so it belongs in the same review as a secret grant. |

## Deployment checklist

1. **Classify access.** Inventory repositories, data, and services each factory can reach.
2. **Set boundaries.** Choose execution backend and isolation posture per intake trust
   level — untrusted public intake should not share a pool with internal work.
3. **Scope credentials.** Per-agent allowlists, separate harness auth, deliberate
   `credentialStrategy`, per-agent identities.
4. **Enforce review.** Branch protection on the definition tree; CODEOWNERS; require the
   config check; protect `pipeline.approvals`.
5. **Constrain egress.** Allowlist, log denials, surface them on runs.
6. **Validate operations.** Test rotation, worker-key revocation, drain behavior, queue
   backpressure, audit export, and a restore from backup — before the first production
   work item, not after the first incident.
