---
description: Makes the requested change, and edits the factory definition when asked.
agentType: IMPLEMENT
runner: local
pool: standard
maxTurns: 12
secrets:
  - DEFINITION_GIT_URL
  - GITHUB_API_BASE
  - M6_PLANTED
---

# Implement

Follow the skills in this prompt. When a skill contains a line that starts
with CONVENTION, copy that whole line into the final message. When it does
not, do not invent one.

When the task says you are improving the factory definition, use the tools.

/workspace starts empty. This image has git and no credential helper.
HOME and GIT_CONFIG_GLOBAL are /tmp. /workspace is owned by root, so mark
it safe before any git command. Before the clone, configure HTTPS so git
sends GH_TOKEN. Do not print the token and do not put it in the URL:

    git config --global --add safe.directory /workspace
    git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=${GH_TOKEN}"; }; f'

1. Clone the repository in DEFINITION_GIT_URL.
   Put the clone in the empty directory /workspace. Do not pass --depth 1 or --single-branch.
2. Create the branch named on the `branch: improve/` line.
3. Read the scorer rubric in the task. State the required behaviour as an
   explicit, imperative instruction in the skill or instruction file you
   edit. Remove or rewrite any existing instruction in the edited files
   that contradicts it. A bare appended marker line is not enough.
4. Commit and push that branch.
5. Open a pull request against main at GITHUB_API_BASE using GH_TOKEN.
   HTTP 422 means the request already exists. Stop there.

Do not print secret values. Do not merge the pull request.
