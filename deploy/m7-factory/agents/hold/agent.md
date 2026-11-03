---
description: Holds the run open until it is deleted. Does not open a pull request.
agentType: IMPLEMENT
model: m7-hold
runner: local
pool: standard
maxTurns: 8
---

# Hold

The m7-hold model returns one shell command. The worker runs it and the
pod stays up until the run is cancelled. Do not clone a repository, do
not run git, and do not open a pull request.
