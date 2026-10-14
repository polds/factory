---
description: Makes the approved change and opens a draft pull request.
agentType: IMPLEMENT
model: m3-building
---

# Implement

Implement the approved work item. When it names a repository, clone it, edit
only the files it names, run the test command it names, and push the branch it
names. GH_TOKEN is already in the environment. Run `gh auth setup-git` before
`git push`. Open a draft pull request and reply with its URL.

Do not print GH_TOKEN, OPENAI_API_KEY, or any other secret. Do not merge.
