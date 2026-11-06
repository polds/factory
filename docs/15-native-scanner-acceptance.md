# Native scanner acceptance

M10-A is the offline acceptance slice. A passing offline run is synthetic. It is not scanner acceptance. The actual backend is not determinable from this tree. `paidProbeAllowed` stays false. INV-GATEWAY, INV-SECURITY-GATE, and INV-ATTESTATION stay GAP until M10-G.

The in-repo scanner runner remains the public stub (`plugin=stub`, `fetch=forbidden`). There is no vendor, owner, or version for a private scanner. The Claude Code adapter stays on the help fixture. This runbook does not fetch a private image and does not dispatch a live workflow.

`docs/13-defined-factory-contracts.md` and `docs/14-native-coordination.md` still describe the pre-M10-A gap text for local CLI, ART, and the bridge. Those files are outside this slice. The matrix rows below are the current record.

## What the offline driver proves

`MODE=offline ./spike/m10/run.sh` uses one embedded definition, `deploy/native-scan/fixtures/definition.json`:

- two nonoverlapping schedule slots, `2026-11-02T00:00:00Z` and `2026-11-09T00:00:00Z`, both pinned to default SHA `bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`
- one pull-request input scan pinned to base `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa` and head `cccccccccccccccccccccccccccccccccccccccc`
- zero findings with coverage `recorded` is success
- a planted finding `synthetic-finding` is retrieved the same way
- Linear configuration is refused before any test runs, and the acceptance path makes no Linear call
- the definition requires `report` only. The driver does not open a pull request
- report and coverage are read from `GET /api/v1/factory/scan/invocations/{id}/result` after the task workspace is removed. The response carries `coverageStatus` and `findingsCount`. A missing bearer token is HTTP 403 with an empty body
- `retention/<id>/report.json` is the file `commit` and `PublishCandidate` write. A publication failure leaves that file absent. `ExpireRetention` removes a file once `publishedAt` is 168 hours old
- publication rejects symlink, `..`, oversize, and `CANARY-SYNTHETIC` bodies and stores nothing for those failures
- cancel removes the item workspace, writes no report, and creates no pull request. Cleanup rejects an id that escapes the item root
- a crash before commit retries once. A second retry does not write a second report
- `factory-outbound-bridge` is disabled. Outage queues the report notice, suppresses echo and replay, and leaves `sendCount` at 0. The native report is still readable

Retention is 168 hours. Each item has its own workspace mount and its own art mount. There is no shared writable checkout and no shared `/art` root. The compose sample is `deploy/native-scan/compose.yaml`. It sets `network_mode: none` so that file is not a live schedule. The offline driver does not apply it.

Local CLI mode is `local_cli`. It does not require Linear or a Factory task ref. Synthetic native briefs use Factory `task_ref` (an `ntask_` id). `linear_issue` on that brief is null. The scanner baseline that still says `linear_issue` is `internal/normalize/linear/doc.go`. The matrix row INV-TASK-REF points at `SyntheticNativeBrief` in `internal/normalize/linear/native_brief.go`. Its producer label remains M9-T. The test owner is M10-A. INV-ART points at `sharedArtRoot` in `ValidateSample`.

## Commands

Offline acceptance, from a clean shell with no `LINEAR_*` variables:

```bash
MODE=offline ./spike/m10/run.sh
```

The driver prints `m10 offline PASS` when the contract check, the offline preflight, the synthetic security checks, the package tests, and the acceptance summary have all succeeded. The summary line `m10 bridge sendCount=0` is printed by `FormatSummary`. `m10 synthetic-security separate` is printed before those checks, and those checks do not start a scanner or a model.

Mode and preflight coverage, still with no network calls:

```bash
MODE=offline ./spike/m10/run_test.sh
```

That script prints `m10 run_test PASS`.

Contract witnesses:

```bash
python3 docs/contracts/defined-factory/validate.py
```

That command prints `CONTRACT FIXTURES PASS` when the witnesses match. It does not start factoryd.

The live preflight is fail-closed. It names every check, prints `model_probe count=0`, prints `m10 preflight live blocked`, and exits 1:

```bash
bash spike/m10/preflight.sh live
```

`MODE=live ./spike/m10/run.sh` prints `m10 supports offline only` and exits 1. It does not run the acceptance path. A set `LINEAR_API_KEY` on the offline path prints `m10 linear-off: refusing Linear config` and exits 1. The value is not printed.

## Preflight checks

Every independent check runs before any model probe:

| Check | Refusal |
| --- | --- |
| `backend_attestation_present` | attestation absent |
| `backend_attestation_fresh` | missing or older than 24h |
| `code_sha` | missing or different from the pinned synthetic SHA |
| `image_digest` | missing or different from the stub digest |
| `adapter_digest` | missing or different from the pinned adapter digest |
| `capability` | capability missing |
| `credential` | credential name missing |
| `grant` | grant missing |
| `execute_target_disabled` | `execute_target` enabled |
| `mounts_secure` | insecure mounts |
| `private_image_fetch` | private image URL set, or the stub pin is not `plugin=stub` / `fetch=forbidden` |
| `paid_probe_closed` | passes while the actual-backend formula is false |

`paidProbeAllowed` is `g14ActualBackend && g18ActualBackend && gatewayCapProven` and either Docker host-firewall enforcement or Kubernetes CNI proof. The offline and live witnesses leave that false. A unit test can force the formula with synthetic flags and a counter. That counter does not dial a model, and the preflight commands do not use that witness.

The synthetic secure case marks attestation present so the checker can be told apart from the live gate. That flag is not an M10-G attestation and does not make the actual backend determinable.

Synthetic security checks are `scripts/ci/scan-security-check.sh`, invoked by the offline driver before acceptance and not as a model startup step.

## Live gate

`.github/workflows/m10-live.yml` is `workflow_dispatch` only. Job `gate` uses environment `m10-live` and refuses to continue unless the `confirm` input is `m10-live`. The following step is `bash spike/m10/preflight.sh live`. It does not read secrets, pull an image, deploy a schedule, or start a model. Do not dispatch it.

Blocked live rows. No run id exists for this slice:

| Row | State | Run ID |
| --- | --- | --- |
| Backend attestation | absent | none |
| `paidProbeAllowed` | false | none |
| Schedule deployment against a live host | not in the workflow | none |
| `m10-live` dispatch | not dispatched | none |
| Actual-backend G14 / G18 | GAP until M10-G | none |
| Private scanner image | fetch forbidden | none |

## Matrix

| Invariant | State |
| --- | --- |
| INV-TASK-REF | Factory `task_ref`, `linear_issue` null. Producer remains M9-T |
| INV-LOCAL-CLI | closed on the local CLI in this repo |
| INV-ART | closed on per-item mounts |
| INV-BRIDGE | closed on `factory-outbound-bridge`, `sendCount` 0 |
| INV-GATEWAY | GAP, M10-G |
| INV-SECURITY-GATE | GAP, M10-G |
| INV-ATTESTATION | GAP, M10-G |
