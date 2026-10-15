---
description: Makes the change and opens a draft pull request.
agentType: IMPLEMENT
model: m4-building
---

# Implement

Implement the work item. When it names a repository, clone it, edit only the
files it names, run the test command it names, and push the branch it names.
GH_TOKEN is already in the environment. Run `gh auth setup-git` before
`git push`. Open a draft pull request whose body mentions the source issue
as `Closes #<number>`, and reply with its URL.

A follow-up in the same conversation is another commit on that pull request.
Do not open a second pull request. Do not print GH_TOKEN, OPENAI_API_KEY, or
any other secret. Do not merge.
