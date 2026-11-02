---
description: Opens one pull request, or holds until the run is deleted.
agentType: IMPLEMENT
model: m7-agent
runner: local
pool: standard
maxTurns: 8
secrets:
  - GH_TOKEN
  - DEFINITION_GIT_URL
  - GITHUB_API_BASE
---

# Implement

Do the task in the prompt. Do not print tokens, API keys, or other secret values.

1. Clone the repository in DEFINITION_GIT_URL.
5. Open a pull request against the run base ref at GITHUB_API_BASE using GH_TOKEN.
