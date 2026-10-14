# M3 — pipeline and blocking spec gate

`spike/m3/run.sh` starts the laptop compose stack with `deploy/m3-factory` and drives one work item through triage, planning, the spec gate, building, and reviewing.

```bash
go build -o bin/factoryctl ./cmd/factoryctl
./bin/factoryctl validate deploy/m3-factory
MODE=offline ./spike/m3/run.sh
```

Offline is the default. It ends with `M3 OFFLINE PASS`. The model router sends each stage model id to its own model-stub. The script checks that `pending_gate` is `spec`, that `factoryctl approvals list` is non-empty, and that no building run exists until `factoryctl approvals decide`. It then checks four child runs for distinct `model_id` values, parent linkage, and `work_item_id`.

## Mac and Docker Desktop (live)

Task containers are not on the compose network. On Docker Desktop, including an Apple Silicon Mac, set these before `MODE=live`. `run.sh` reads `deploy/.env` when that file exists. A non-empty shell variable wins. This script does not rewrite a loopback model URL.

| Variable | Docker Desktop value |
| --- | --- |
| `FACTORY_TASK_SERVER_URL` | `http://host.docker.internal:8080` |
| `OPENAI_BASE_URL` | An OpenAI-compatible base URL whose host is `host.docker.internal`, the host a task container uses to reach a model proxy on the Mac. `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL` is the same value. |
| `APPROVAL_DECISION` | `approve` decides the spec gate. Unset prints the approval id and waits. Any other value is refused. |

`GH_TOKEN` and four distinct model ids (`M3_MODEL_TRIAGE`, `M3_MODEL_PLANNING`, `M3_MODEL_BUILDING`, `M3_MODEL_REVIEWING`) are also required. The script prints those ids as `models: triage=…`. `GH_TOKEN` stays out of the log. On Linux, an empty `FACTORY_TASK_SERVER_URL` becomes the bridge gateway.

Peter ran this on an Apple Silicon Mac with Docker Desktop on 2026-10-14. `FACTORY_TASK_SERVER_URL` and `OPENAI_BASE_URL` were already set as above. The command was:

```bash
MODE=live APPROVAL_DECISION=approve ./spike/m3/run.sh
```

That command needs the live credentials in `deploy/.env` or the shell. CI and this environment do not have them, so it is not re-run here. The laptop result is in [`docs/12-build-sequence.md`](../../docs/12-build-sequence.md). It ends with `M3 LIVE PASS` and the pull request URL. The driver log asserts `four runs linked parent to child on distinct model ids` and prints the four ids on the `models: triage=…` line.

## Live variables

`run.sh` reads `deploy/.env` when the file exists. A non-empty shell variable wins. Empty assignments are ignored and values are not executed.

| Shell name | Also accepted | Required | Meaning |
| --- | --- | --- | --- |
| `OPENAI_BASE_URL` | `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL` | yes | Endpoint reachable from a task container |
| `GH_TOKEN` | `FACTORY_SECRET_GH_TOKEN` | yes | Token that can push and open a draft PR |
| `M3_MODEL_TRIAGE` | | yes | Model id for the triage agent |
| `M3_MODEL_PLANNING` | | yes | Model id for the spec agent |
| `M3_MODEL_BUILDING` | | yes | Model id for the implementer |
| `M3_MODEL_REVIEWING` | | yes | Model id for the reviewer; must differ from the other three |
| `OPENAI_API_KEY` | `FACTORY_INFERENCE_OPENAI_API_KEY` | no | Sent only when set |
| `M3_REPO` | | no | Default `polds/factory` |
| `APPROVAL_DECISION` | | no | `approve` decides the spec gate; any other value is refused |
| `FACTORY_TASK_SERVER_URL` | | on Docker Desktop | `http://host.docker.internal:8080` |

The four model ids are written into a copy of `deploy/m3-factory` before factoryd boots. Offline keeps the ids in that tree (`m3-triage`, `m3-planning`, `m3-building`, `m3-reviewing`). Those offline ids are not the live run's model ids.

Scratch is `spike/m3/.work/`, which is gitignored.
