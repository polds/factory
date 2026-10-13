---
name: tests-run
description: Did the implementer run the relevant tests before opening the PR?
agents:
  - implementer
output: classification
labels:
  - value: tests_run
    description: The transcript contains a test command and its actual result output.
    score: 1
  - value: tests_claimed
    description: The PR claims tests pass but the transcript shows no test run.
    score: 0
  - value: tests_skipped
    description: No test command was run and none was claimed.
    score: 0
passingScore: 1
samplingRate: 25
model: claude-4-5-haiku
selfImprovement: true
---

Evaluate whether the agent ran the tests that exercise the behavior it changed,
before opening the pull request.

Return `tests_run` only when the transcript contains both a test command and its
output. A pull request description asserting that tests pass is **not**
evidence — when you see that assertion without a corresponding command in the
transcript, return `tests_claimed`, which is the more serious failure because it
is misleading rather than merely incomplete.

Return exactly one declared label.
