# M3 — pipeline and blocking spec gate

`spike/m3/run.sh` starts the laptop compose stack with `deploy/m3-factory` and drives one work item through triage, planning, the spec gate, building, and reviewing.

```bash
go build -o bin/factoryctl ./cmd/factoryctl
./bin/factoryctl validate deploy/m3-factory
MODE=offline ./spike/m3/run.sh
```

Offline is the default. It ends with `M3 OFFLINE PASS`. The model router sends each stage model id to its own model-stub. The script checks that `pending_gate` is `spec`, that `factoryctl approvals list` is non-empty, and that no building run exists until `factoryctl approvals decide`. It then checks four child runs for distinct `model_id` values, parent linkage, and `work_item_id`.

`MODE=live` is for a machine with `deploy/.env` and a GitHub token. It is not run in this environment. The script calls `approvals decide` only when `APPROVAL_DECISION=approve`. Otherwise it prints the approval id and waits. It ends with `M3 LIVE PASS` and the pull request URL.

## Docker Desktop

Task containers are not on the compose network. Set `FACTORY_TASK_SERVER_URL` to `http://host.docker.internal:8080`. Point `OPENAI_BASE_URL` at that same host. This script does not rewrite a loopback model URL. On Linux, an empty `FACTORY_TASK_SERVER_URL` becomes the bridge gateway.

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

The four model ids are written into a copy of `deploy/m3-factory` before factoryd boots. Offline keeps the ids in that tree (`m3-triage`, `m3-planning`, `m3-building`, `m3-reviewing`).

Scratch is `spike/m3/.work/`, which is gitignored.
