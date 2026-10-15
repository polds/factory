---
enabled: true
agent: implementer
displayName: Labeled issue intake
required_repos:
  - { codeForge: GITHUB, owner: polds, name: factory }
triggers:
  - provider: github
    event: issue_labeled
    filter:
      repos: [polds/factory]
      labels: [factory-ready]
      authors:
        not_in: [dependabot]
---

Take the labeled issue through the factory. Preserve the issue's acceptance
criteria. Do not print tokens or API keys.
