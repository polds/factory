# 07 — Integrations, webhooks, and skills

Work enters a factory from chat, issue trackers, code forges, webhooks, schedules, the
API, or MCP. Every path converges on the same pipeline:

```
provider delivery ──▶ ingress ──▶ normalize ──▶ trigger match ──▶ automation ──▶ run
                      (verify,    (canonical    (filters)         (agent +
                       dedup)      event)                          overrides)
```

## Ingress

One endpoint per provider, plus one per declared custom webhook:

```
POST /api/v1/ingress/github
POST /api/v1/ingress/gitlab
POST /api/v1/ingress/slack
POST /api/v1/ingress/linear
POST /api/v1/ingress/jira
POST /api/v1/ingress/teams
POST /api/v1/ingress/azure-devops
POST /api/v1/webhooks/{webhook_uid}            # authMode: token | signature
POST /api/v1/webhooks/u/{url_token}            # authMode: url_token
```

Ingress responsibilities, in order, and all four matter:

1. **Verify** the provider signature (HMAC per provider scheme) *before parsing*. Parse
   after verify, never before.
2. **Deduplicate** on the provider's delivery ID. Providers retry aggressively; a
   duplicated delivery means a duplicated agent run, which is both expensive and visibly
   wrong in a Slack thread.
3. **Persist** the raw delivery (bounded, redacted) for replay and debugging, then
   acknowledge fast — within the provider's timeout, which is 3 s for Slack.
4. **Dispatch** matching asynchronously. Trigger evaluation never happens on the request
   path.

Deliveries that match nothing are retained briefly and surfaced in the console as
"unmatched deliveries", because the alternative — silence — makes a mistyped filter
indistinguishable from a provider outage.

## Normalized events

Each provider event normalizes to:

```jsonc
{
  "provider": "github",
  "event": "issue_labeled",
  "delivery_id": "…",
  "occurred_at": "…",
  "actor": { "provider_id": "…", "handle": "…", "factory_user_id": "…|null" },
  "subject": {                      // what the event is about
    "kind": "issue", "id": "…", "number": 412, "title": "…", "url": "…",
    "repo": { "code_forge": "GITHUB", "owner": "acme", "name": "payments-service" },
    "labels": ["factory-ready"], "branch": null, "base_branch": null
  },
  "thread": { "kind": "github_issue", "external_ref": "acme/payments-service#412" },
  "raw": { /* provider payload, for webhook payload filters */ }
}
```

`thread.external_ref` is the idempotency key that ties a conversation to a run via
`/agent/run-by-external-reference` ([04](04-protocols-and-apis.md)). It is why a second
comment on an issue continues the existing agent conversation rather than starting a
competing one.

## Triggers

Providers and their events (adopted from observed behavior so definitions port):

| Provider | Events |
| --- | --- |
| `github` | `issue_created`, `issue_assigned`, `issue_labeled`, `issue_commented`, `issue_mentioned`, `pull_request_opened`, `_closed`, `_merged`, `_reopened`, `_ready`, `_labeled`, `_assigned`, `_commented`, `_mentioned`, `_synchronized`, `_review_requested`, `_review_submitted`, `_enqueued`, `_dequeued`, `push`, `check_run_rerequested`, `check_suite_completed`, `check_suite_rerequested`, `workflow_run_completed` |
| `gitlab` | `merge_request`, `push`, `bot_mentioned` |
| `azure_devops` | `work_item_created`, `_assigned`, `_labeled`, `_mentioned`, `pull_request_created`, `_updated`, `_closed`, `_merged`, `_commented`, `_mentioned`, `push` |
| `slack` | `app_mention`, `message_posted`, `message_dm`, `message_im`, `message_mpim`, `reaction_added`, `member_joined_channel` |
| `teams` | `app_mention`, `message_posted` |
| `linear` | `issue_created`, `issue_assigned`, `issue_labeled`, `issue_state_changed`, `comment_created`, `agent_session_created` |
| `jira` | `issue_created`, `issue_labeled`, `status_changed`, `agent_session_created` |
| `schedule` | `cron_fired` |
| `webhook` | `received` |
| `factory` | `work_item_stage_changed` |

