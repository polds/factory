---
enabled: true
agent: triage
displayName: Issue intake
triggers:
  - provider: github
    event: issue_created
    filter:
      repos: [polds/m5]
---

Open the issue in the pipeline. Do not print tokens or secret values.
