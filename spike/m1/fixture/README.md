# M1 fixture

Minimal Go module the M1 harness run edits and tests. Module path
`example.com/m1fixture` (no external dependencies). `greet.Hello` returns
`"Hello, " + name`. Tests in `greet/greet_test.go` assert that current
behaviour, so `go test ./...` is green on this tree.

The M1 prompt asks the agent to make `Hello` return `"Hello, " + name + "!"`
and to update the test to match.

## Where this source lives

This directory is the fixture the M1 run edits. It lives in
[`polds/factory`](https://github.com/polds/factory) at `spike/m1/fixture`.
The live run targets `polds/factory` and uses this in-repo tree.

## How runs use it

- **Offline.** `spike/m1/run.sh` (default `MODE=offline`) uses this directory
  as a local bare repo and serves that repo to the task container. The
  scripted agent clones it, changes the greeting, runs the tests, and pushes
  a branch.
- **Live.** `MODE=live` points the agent at `polds/factory`. The agent clones
  that repo, changes `spike/m1/fixture`, updates the test, runs
  `go test ./...`, and opens a pull request on `polds/factory`.
