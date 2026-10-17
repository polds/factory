---
enabled: true
agent: triage
displayName: CI opened
triggers:
  - provider: webhook
    event: received
    filter:
      payload:
        action: [opened]
---

Start work when a signed delivery says opened. Do not print tokens or secret values.
