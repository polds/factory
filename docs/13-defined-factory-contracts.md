# 13 — Defined-factory contracts

Contract version `defined-factory/1`. Baseline `85812997d8e81b0209b678e583a7738b9d066966`.

This document freezes the invocation, result, native-coordination, budget, harness, and scanner-acceptance contracts for later workers. It does not implement them. The JSON under `docs/contracts/defined-factory/` is the witness set. `validate.py` checks those witnesses. It does not start factoryd, open a network connection, read credentials, or call a model.

Run this from a checkout:

```text
python3 docs/contracts/defined-factory/validate.py
```

On success the command prints a fixture count line, a self-test count line, then these two lines:

```text
CONTRACT FIXTURES PASS
validates examples, not the real implementation
```

A witness that violates its rule is an asserted failure. The command exits non-zero when a legal witness fails or an illegal witness is accepted. Passing this command is contract consistency only. It is not a production API, UI, native-runtime, or scanner result.

The four personas in the witnesses are a synthetic coordinator, lead, and two specialists. They are not Peter's Atum roster. `atumRoster` is false on `INV-TREE`.

## Closed choices

| Topic | Decision |
|---|---|
| Public entry | `POST /api/v1/factory/{uid}/invocations` |
| In-process entry | `Invoke(ctx, principal, request) (Receipt, error)` |
| Adapter call | API, intake, and the scheduler call `Invoke` in process |
| Idempotency scope | factory uid, profile name (empty when omitted), source occurrence key |
| Canonical body | sorted-key JSON of revision, profile, target, inputs, occurrence key, correlation, intent, and priority |
| Replay | same key and same canonical digest returns the original receipt, definition digest, and target SHA |
| Conflict | same key and a different digest returns HTTP 409 `invocation_conflict` and writes nothing |
| Correlation | thread, conversation, and reply binding are not the occurrence key |
| Priority | human invoke is `interactive`; a schedule occurrence is `batch` |
| Intent | explicit `new`, `continue`, or `wake`; a different intent is a different canonical body |
| Target pin | pull-request head and base, or the resolved ref SHA, stored on the receipt; checkout uses that SHA |
| Repository-free | a definition that does not require a repository mints no forge token |
| Profile | `{name}` only; budget, code, credentials, and grants in the profile are `profile_rejected` |
| Principal | taken from the authenticated context, never from the body |
| Schedule identity | `schedule:{scheduleId}:slot:{dueSlot}` using the scheduled due time |
| Overlap | `forbid` writes `skipped_overlap`, starts no run, and does not continue the busy item |
| Legacy schedules | `occurrenceMode: legacy_continue` remains the default |
| Leader recovery | the receipt row lives in Postgres; a second leader observes it; a crash before commit inserts once |
| `requires` | `pullRequest` and `report` only; `report` ships with storage and a terminal validator in the same change |
| Empty `requires` | demands neither a pull request nor a report |
| Missing pull request | existing `no_work_product` |
| Missing report | `report_missing` |
| Input pull request | a target field; it does not satisfy `requires: [pullRequest]` |
| Zero findings | `findingsCount: 0` with coverage present is success |
| Publication | lexical path, symlink, 1 MiB report, 16 MiB artifact, secret scan |
| Hierarchy | optional for legacy; native mode freezes one root, acyclic, connected, maxDepth and absoluteMaxDepth from the definition, each at most 8 |
| Delegation | direct manager: delegator is `reportsTo` and is in `spawnableBy`; ancestor shortcut stays off |
| Lineage | `parent_run_id` stays the stage predecessor; a delegation edge has its own id |
| Stage advance | only the stage owner; a child with the same stage string does not advance |
| Reporting | `reportsTo` does not grant secrets or repository access |
| Identities | task, assignee, blockers, activation, and execution are different ids |
| Work-item id | existing `task_` prefix; native tasks use `ntask_` |
| Ledger | one per-invocation sequence row, locked `FOR UPDATE`, committing event, state, and outbox together |
| Not invocation order | `run_events.seq` (per run) and lazily assigned `factory_seq` |
| SSE | `Last-Event-ID` / `from_seq` stay on `run_events.seq` |
| Inbox body | 8192 bytes |
| Page | opaque cursor, limit 1..100, default 50 |
| Ack | explicit, idempotent, task- and principal-scoped, after durable apply |
| Read | read and long-poll do not ack |
| Wake coalesce | many intents for one task become one durable wake record |
| Claim | one fence holder; the loser gets `claim_fenced` |
| Capacity | managers park and release the slot before the child is claimed |
| Completed wake | new linked run; prior terminal evidence stays immutable |
| Retry and park continuation | new execution of the same run |
| Cancelled task | no automatic resurrection; board resume is authorized and audited |
| Child failure | `fail_parent` or `block_parent`; no silent replacement |
| Join | no synthesis while a required child is live or unresolved |
| Money | integer cents |
| Usage key | `(callId, attemptId)`, including failed attempts |
| Rollup | each leaf once at invocation, lead, agent, and factory |
| Reservation | one transaction, ancestor rows locked in scope-id order |
| Alert | 80 percent, one alert per scope episode; a refund below the line opens a new episode |
| Missing usage | no new admission; overshoot bound equals outstanding reservations |
| Ceiling overshoot | attributed spend beyond the ceiling is 0 while `spent + reserved <= ceiling` |
| Late charge | excess over the reservation is quarantined and the scope pauses |
| Ceiling override | an audited `api_key:ops` row may raise one scope ceiling; the raise spends 0 cents |
| Gateway | per-engagement cap at most the ceiling; a shared key does not guarantee the ceiling |
| Policy cap witness | 2500 cents in the budget witnesses; this is a configured cap, not a measured scan cost |
| Tokens | server-issued, attenuated to the child; specialists cannot dispatch |
| Steer | only when the capability was negotiated; otherwise input stays pending |
| Workflow | main-loop only; internal workers stay in-session |
| Secrets | name allowlist, lookup on every injection, unsafe and missing refs fail closed |
| Rotation | running process credentials stay until restart; redaction retains old and new canaries |
| Isolation | adapter-level boundary; environment-only isolation is not accepted |
| Forge token for scanner tasks | `contents: read`, `metadata: read` |
| Scanner native mode | `task_ref`; `linear_issue` is null |
| Local CLI | remains available without Linear and without a task ref |
| ART | per-item mount; no shared writable root |
| Bridge | one owner, `factory-outbound-bridge`, disabled unless a later explicit send policy authorizes it |
| Security gate | G14, G18, and a gateway cap on the actual backend before any paid probe |
| Attestation | binds backend, config, code, image, and adapter; a mismatch invalidates it |