`factory`/`work_item_stage_changed` is what makes pipelines composable: a stage transition
is itself an event, so one factory can react to another, or a notification automation can
fire on handoff without polling.

### Filters

```yaml
triggers:
  - provider: github
    event: pull_request_opened
    filter:
      repos: [acme/payments-service]
      base_branches: [main]
      paths: ["services/payments/**"]
      authors: { not_in: [dependabot, renovate] }
      labels: { in: [needs-review] }
```

Semantics, exactly as observed, because getting these backwards is the common bug:

- **Across keys: AND.** Every filter must match.
- **Within a key: OR.** Any listed value matches.
- **Omitted key: matches everything.**
- Matcher objects accept `in` and `not_in`; webhook `payload` also accepts `exists`.
- Name-based aliases (`teams`, `projects`, `states`, `issues`, `channels`, `users`,
  `itemUsers`) resolve to IDs; an alias and its canonical `_ids` key are mutually
  exclusive.

Per-provider filter keys: GitHub — `repos`, `branches`, `base_branches`, `paths`,
`labels`, `authors`, `assignees`, `mentioned`, `reviewers`, `reviewer_teams`,
`review_states`, `workflows`, `conclusions`, `keywords`, `pr_numbers`. Slack —
`channel_ids`/`channels`, `user_ids`/`users`, `keywords`, `emojis`,
`item_user_ids`/`itemUsers`. Linear — `team_ids`/`teams`, `project_ids`/`projects`,
`state_ids`/`states`, `issue_ids`/`issues`, `labels`, `assignee_ids`, `creator_ids`,
`mentioned_user_ids`, `keywords`. Jira — `project_keys`, `labels`, `status_ids`,
`keywords`. GitLab — `repos`, `actions`, `branches`, `base_branches`, `mentioned`.
Azure DevOps — `repos`, `source_repos`, `branches`, `base_branches`, `work_item_types`,
`labels`, `authors`, `assignees`, `mentioned`. Factory — `stages`.

> **Filters decide when work starts, not what a running agent can reach.**
> Access is `secrets`, `mcpServers`, `required_repos`, and the run's identity. A filter is
> not an authorization boundary, and the validator says so in its warning text.

### Webhook payload filters

```yaml
triggers:
  - provider: webhook
    event: received
    filter:
      webhook_ids: [whk_01J…]        # exactly one; `in` only; one trigger per webhook
      payload:
        action: [opened, reopened]
        pull_request:
          draft: [false]
        sender:
          type: { not_in: [Bot] }
        deployment:
          url: { exists: true }
```

Bounded on purpose: nesting ≤ 10 levels, ≤ 64 leaf fields, ≤ 32 values per set. String
matching is exact and case-sensitive. A missing key **fails `in`** and **passes `not_in`** —
the one rule worth memorizing, since it determines whether an absent field opts in or out.
Arrays of objects match if any element matches.

### Schedules

```yaml
triggers:
  - provider: schedule
    event: cron_fired
    schedule:
      name: weekly-dep-audit      # at most one schedule per automation may omit a name
      cron: "0 6 * * 1"           # 5-field UTC, or @daily / @hourly / @every 1h
```

Changing `cron` updates the schedule in place; changing `name` replaces it. Schedules are
evaluated by a leader-elected ticker with a catch-up window, so a control-plane restart
does not silently skip a nightly run — and a missed window fires once, not once per
missed interval.

