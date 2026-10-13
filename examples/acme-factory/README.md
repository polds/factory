# Example: acme-payments

A complete factory definition tree exercising every resource type. Copy it,
replace the placeholders, register it.

```
factory.yaml                                  root: repos, defaults, pipeline, inference
agents/
  foreman/agent.md                            FOREMAN   — coordinates, talks to requester
  triage/agent.md                             TRIAGE    — scope and evidence
  spec/agent.md                               SPEC      — the plan (human-approved)
  implementer/agent.md                        IMPLEMENT — the change + evidence
  reviewer/agent.md                           REVIEW    — adversarial, different model
automations/
  issue-labeled/automation.md                 GitHub label + Slack mention + CI webhook
runners/
  linux-standard.yaml                         4 vCPU / 8 GB linux sandbox
scorers/
  tests-run/scorer.md                         did the implementer actually run tests?
skills/
  repository-conventions/SKILL.md             factory-wide procedure
webhooks/
  ci-failure.yaml                             authenticated ingress for CI
```

## Make it yours

| Placeholder | Replace with |
| --- | --- |
| `acme/payments-service`, `acme/payments-docs` | your repositories |
| `ghcr.io/acme/payments-ci:2026-10` | your CI image (glibc-based, **not** Alpine) |
| `REPLACE_WITH_WEBHOOK_UID` in the automation | the UID assigned when the webhook is created |
| `ANTHROPIC_API_KEY`, `SENTRY_MCP_TOKEN`, `SENTRY_AUTH_TOKEN`, `NPM_TOKEN`, `GHCR_PULL`, `CI_WEBHOOK_SECRET` | managed secret names in your deployment |
| `eng-payments` | your Slack channel |
| `https://mcp.sentry.internal/sse`, `http://vllm.internal:8000/v1` | your endpoints |

Everything else in the tree is real and intended to be used as written.

## Validate before you push

```bash
factoryctl validate ./examples/acme-factory
```

Offline, no server, no login. It checks schema, required fields, and cross-file
references — that `agentDefaults.runner` names a real runner, that the
automation targets a real agent, that there is exactly one `FOREMAN`, and that
the scorer has labels on both sides of `passingScore`.

## What this example demonstrates

- **Model diversity by stage.** Triage and review on Sonnet, spec and
  implementation on Opus, scoring on Haiku. Review deliberately runs on a
  different model than implementation to decorrelate blind spots — and the cheap
  stages stay cheap, which is where cost per PR actually comes from.
- **Declarative gates.** `pipeline.approvals.spec` blocks the Building stage in
  the orchestrator, not in a prompt the model could be talked out of.
- **Empty secrets by default.** `factory.yaml` grants nothing; each agent opts
  into exactly what it needs. Widening access is a reviewable diff.
- **Three intake paths, one pipeline.** A GitHub label, a Slack mention, and a
  CI webhook all produce work items that flow through the same stages.
- **A scorer that catches a lie.** `tests-run` distinguishes "no tests" from
  "claimed tests that were never run", and the second is scored as the worse
  failure. With `selfImprovement: true`, a recurring pattern becomes a pull
  request against the skill or the agent instructions.