## Identifiers

New ids use the existing Crockford form: prefix plus 26 characters from `0123456789ABCDEFGHJKMNPQRSTVWXYZ`. This task does not edit `store.NewID`. The implementing worker adds the prefix to the known set in the same change that inserts the row.

| Prefix | Object |
|---|---|
| `inv_` | invocation |
| `task_` | existing work item |
| `run_` | logical run |
| `exec_` | execution attempt |
| `ntask_` | native task |
| `nmsg_` | inbox message |
| `ndlv_` | native delivery |
| `nact_` | activation |
| `nres_` | budget reservation |
| `ntok_` | token id (not secret material) |
| `pol_` | frozen policy snapshot |
| `sch_` | existing schedule |
| `conv_` | existing conversation |

## Invocation

```text
POST /api/v1/factory/{uid}/invocations
Invoke(ctx context.Context, principal Principal, req Request) (Receipt, error)
```

`principal` is the authenticated API key, verified Slack user, GitHub App installation, schedule actor, or webhook actor. The JSON body cannot carry it.

`Request` fields are `definitionRevision` (default: current applied), `profile` (`{name}`), `target` (`{repository, ref}` or `{repository, pullRequest}`), `inputs`, `sourceOccurrenceKey`, `correlation` (`conversationId`, `replyTo`, `threadRef`), `intent`, and `priority`. Unknown body fields are `input_invalid`.

The receipt carries `invocationId`, `workItemId`, factory uid, definition id/revision/digest, profile name/digest, policy digest, pinned SHAs, state, the occurrence key, the canonical digest, and links:

```text
GET /api/v1/factory/{uid}/invocations/{invocationId}
GET /api/v1/factory/{uid}/invocations/{invocationId}/result
```

