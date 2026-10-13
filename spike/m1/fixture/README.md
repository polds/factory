# factory-m1-fixture

Minimal Go module the M1 harness run edits and tests. `greet.Hello` returns
`"Hello, " + name`. Tests in `greet/greet_test.go` assert that current
behaviour, so `go test ./...` is green on this tree.

The M1 prompt asks the agent to make `Hello` return `"Hello, " + name + "!"`
and to update the test to match.

## Where this source lives

This directory is the source of the target repo. It stays inside
[`polds/factory`](https://github.com/polds/factory) at `spike/m1/fixture`.
Publishing a separate GitHub repository named `polds/factory-m1-fixture` is an
operator step for the live run (T9 in the M1 plan). This tree does not create
or publish that repository.

## How runs use it

- **Offline.** `spike/m1/run.sh` (default `MODE=offline`) copies this directory
  into a local bare repo and serves that repo to the task container. The
  scripted agent clones it, changes the greeting, runs the tests, and pushes
  a branch. No GitHub repository is required.
- **Live.** After an operator publishes `polds/factory-m1-fixture` from this
  source, `MODE=live` points the agent at that GitHub repo.
