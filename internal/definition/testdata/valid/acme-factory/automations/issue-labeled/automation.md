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
      authors:
        not_in: [dependabot, renovate]
  - provider: slack
    event: app_mention
    filter:
      channels: [eng-payments]
  - provider: webhook
    event: received
    filter:
      webhook_ids: [REPLACE_WITH_WEBHOOK_UID]
      payload:
        status: [failed]
        branch: [main]
---

Take this request through the factory.

Preserve the requester's acceptance criteria verbatim — do not paraphrase them
into something easier to satisfy. Return unresolved product questions to a
human rather than deciding them yourself.

If the request arrived from the CI webhook, it is a failure on `main`: treat it
as urgent, start from the failing job's logs, and report the cause before
proposing a fix.