HTTP 201 creates a receipt. HTTP 200 returns the original receipt on replay. HTTP 409 is `invocation_conflict`. HTTP 403 is `principal_rejected`, `profile_rejected`, `repository_rejected`, or `grant_rejected`. HTTP 400 is `input_invalid`. Rejected authz writes nothing.

Profile injection and the `execute_target` grant are GAP. `WeakensApprovals` compares approval gates: a removed gate, `required` true to false, or widened approvers. It does not read a profile, a budget ceiling, credentials, or `execute_target`.

`POST /api/v1/factory/{uid}/tasks` still creates a work item. `POST /api/v1/agent/run` still starts a bare run. Neither route becomes `Invoke`.

One transaction inserts the invocation. The unique key is `(factory_uid, profile_name, source_occurrence_key)`. On conflict the transaction compares the canonical digest, returns the stored receipt or the 409, and inserts nothing else.

## Schedules

The due slot is the scheduled time already placed in the cron delivery id (`internal/server/m4_cron.go`). Fresh mode stops using the constant schedule thread ref as the work-item identity (`internal/automation/engine.go` `OpenWorkItem`). Overlap `forbid` locks the schedule row, and when another invocation for that schedule is non-terminal it inserts a skipped receipt with `countsAsCompletedScan: false`.

A late delivery of the same slot replays. After commit, the next elected leader reads the receipt. A crash before commit leaves the unique key free, and the next tick inserts one row.

## Results

`requires: [pullRequest]` keeps today's fail-closed `no_work_product` check (`internal/server/terminal.go`). `checkRequires` (`internal/definition/validate.go`) accepts only the `pullRequest` token. A report product does not exist at this baseline. `requires: [report]` is added by M8-I only together with durable storage, authorization, and a terminal validator. `findings` and `artifacts` are not requirement values. Findings are a typed field on the report, and zero is valid.

Publication order is authorization, lexical path, symlink check, size, then secret scan. Failure publishes nothing. Unauthorized `GET` of a result is 403 with an empty body. The durable result row survives deletion of the task workspace. Path, symlink, size, per-item ART, and result retention are GAP: `archive` writes `runs/{run}/{exec}/events.ndjson`, and `defsync.rejectSymlinks` covers a definition tree.

## Native tree and stage owner

Legacy factories stay valid with hierarchy enforcement off. Native mode stores the effective tree and delegation policy on the invocation (`pol_` digest) so a later definition reload cannot change that engagement's manager or grants.

`advanceAfterTerminal` (`internal/server/work.go`) currently treats any run whose stage equals the work item's stage as the stage result. Native mode adds a stage-owner bit. Child completion updates coordination state and does not call that advance.

`spawnableBy` is parsed and defaulted to `foreman` today (`internal/definition/inherit.go`) and is not enforced. M9-N enforces the intersection with `reportsTo`.

## Ledger, inbox, ack

`run_events.seq` is `MAX(seq)+1` inside one run (`internal/store/events.go`). `factory_seq` is assigned on a later read, in `gid` order, among rows that have already committed (`internal/store/migrations/0009_run_lifecycle_events.sql`). A completion that commits first can receive an earlier `factory_seq` than a result that commits later. That is not result-before-completion.

M9-T adds `invocation_ledgers(invocation_id, ledger_seq)`. `AppendLedger` locks that row, allocates the next sequence, and writes the canonical event, the state change, and the outbox row in the same transaction. Result-before-completion means the completion transaction observes the result event already committed, or writes the result event earlier in the same transaction. Per-run SSE cursors stay on `run_events.seq`.

Inbox read projects the canonical event for one assignee. Ack is a separate idempotent insert on `(delivery_id, principal, task_id)` after the recipient has applied the message. A dropped HTTP response leaves the delivery unacked. Redelivery uses a new delivery id and the same message id and applies once.

The contract body cap is 8192 bytes. `DeliveryBodyCap` (`internal/store/types.go`) is `256 << 10`. `maxBodyBytes` (`internal/api/handler.go`) is `1 << 20`. Neither constant is 8192.

## Activation and budgets

