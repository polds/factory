# spike/m7

Exit driver for M7. It builds a pinned kind cluster, installs
`deploy/helm/factory` in operator mode, applies Factory `m7`, and proves one
pull request plus delete-to-cancel.

```bash
MODE=offline ./spike/m7/run.sh
```

`MODE=live` and `MODE=live-ci` need the same secrets as `m7-live.yml`
(`M1_OPENAI_BASE_URL`, `M1_OPENAI_API_KEY`, `M6_GITHUB_APP_ID`,
`M6_GITHUB_APP_PRIVATE_KEY` or `FACTORY_GITHUB_APP_PRIVATE_KEY_FILE`, and
`FACTORY_REVIEWERS_ADMINS`). Do not dispatch `m7-live.yml` from an agent.
`MODE=offline ./spike/m7/run.sh --cleanup-remote` is refused.

## Modes

| MODE | What it proves |
| --- | --- |
| `offline` | Kind, fake model router, local forge. The pass line starts `M7 OFFLINE PASS wall`. |
| `live` | Real model and a real PR on `polds/factory-m7-target`. The pass line starts `M7 LIVE PASS wall`. |
| `live-ci` | Same as live. `m7-live.yml` sets this. The pass line starts `M7 LIVE PASS wall`. |

Offline and live share the pull-request assertion (`status.prUrls` equals the
forge URL) and the delete-to-cancel check. Live runs remote preflight before
image builds.

## Workflow inputs

`m7-live.yml` is `workflow_dispatch` only.

| Input | Default | Env on the driver |
| --- | --- | --- |
| `model` | `nous/claude-sonnet-5.5` | `M7_LIVE_MODEL` |
| `target_git_url` | `https://github.com/polds/factory-m7-target` | `M7_DEFINITION_GIT_URL` |
| `reviewer` | required | `FACTORY_REVIEWERS_ADMINS` |
| `pr_wait_seconds` | `480` | `M7_PR_WAIT_SECONDS` |
| `cancel_proof` | `false` | `M7_CANCEL_PROOF` |

`cancel_proof` is also set on the `if: cancelled()` cleanup step, and nowhere
else. `M7_CANCEL_PROOF_HOLD_SECONDS` defaults to 600 and is capped at 600.
The workflow does not pass it; a shorter value is for the offline tests.

## Cancel proof

With `M7_CANCEL_PROOF=true` the driver still records the pull request and
checks `status.prUrls`. It then prints a line that starts with
`m7 cancel-proof: holding`, then the seconds, `s pr=`, and the pull request
URL, and waits with `sleep` in the background plus `wait`, so SIGINT is
handled during the hold. The orchestrator cancels after that line. If the
hold ends with no signal, the driver prints
`m7 cancel-proof: hold expired without cancel` and exits non-zero. It does
not print a `PASS` line.

Who deletes the remote state:

| Path | Remote ledger | Kind cluster |
| --- | --- | --- |
| Normal exit, or hold expiry | in-driver cleanup | deleted |
| INT/TERM, `cancel_proof` false | in-driver budgeted cleanup, then exit 130/143 | deleted, capped |
| INT/TERM, `cancel_proof` true | left for the workflow step | deleted, capped |
| `if: cancelled()` step | `--cleanup-remote` | already gone or left with the runner |

The signal handler ignores a second INT/TERM and exits 130 or 143. It does
not resume the script. GitHub sends SIGTERM about 7.5s after SIGINT and
SIGKILL about 2.5s after that. The handler ignores SIGTERM, so it has about
10s. Cancel-proof skips remote cleanup. Kind delete on a signal is capped
at 6s and the handler targets under 8s. The offline INT test finishes in
under 2s.

The ledger stays at `spike/m7/.work/remote-ledger.json`. The signal path
does not delete `.work`. The workflow cleanup step reads that file. A
cancel-proof cleanup summary has non-zero deleted counts and `left=0`, for
example `m7 cleanup: refs deleted=3 left=0 branches deleted=3 left=0 prs deleted=1 left=0`.

## Evidence

`collect_evidence` writes `spike/m7/.work/evidence/`: `crs.yaml`, `events.txt`,
`pods-factory.txt`, `pods-runs.txt`, `factoryd.log`, `operator.log`,
`worker.log`, `router.log`, `task-pods/`, `task-describe/`, and
`run-events/`. `hold-run.json` is the factoryd detail for the hold run.
`m7-live.yml` redacts that directory and uploads it as `m7-evidence`.

## Tests

`bash spike/m7/run_test.sh` does not start kind. It covers secrets, the
cancel window, and the cancel-proof hold (INT 130, TERM 143, expiry, normal
INT cleanup, and `--cleanup-remote` after a cancel-proof INT).
