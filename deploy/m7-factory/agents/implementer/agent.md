---
description: Opens one pull request on the seeded base branch.
agentType: IMPLEMENT
model: m7-agent
runner: local
pool: standard
maxTurns: 8
requires:
  - pullRequest
secrets:
  - GH_TOKEN
  - DEFINITION_GIT_URL
  - GITHUB_API_BASE
---

# Implement

Do the task in the prompt. Do not print tokens, API keys, or other secret values.

/workspace starts empty. This image has git and no credential helper.
HOME and GIT_CONFIG_GLOBAL are /tmp. /workspace is owned by root, so mark
it safe before any git command. Before the clone, configure HTTPS so git
sends GH_TOKEN. Do not print the token and do not put it in the URL:

    git config --global --add safe.directory /workspace
    git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=${GH_TOKEN}"; }; f'

Put the clone in the empty directory /workspace. Do not pass --depth 1 or --single-branch.
Check out the ref named in the clone step. Push the head branch named in the pull-request step.
The pull request base is that named ref. It is not the repository default branch.

1. Clone the repository in DEFINITION_GIT_URL.
5. Open a pull request against the run base ref at GITHUB_API_BASE using GH_TOKEN.

Create the pull request with POST $GITHUB_API_BASE/repos/<owner>/<repo>/pulls
and header Authorization: Bearer $GH_TOKEN. Read owner and repo from
DEFINITION_GIT_URL. The JSON body uses the head and base branches named above.
Print only the https html_url from the response.
Then request a review from {{M7_REVIEWER}}:
POST $GITHUB_API_BASE/repos/<owner>/<repo>/pulls/<number>/requested_reviewers
with body {"reviewers":["{{M7_REVIEWER}}"]}.
If that request fails, leave the pull request open and keep the printed url.
Do not merge the pull request.