`ClaimActivation` updates one task row from pending or parked to running and increments the fence. Park clears the active execution and releases capacity in the same transaction as the blocker insert. The capacity witness has one worker slot: the coordinator and the lead park before their children run, and only the coordinator's completion advances the pipeline.

`coordination.ReserveAncestors` locks factory, invocation, lead, and agent scope rows in scope-id order and holds the cents in that transaction. `coordination.SettleUsage` is unique on `(call_id, attempt_id)`, including a failed attempt. `ApplyBudgetDelta` emits one 80 percent alert per scope episode. `RunUsage` (`internal/store/m5_read.go`) still adds `CostCents` for every `usage` event on one run. It is not the admission gate.

The 2500 cent figures in the budget witnesses are the configured policy cap used as an example. They are not a measured cost and not a spend authorization.

A shared gateway key plus a spawn refusal does not cap calls that are already in flight. Paid work needs a per-engagement gateway allocation at most the ceiling. That allocation is GAP until M10-G (INV-GATEWAY). `secrets.FailClosedAllocator` fails closed and does not set `guaranteesCeiling`. A shared key does not guarantee the ceiling.

`coordination.AuthorizeCeiling` raises one scope ceiling in the same transaction as an audit row for `api_key:ops`, spends 0 cents, and rejects an unaudited raise. Cancel and pause (`CancelSubtree`, late-charge pause) leave in-flight reservations on the ceiling until the releasing transaction commits. A late charge above the reservation is quarantined and the scope pauses. Attributed spend beyond the ceiling stays 0 while `spent + reserved` is within the ceiling.

## Legacy defaults

| Surface | Default |
|---|---|
| `POST /api/v1/factory/{uid}/tasks` | creates a work item |
| `POST /api/v1/agent/run` | starts a bare run |
| Schedule `occurrenceMode` | `legacy_continue` (one work item) |
| Overlap `forbid` | opt-in on fresh occurrence mode |
| `requires` omitted or empty | neither a pull request nor a report |
| `requires: [pullRequest]` | fail closed with `no_work_product` |
| `reportsTo` omitted | valid while native hierarchy is off |
| `metadata.reportsTo` | not a native edge |
| `parent_run_id` | stage predecessor |
| Linear | not required for invocation or local CLI |

## Method signatures

```text
invocation.Service.Invoke(ctx, principal, req) (Receipt, error)
store.InsertInvocation unique (factory_uid, profile_name, source_occurrence_key)
store.AppendLedger locks invocation_ledgers FOR UPDATE; writes event, state, outbox
store.AckDelivery idempotent on (deliveryID, principal, taskID)
store.ReadInbox does not ack
coordination.ClaimActivation
coordination.Delegate
coordination.ReserveAncestors
coordination.SettleUsage unique (callID, attemptID)
coordination.AuthorizeCeiling
coordination.Park
coordination.Join
coordination.CancelSubtree
coordination.BoardResume
coordination.WakeCompleted
coordination.RetryExecution
harness.IssueRunToken
harness.Negotiate
harness.ProveCredentialBoundary
forge.MintReadToken
secrets.Resolver.Resolve(ctx, name, injectionID)
scanner.NativeBrief.taskRef
outbound.Enqueue
attestation.Gate
attestation.Bind
```

## Harness and secrets

```text
IssueRunToken(ctx, scope, attenuation) (Token, error)
Resolver.Resolve(ctx, name, injectionID) (Handle, error)
```

Token material redacts under every format verb. Witnesses carry `ntok_` ids only. Child grants are a subset of the parent. Specialist tokens omit `roster.dispatch`. A token for one invocation does not authorize another.

`Resolve` reads the mounted file on every injection, accepts only allowlisted names, and rejects missing refs and paths that escape the mount. `subPath` is not a rotation mechanism. A running CLI keeps the credential it already holds. Redaction covers the previous and current canary values, including MCP and integration output. Witness canaries are the labels `CANARY-OLD`, `CANARY-NEW`, and `CANARY-SYNTHETIC`.

The credential-boundary witness requires an adapter-level result of `isolated`, or `liveBlocked` when that proof is absent. A passing child-environment check does not satisfy the witness.

Scanner minting does not use `DefaultPermissions` (`internal/forge/github/token.go`), which grants write on contents, pull requests, issues, and checks. Scanner tasks get contents read and metadata read.