Scheduled runs are `batch` priority ([02](02-architecture.md#scheduler)) so a nightly
audit never delays someone waiting in Slack.

## Webhook authentication

```yaml
authMode: signature            # token | url_token | signature
signatureScheme: github        # github | gitlab | stripe | sentry | pagerduty |
                               # vercel | standard_webhooks
secretName: CI_WEBHOOK_SECRET
deliveryIdHeader: X-CI-Run-Id
enabled: true
```

| Mode | Mechanism | Use |
| --- | --- | --- |
| `token` | `Authorization: Bearer <secret>` | Default; senders you control |
| `url_token` | Secret embedded in the URL path | Senders that cannot set headers |
| `signature` | Provider HMAC scheme | Third-party SaaS with a signing secret |

`url_token` secrets appear in URLs, so they are restricted to `A-Za-z0-9-._~`, and must be
treated as credentials in logs and referrers — prefer `token` or `signature` whenever the
sender allows it.

`deliveryIdHeader` names the sender's dedup header (≤ 64 chars, valid HTTP header name).
Credential-bearing headers (`Authorization`, `Cookie`, provider signature headers) are
rejected as dedup sources, since using one as a cache key would store it.

`authMode` and `signatureScheme` are **immutable** after creation; rotating the mechanism
means creating a new webhook under a new name and repointing the sender. `enabled: false`
stops deliveries immediately without deleting — which is also how you pre-create a webhook
before the provider has issued its signing secret.

## Outbound

Chat and tracker integrations are bidirectional. The factory posts: run started, stage
changed, question asked (with the answer path), PR opened with evidence, findings summary,
handoff. Rules learned from how these systems feel in practice:

- **One thread per work item.** Progress is edited into existing messages where the
  platform allows, rather than appended as a stream of new ones.
- **Replies continue work.** With `autoRespondToThreadReplies` enabled, an eligible plain
  reply in an existing factory thread continues the conversation without another mention.
- **Attribution is explicit.** Every post names the agent and model, and links the run.
- **Answer in place.** A question posted to Slack is answerable in Slack; the inbox
  ([09](09-web-console.md)) is a second surface for the same gate, not the only one.

Outbound webhooks (to your own systems) post signed `run.*` and `work_item.*` events with
`standard_webhooks` signatures and at-least-once delivery with exponential retry.

## Skills

A skill gives agents a repeatable, version-controlled procedure. Three sources compose,
and custom skills **extend** rather than replace the baseline:

1. **Role and integration skills** — baseline procedures from an agent's role and the
   connected providers. Every default agent gets forge skills; the foreman also gets a
   chat skill; the chosen tracker adds its own. These are not files in your definition.
2. **Platform-bundled skills** — capabilities the deployment ships (schema lookup,
   definition validation, factory MCP, run operation). Stable IDs, versioned with the
   release.
3. **Custom file-based skills** — yours, in the definition tree:
   - `skills/<name>/SKILL.md` → every agent.
   - `agents/<name>/skills/<name>/SKILL.md` → that agent only.

```markdown
---
name: repository-conventions
description: How this repo expects changes to be made, tested, and described.
---

## Before you finish
1. `pnpm lint && pnpm test --filter <changed-package>`
2. Changesets: any user-visible change needs `pnpm changeset`.
3. PR title follows Conventional Commits.

## Recurring failure modes in this repo
- Tests mocking the HTTP layer instead of the client (hides serialization bugs).
- Migrations without a down path.
```

**A skill changes what an agent knows how to do, never what it can reach.** To widen
access you change `secrets`, `mcpServers`, or `required_repos` — not a skill. Custom agents
have *no* baseline skills, so a custom agent with no skill is an agent with only its prompt.

### Attribution

Skill loads surface as `skill.read` events with either `bundled_skill_id` (a platform
skill) or `skill_path` (a file). **M6 applied.** This harness does not emit
`skill.read`. factoryd passes the skill body in `FACTORY_SKILL_TEXT`
(`internal/server/assign.go`) and the harness appends that text to the system
prompt (`internal/harness/run.go`). When measuring whether your team's skills are being used,
exclude bundled IDs, and count a path as team-owned only when it sits under a root you
manage. A path alone does not identify an owner — a point worth encoding in the metric
rather than discovering later when the dashboard reports suspiciously high adoption.

Skills participate in self-improvement: a recurring scored failure can produce a PR editing
the responsible skill, the same way it would edit application code
([08](08-measure-and-self-improvement.md)).