Untrusted tasks get no Docker socket, no writable foreign checkout, and no broad secret mount.

## Scanner, bridge, and attestation

Native mode replaces a mandatory Linear issue with `taskRef`. Local CLI mode is GAP in this repository: `internal/harness/run.go` returns `config_invalid` when the named harness adapter is absent from the binary, and that check does not mention Linear or a task ref. The contract keeps local CLI available without either. Per-item ART mounts and durable report rows are GAP, as are the publication checks above.

The scanner bridge is GAP. `automation.followUp` sets `StageBuilding` and starts another run. `internal/outbound` posts Slack, GitHub, and Linear status cards for a work item. Neither is `factory-outbound-bridge`. The contract still has one owner, stays disabled until a send is authorized, queues reporting on outage, and leaves `sendCount` at 0.

```text
paidProbeAllowed = g14ActualBackend && g18ActualBackend && gatewayCapProven
                   && ((backend == docker && hostFirewallEnforced)
                       || (backend == k8s && cniProved))
```

The paid-probe gate is GAP. `assign.go` fills `DockerImage` and `SidecarImage` on a task assignment. `docs/11-security-model.md` describes NetworkPolicy or CNI on Kubernetes and a dedicated Docker network plus host firewall. Those lines do not deny traffic or open a probe. The witness stays closed: Helm render and kind may pass, host firewall and CNI stay unproved, and `paidProbeAllowed` is false.

Attestation binding is GAP. `mintGitHubToken` calls `InstallationToken` with `DefaultPermissions` and does not record digests. The witness is valid only when `bound` and `observed` are equal, including `secretPolicy`. A secret-policy mismatch invalidates it. This witness does not claim that a gate has been run.

## Error codes

| Code | When |
|---|---|
| `input_invalid` | closed-schema rejection, including an injected body principal or profile field |
| `invocation_conflict` | same occurrence key, different canonical body |
| `principal_rejected` | authenticated principal is not allowed |
| `profile_rejected` | profile carries budget, code, credentials, or grants |
| `repository_rejected` | target repository is outside the allowlist |
| `grant_rejected` | `execute_target` or another grant is not held |
| `report_missing` | `requires: [report]` and no durable report |
| `no_work_product` | existing missing output pull request |
| `result_unauthorized` | result read outside the caller scope |
| `result_path_rejected` | absolute path, backslash, or `..` |
| `result_symlink_rejected` | path is a symlink |
| `result_oversize` | report over 1 MiB or artifact over 16 MiB |
| `secret_publication_rejected` | secret scan hit |
| `delegation_denied` | delegator is not the direct manager |
| `depth_exceeded` | depth above the frozen max |
| `spawn_denied` | spawn after subtree cancel or pause |
| `claim_fenced` | lost activation or inbox claim |
| `budget_exceeded` | ancestor ceiling would break |
| `usage_late_over_reservation` | reported cents above the reservation |
| `secret_ref_rejected` | name, path, or missing ref fails closed |
| `cancelled_no_resurrect` | wake of a cancelled task without board resume |

## Producer map

M8-C owns this contract and the witnesses. The producer column is the worker that implements the method. The consumer column is who may call it after CP0. Test owner is the worker whose production test must keep the witness true.

The map is [`docs/contracts/defined-factory/matrix.json`](contracts/defined-factory/matrix.json). A row cites a baseline file only when those lines are the behavior the decision is about. A row with `"status": "gap"` has no such code in this tree. `validate.py` checks code anchors against the file and rejects a gap row that still cites one.

## Limitations

- Witnesses are examples. They do not exercise factoryd, Postgres, the console, a model, or a scanner.
- Claude Code protocol flags, resume, and steer support are pinned by M9-H against the CLI it selects. These witnesses set `negotiatedSteer: false` and do not claim a CLI version.
- G14, G18, host firewall, and CNI are gate inputs. The security witness records the closed state. It is not a passed probe.
- The 2500 cent cap is policy input, not a measured Atum scan cost.
- No migration, route, or store method is added here. Implementing workers own the next migration number at their base.
- Peter's roster, repository, image, and scanner-repository changes stay outside this contract.
