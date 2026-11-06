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
| Hierarchy | optional for legacy; native mode freezes one root, acyclic, connected, proof depth 2, absolute max 8 |
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

`POST /api/v1/factory/{uid}/tasks` still creates a work item. `POST /api/v1/agent/run` still starts a bare run. Neither route becomes `Invoke`.

One transaction inserts the invocation. The unique key is `(factory_uid, profile_name, source_occurrence_key)`. On conflict the transaction compares the canonical digest, returns the stored receipt or the 409, and inserts nothing else.

## Schedules

The due slot is the scheduled time already placed in the cron delivery id (`internal/server/m4_cron.go`). Fresh mode stops using the constant schedule thread ref as the work-item identity (`internal/automation/engine.go` `OpenWorkItem`). Overlap `forbid` locks the schedule row, and when another invocation for that schedule is non-terminal it inserts a skipped receipt with `countsAsCompletedScan: false`.

A late delivery of the same slot replays. After commit, the next elected leader reads the receipt. A crash before commit leaves the unique key free, and the next tick inserts one row.

## Results

`requires: [pullRequest]` keeps today's fail-closed `no_work_product` check (`internal/server/terminal.go`). `requires: [report]` is added by M8-I only together with durable storage, authorization, and a terminal validator. `findings` and `artifacts` are not requirement values. Findings are a typed field on the report, and zero is valid.

Publication order is authorization, lexical path, symlink check, size, then secret scan. Failure publishes nothing. Unauthorized `GET` of a result is 403 with an empty body. The durable result row survives deletion of the task workspace.

## Native tree and stage owner

Legacy factories stay valid with hierarchy enforcement off. Native mode stores the effective tree and delegation policy on the invocation (`pol_` digest) so a later definition reload cannot change that engagement's manager or grants.

`advanceAfterTerminal` (`internal/server/work.go`) currently treats any run whose stage equals the work item's stage as the stage result. Native mode adds a stage-owner bit. Child completion updates coordination state and does not call that advance.

`spawnableBy` is parsed and defaulted to `foreman` today (`internal/definition/inherit.go`) and is not enforced. M9-N enforces the intersection with `reportsTo`.

## Ledger, inbox, ack

`run_events.seq` is `MAX(seq)+1` inside one run (`internal/store/events.go`). `factory_seq` is assigned on a later read, in `gid` order, among rows that have already committed (`internal/store/migrations/0009_run_lifecycle_events.sql`). A completion that commits first can receive an earlier `factory_seq` than a result that commits later. That is not result-before-completion.

M9-T adds `invocation_ledgers(invocation_id, ledger_seq)`. `AppendLedger` locks that row, allocates the next sequence, and writes the canonical event, the state change, and the outbox row in the same transaction. Result-before-completion means the completion transaction observes the result event already committed, or writes the result event earlier in the same transaction. Per-run SSE cursors stay on `run_events.seq`.

Inbox read projects the canonical event for one assignee. Ack is a separate idempotent insert on `(delivery_id, principal, task_id)` after the recipient has applied the message. A dropped HTTP response leaves the delivery unacked. Redelivery uses a new delivery id and the same message id and applies once.

## Activation and budgets

`ClaimActivation` updates one task row from pending or parked to running and increments the fence. Park clears the active execution and releases capacity in the same transaction as the blocker insert. The capacity witness has one worker slot: the coordinator and the lead park before their children run, and only the coordinator's completion advances the pipeline.

`ReserveAncestors` locks factory, invocation, lead, and agent scope rows in scope-id order and inserts the reservation only when every ancestor has `spent + reserved + requested <= ceiling`. Settle moves the used cents from reserved to spent once and refunds the rest. Usage insert is unique on `(call_id, attempt_id)`.

The 2500 cent figures in the budget witnesses are the configured policy cap used as an example. They are not a measured cost and not a spend authorization.

A shared gateway key plus a spawn refusal does not cap calls that are already in flight. Paid work needs a per-engagement gateway allocation at most the ceiling. Until that allocation is proved, `guaranteesCeiling` is false.

`AuthorizeCeiling` raises one scope ceiling only in the same transaction as an audit row for `api_key:ops`. The raise spends 0 cents. An unaudited raise is rejected. Cancel and pause (`CancelSubtree`, late-charge pause) leave in-flight reservations on the ceiling until the releasing transaction commits.

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

Native mode replaces a mandatory Linear issue with `taskRef`. Local CLI mode stays available and does not require either. Each item mounts only its own workspace. Report and coverage refs are durable result rows with recorded retention and provenance (scanner commit, image digest, plugin digest, target SHAs).

The optional bridge has one owner. It is disabled in the acceptance witness. An outage queues reporting and leaves execution running. Echo and replay suppression are on. `sendCount` stays 0 while `sendAuthorized` is false.

```text
paidProbeAllowed = g14ActualBackend && g18ActualBackend && gatewayCapProven
                   && ((backend == docker && hostFirewallEnforced)
                       || (backend == k8s && cniProved))
```

The witness records Helm render and kind success with host firewall and CNI unproved, and `paidProbeAllowed` false. A Helm render is not a Docker-host attestation. Kind is not a substitute for the selected host's firewall.

The attestation's `valid` bit is true only when the observed backend id and config, code, image, and adapter digests equal the bound set. Changing any of those, or the secret policy, invalidates it. LIVE requires the same digest in that deployment session. This witness does not claim that a gate has been run.

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

M8-C owns this contract and the witnesses. The producer column is the worker that implements the method. The consumer column is who may call it after CP0. Test owner is the worker whose production test must keep the witness true. Baseline file and line are checked by `validate.py` against this checkout.

The decision for each invariant, the seam anchor, and both vectors live in `docs/contracts/defined-factory/matrix.json`.

## Decisions and seams

- `INV-REQUEST-SHAPE` (internal/api/handler.go:75-86): Add Invoke beside POST /tasks and POST /agent/run. The body has no principal and the profile is a name only.
- `INV-REPLAY` (internal/store/automation.go:177-181): Same key and same canonical digest returns the original definition digest and target SHA. A reload or a moved ref does not rewrite the receipt.
- `INV-CONFLICT` (internal/store/automation.go:177-181): Same key and a different canonical digest returns 409 invocation_conflict and writes nothing.
- `INV-INTENT-CONFLICT` (internal/store/automation.go:177-181): Intent is part of the canonical body. new and wake on one occurrence key conflict.
- `INV-PRIORITY` (internal/server/m4_cron.go:61-83): Human invoke is interactive. A schedule occurrence is batch.
- `INV-CORRELATION` (internal/automation/engine.go:170-174): A thread ref correlates replies. It is not the occurrence identity.
- `INV-PR-DISTINCT` (internal/server/m4_ingress.go:295-310): Two deliberate reviews of one pull request are two invocations.
- `INV-PIN` (internal/server/m4_ingress.go:295-310): Resolve the pull request head and base, or the ref, once. Checkout uses the pinned SHA, not the SHA written in the prompt.
- `INV-NO-FORGE` (internal/server/backend.go:104-112): A definition that does not require a repository mints no forge token.
- `INV-PRINCIPAL` (internal/api/handler.go:138-145): The principal comes from the authenticated context. A rejected principal writes nothing.
- `INV-PROFILE` (internal/definition/guardrails.go:83-90): A profile selects reviewed defaults. Injected budget, code, credentials, or grants are profile_rejected with no writes.
- `INV-REPOSITORY` (internal/forge/github/token.go:64-70): A repository outside the allowlist is repository_rejected and writes nothing.
- `INV-GRANT` (internal/definition/guardrails.go:83-90): execute_target is a grant. Requesting it without the grant is grant_rejected and writes nothing.
- `INV-SCHEDULE-KEY` (internal/server/m4_cron.go:76-82): The occurrence key is schedule id plus the due slot. Delivery time is not part of the key.
- `INV-SCHEDULE-REPLAY` (internal/server/m4_cron.go:76-82): Redelivery of the same due slot returns the original receipt and does not start another run.
- `INV-OVERLAP` (internal/store/automation.go:177-181): Forbid overlap records skipped_overlap. It does not continue the busy item and it is not a completed scan.
- `INV-TWO-SLOTS` (internal/server/m4_cron.go:76-82): Two nonoverlapping due slots create two work items.
- `INV-LEGACY-CONTINUE` (internal/automation/engine.go:170-180): Legacy schedule and coding-thread continuation stays the default until occurrenceMode is fresh.
- `INV-LEADER` (internal/server/m4_cron.go:31-40): The due slot and receipt live in Postgres. Leader change and late delivery do not create a second receipt. A crash before commit inserts at most once.
- `INV-REPORT-ZERO` (internal/definition/types.go:276-286): A report with findingsCount 0 and coverage present is success.
- `INV-PR-SEPARATION` (internal/server/terminal.go:601-607): The input pull request never satisfies requires pullRequest.
- `INV-REQUIRES-PR` (internal/server/terminal.go:499-516): Opt-in requires pullRequest still fails closed with no_work_product when no output pull request was recorded.
- `INV-REQUIRES-EMPTY` (internal/definition/validate.go:96-113): Omitted or empty requires does not demand a pull request or a report.
- `INV-REQUIRES-REPORT` (internal/definition/validate.go:96-113): requires report ships only in the same change as durable storage and a real terminal validator.
- `INV-NO-FINDINGS-ENUM` (internal/definition/validate.go:109-112): requires findings and requires artifacts are not values.
- `INV-REPORT-MISSING` (internal/server/terminal.go:21-22): Missing report when report is required fails with report_missing. It does not reuse no_work_product.
- `INV-UNAUTH-READ` (internal/api/handler.go:138-145): An unauthorized result read is 403 with an empty body.
- `INV-PATH` (internal/server/terminal.go:748-760): Publication rejects absolute paths, backslashes, and .. segments.
- `INV-SYMLINK` (internal/server/terminal.go:748-760): A lexically safe path that is a symlink is result_symlink_rejected.
- `INV-SIZE` (internal/server/terminal.go:748-760): Report bodies are at most 1 MiB. Artifact files are at most 16 MiB.
- `INV-SECRET-PUB` (internal/server/assign.go:91-94): A secret scan hit is not published. Fixtures use the synthetic canary label only.
- `INV-TREE` (internal/definition/inherit.go:66-73): Native mode freezes one synthetic root, an acyclic connected depth-2 tree, direct-manager delegation, and no ancestor shortcut. It is not an Atum roster.
- `INV-LEGACY-HIERARCHY` (internal/definition/types.go:267-270): Legacy factories may omit reportsTo. The field is not enforced unless native hierarchy is on.
- `INV-DEPTH` (internal/definition/inherit.go:66-73): The proof profile max depth is 2. The absolute max depth is 8. A deeper spawn is depth_exceeded.
- `INV-DELEGATION` (internal/definition/inherit.go:66-73): Direct-manager delegation requires the delegator to be reportsTo and listed in spawnableBy. spawnableBy stays parsed until M9-N enforces it.
- `INV-NOT-PARENT-RUN` (internal/server/work.go:660-682): parent_run_id remains the stage predecessor. A delegation edge is a different id.
- `INV-NO-SECRET-FROM-REPORTS` (internal/server/assign.go:47-53): Reporting line does not grant secret or repository access.
- `INV-IDENTITIES` (internal/store/types.go:110-131): Task, assignee, blockers, activation, and execution are distinct identities.
- `INV-STAGE-OWNER` (internal/server/work.go:246-255): Only the stage owner advances the pipeline. A child with the same stage string does not.
- `INV-CANONICAL` (internal/store/events.go:107-115): One canonical event record per invocation. The inbox is a per-assignee projection, not a second store.
- `INV-BODY` (internal/api/handler.go:18-20): Inbox and comment bodies are at most 8192 bytes.
- `INV-ACK` (internal/store/events.go:97-105): Ack is explicit, idempotent, and scoped to task and principal. It runs only after durable apply.
- `INV-READ-NOT-ACK` (internal/client/run_events.go:50-56): Read and long-poll do not acknowledge.
- `INV-DROP` (internal/store/events.go:97-105): A dropped response leaves the delivery unacked. Redelivery applies the message once.
- `INV-DUP-ACK` (internal/store/events.go:97-105): A duplicate ack does not apply the message twice.
- `INV-LATE-COMMIT` (internal/store/migrations/0009_run_lifecycle_events.sql:94-96): Lock the invocation ledger row and commit the event, state, and outbox together. factory_seq and run_events.seq are not invocation order.
- `INV-SSE` (internal/client/run_events.go:50-56): Per-run SSE cursors stay on run_events.seq.
- `INV-CLAIM` (internal/store/events.go:107-115): A second claim loses the fence. The holder and fence stay unchanged.
- `INV-PAGINATION` (internal/store/events.go:122-127): Inbox pages use an opaque cursor. Limit is 1 through 100 and defaults to 50.
- `INV-COALESCE` (internal/server/m5_steer.go:31-42): Repeated wake intents for one task collapse to one durable wake record.
- `INV-CAPACITY` (internal/server/work.go:341-346): Capacity 1 still finishes the tree because a manager parks and releases its slot before the child runs.
- `INV-PARK` (internal/server/work.go:341-346): Park is per task, records blockers, and releases worker capacity. Today's suspension is one per work item.
- `INV-STEER` (internal/server/m5_steer.go:23-29): A running recipient is steered only when steer was negotiated. Otherwise the input stays pending.
- `INV-COMPLETED-WAKE` (internal/server/m5_steer.go:39-41): Waking a completed task creates a new linked run. The prior terminal row stays immutable.
- `INV-RETRY-EXEC` (internal/store/types.go:110-112): Infrastructure retry creates a new execution of the same run.
- `INV-PARK-CONTINUE` (internal/server/work.go:341-346): Park continuation creates a new execution of the same run.
- `INV-NO-RESURRECT` (internal/server/work.go:246-255): A cancelled task does not auto-resurrect.
- `INV-SUBTREE-CANCEL` (internal/server/work.go:330-338): Cancel and pause cascade over the chosen subtree and later spawn is spawn_denied.
- `INV-BOARD-RESUME` (internal/store/types.go:100-102): Board resume is an authorized audited transition. It does not mutate the cancelled run's terminal evidence.
- `INV-FAIL-CHILD` (internal/server/work.go:276-279): fail_parent does not synthesize and does not spawn a replacement.
- `INV-JOIN` (internal/server/work.go:276-279): Join does not synthesize while a required child is live or unresolved.
- `INV-DELEGATE-IDEMPOTENT` (internal/store/automation.go:177-181): Duplicate delegate delivery does not create a second child or a second reservation.
- `INV-BUDGET-DEDUP` (internal/store/m5_read.go:137-139): Usage is integer cents deduplicated by call and attempt, including a failed attempt. A duplicate attempt is not counted twice.
- `INV-ROLLUP` (internal/store/m5_read.go:109-116): Each leaf charge rolls up once to invocation, lead, agent, and factory. Summing inclusive totals is not the rollup.
- `INV-RESERVE` (internal/store/m5_read.go:137-139): Ancestor reservation is one transaction. Scope rows lock in scope id order. Failure leaves no partial reserve.
- `INV-CONCURRENT` (internal/store/m5_read.go:137-139): Two concurrent reservations that would pass a 2500 cent ceiling cannot both commit.
- `INV-ALERT80` (internal/store/m5_read.go:137-139): An 80 percent crossing emits one alert per episode. A refund below the line starts a new episode.
- `INV-MISSING-USAGE` (internal/store/m5_read.go:109-116): Missing usage after the deadline admits no new spend. The overshoot bound is the outstanding reservation.
- `INV-OVERSHOOT` (internal/store/m5_read.go:137-139): Spent plus reserved stays at or under the ceiling, so attributed overshoot beyond the ceiling is 0. The gateway cap is at most the ceiling.
- `INV-LATE-CHARGE` (internal/store/m5_read.go:137-139): A late charge above its reservation is quarantined, the scope pauses, and spent stays at the reservation.
- `INV-GATEWAY` (internal/store/m5_read.go:137-139): Each engagement has its own gateway allocation at most the ceiling. A shared key is not this allocation.
- `INV-SHARED-KEY` (internal/server/assign.go:91-94): Refusing a spawn on a shared key does not guarantee the ceiling. In-flight calls on that key stay uncapped.
- `INV-BUDGET-OVERRIDE` (internal/store/m5_read.go:109-116): An authorized ceiling override commits an audit row and changes the ceiling only. It spends nothing.
- `INV-TOKEN` (internal/server/m4_wire.go:343-356): Child tool tokens are a subset of the parent. A specialist token cannot dispatch or cross invocations.
- `INV-NO-DISPATCH` (internal/definition/inherit.go:66-73): Specialists do not dispatch roster agents. Leads may, under policy.
- `INV-WORKFLOW` (internal/harness/run.go:117-123): Internal workers stay in-session. Workflow stays main-loop-only. They are not roster nodes.
- `INV-SECRET-ALLOWLIST` (internal/server/assign.go:47-53): Resolve allows only names on the allowlist. Other names fail closed.
- `INV-SECRET-REFRESH` (internal/server/assign.go:47-53): Every injection reads the mounted file again. subPath is not rotation. A running process does not rotate in place.
- `INV-SECRET-UNSAFE` (internal/server/assign.go:47-53): A missing ref or an unsafe path is secret_ref_rejected.
- `INV-REDACTION` (internal/server/assign.go:91-94): Redaction keeps both the old and the new synthetic values through rotation, including MCP output.
- `INV-ISOLATION` (internal/harness/run.go:132-134): Credential isolation is an adapter-level result. Child environment isolation alone is not accepted. An unproven boundary blocks live.
- `INV-READONLY-TOKEN` (internal/forge/github/token.go:54-61): Scanner forge tokens are contents read and metadata read. The current DefaultPermissions write set is not a scanner token.
- `INV-UNTRUSTED` (internal/server/assign.go:174-178): An untrusted task gets no Docker socket, no foreign checkout write, and no broad secret mount.
- `INV-TASK-REF` (internal/normalize/linear/doc.go:18-18): Native scanner mode uses task_ref. linear_issue is null and Linear is not required.
- `INV-LOCAL-CLI` (internal/harness/run.go:117-123): Local CLI mode stays available and does not require Linear or a Factory task ref.
- `INV-ART` (internal/server/terminal.go:748-760): Each item gets its own workspace mount. There is no shared writable ART root.
- `INV-RETENTION` (internal/server/terminal.go:748-760): The durable result row outlives task-exit cleanup of the host workspace.
- `INV-BRIDGE` (internal/automation/reply.go:88-92): One outbound owner. Default disabled. Outage queues reporting and does not block execution. A send without authorization does not send.
- `INV-SECURITY-GATE` (internal/server/assign.go:174-178): A paid probe stays closed until G14, G18, and the gateway cap are proved on the actual backend. Helm render and kind do not prove a Docker host.
- `INV-ATTESTATION` (internal/server/m4_wire.go:343-356): The attestation matches bound to observed digests. A changed config, code, image, adapter, backend, or secret policy invalidates it.
- `INV-LEGACY-ROUTES` (internal/api/handler.go:75-86): POST /tasks keeps its work-item meaning. POST /agent/run stays a bare run. Neither becomes Invoke.

## Producer table

| Invariant | Consumer | Producer | Method | Field | Test vector |
|---|---|---|---|---|---|
| `INV-REQUEST-SHAPE` | M8-I | M8-I | `invocation.Service.Invoke` | `POST /api/v1/factory/{uid}/invocations` | `INV-REQUEST-SHAPE`, `INV-REQUEST-SHAPE-BAD` |
| `INV-REPLAY` | M8-I,M9-N,M9-H | M8-I | `store.InsertInvocation` | `factoryUid,profile,sourceOccurrenceKey,canonicalDigest` | `INV-REPLAY`, `INV-REPLAY-BAD` |
| `INV-CONFLICT` | M8-I | M8-I | `invocation.Service.Invoke` | `canonicalDigest` | `INV-CONFLICT`, `INV-CONFLICT-BAD` |
| `INV-INTENT-CONFLICT` | M9-N | M8-I | `invocation.Service.Invoke` | `intent` | `INV-INTENT-CONFLICT`, `INV-INTENT-CONFLICT-BAD` |
| `INV-PRIORITY` | M8-I | M8-I | `invocation.Service.Invoke` | `priority` | `INV-PRIORITY`, `INV-PRIORITY-BAD` |
| `INV-CORRELATION` | M8-I | M8-I | `invocation.Service.Invoke` | `correlation.threadRef` | `INV-CORRELATION`, `INV-CORRELATION-BAD` |
| `INV-PR-DISTINCT` | M8-I,M10-A | M8-I | `invocation.Service.Invoke` | `target.pullRequest,sourceOccurrenceKey` | `INV-PR-DISTINCT`, `INV-PR-DISTINCT-BAD` |
| `INV-PIN` | M9-H,M10-A | M8-I | `invocation.Service.Invoke` | `targetPin.headSha,targetPin.baseSha,targetPin.refSha` | `INV-PIN`, `INV-PIN-BAD` |
| `INV-NO-FORGE` | M8-I | M8-I | `invocation.Service.Invoke` | `target` | `INV-NO-FORGE`, `INV-NO-FORGE-BAD` |
| `INV-PRINCIPAL` | M8-I,M9-T | M8-I | `api.withAPIKey` | `principal` | `INV-PRINCIPAL`, `INV-PRINCIPAL-BAD` |
| `INV-PROFILE` | M8-I | M8-I | `invocation.Service.Invoke` | `profile.name` | `INV-PROFILE`, `INV-PROFILE-BAD` |
| `INV-REPOSITORY` | M9-H | M8-I | `invocation.Service.Invoke` | `target.repository` | `INV-REPOSITORY`, `INV-REPOSITORY-BAD` |
| `INV-GRANT` | M9-N,M10-A | M8-I | `invocation.Service.Invoke` | `grants.executeTarget` | `INV-GRANT`, `INV-GRANT-BAD` |
| `INV-SCHEDULE-KEY` | M8-I | M8-I | `scheduler.EmitOccurrence` | `scheduleId,dueSlot` | `INV-SCHEDULE-KEY`, `INV-SCHEDULE-KEY-BAD` |
| `INV-SCHEDULE-REPLAY` | M8-I | M8-I | `store.InsertInvocation` | `sourceOccurrenceKey` | `INV-SCHEDULE-REPLAY`, `INV-SCHEDULE-REPLAY-BAD` |
| `INV-OVERLAP` | M8-I,M10-A | M8-I | `invocation.Service.Invoke` | `overlapPolicy,outcome` | `INV-OVERLAP`, `INV-OVERLAP-BAD` |
| `INV-TWO-SLOTS` | M10-A | M8-I | `scheduler.EmitOccurrence` | `dueSlot,workItemId` | `INV-TWO-SLOTS`, `INV-TWO-SLOTS-BAD` |
| `INV-LEGACY-CONTINUE` | M8-I | M8-I | `automation.Engine.intake` | `occurrenceMode` | `INV-LEGACY-CONTINUE`, `INV-LEGACY-CONTINUE-BAD` |
| `INV-LEADER` | M8-I | M8-I | `store.InsertInvocation` | `invocation_ledgers` | `INV-LEADER`, `INV-LEADER-BAD` |
| `INV-REPORT-ZERO` | M10-A | M8-I | `result.PutReport` | `products.report.findingsCount` | `INV-REPORT-ZERO`, `INV-REPORT-ZERO-BAD` |
| `INV-PR-SEPARATION` | M8-I,M10-A | M8-I | `result.Get` | `target.pullRequest,products.outputPullRequest` | `INV-PR-SEPARATION`, `INV-PR-SEPARATION-BAD` |
| `INV-REQUIRES-PR` | M8-I | M8-I | `server.pullRequestMissing` | `requires` | `INV-REQUIRES-PR`, `INV-REQUIRES-PR-BAD` |
| `INV-REQUIRES-EMPTY` | M8-I | M8-I | `definition.RequiresPullRequest` | `requires` | `INV-REQUIRES-EMPTY`, `INV-REQUIRES-EMPTY-BAD` |
| `INV-REQUIRES-REPORT` | M8-I | M8-I | `definition.Validate` | `requires` | `INV-REQUIRES-REPORT`, `INV-REQUIRES-REPORT-BAD` |
| `INV-NO-FINDINGS-ENUM` | M8-I | M8-I | `definition.Validate` | `requires` | `INV-NO-FINDINGS-ENUM`, `INV-NO-FINDINGS-ENUM-BAD` |
| `INV-REPORT-MISSING` | M8-I | M8-I | `result.ValidateTerminal` | `products.report` | `INV-REPORT-MISSING`, `INV-REPORT-MISSING-BAD` |
| `INV-UNAUTH-READ` | M8-I,M10-A | M8-I | `GET /invocations/{id}/result` | `links.result` | `INV-UNAUTH-READ`, `INV-UNAUTH-READ-BAD` |
| `INV-PATH` | M8-I | M8-I | `result.Publish` | `storageRef` | `INV-PATH`, `INV-PATH-BAD` |
| `INV-SYMLINK` | M8-I | M8-I | `result.Publish` | `storageRef` | `INV-SYMLINK`, `INV-SYMLINK-BAD` |
| `INV-SIZE` | M8-I | M8-I | `result.Publish` | `reportMaxBytes,artifactMaxBytes` | `INV-SIZE`, `INV-SIZE-BAD` |
| `INV-SECRET-PUB` | M10-S,M8-I | M8-I | `result.Publish` | `secretScan` | `INV-SECRET-PUB`, `INV-SECRET-PUB-BAD` |
| `INV-TREE` | M9-N | M9-N | `definition.ValidateNativeTree` | `personas.reportsTo,policySnapshot` | `INV-TREE`, `INV-TREE-BAD` |
| `INV-LEGACY-HIERARCHY` | M9-N | M9-N | `definition.ValidateNativeTree` | `nativeHierarchy` | `INV-LEGACY-HIERARCHY`, `INV-LEGACY-HIERARCHY-BAD` |
| `INV-DEPTH` | M9-N | M9-N | `coordination.Delegate` | `maxDepth,absoluteMaxDepth` | `INV-DEPTH`, `INV-DEPTH-BAD` |
| `INV-DELEGATION` | M9-H | M9-N | `coordination.Delegate` | `spawnableBy,reportsTo` | `INV-DELEGATION`, `INV-DELEGATION-BAD` |
| `INV-NOT-PARENT-RUN` | M9-N,M9-T | M9-N | `coordination.Delegate` | `delegationEdgeId,parentRunId` | `INV-NOT-PARENT-RUN`, `INV-NOT-PARENT-RUN-BAD` |
| `INV-NO-SECRET-FROM-REPORTS` | M9-H,M10-S | M9-N | `coordination.Delegate` | `reportsTo` | `INV-NO-SECRET-FROM-REPORTS`, `INV-NO-SECRET-FROM-REPORTS-BAD` |
| `INV-IDENTITIES` | M9-T,M9-N | M9-T | `store.InsertNativeTask` | `taskId,assignee,blockerIds,activationId,executionId` | `INV-IDENTITIES`, `INV-IDENTITIES-BAD` |
| `INV-STAGE-OWNER` | M9-N | M9-N | `server.advanceAfterTerminal` | `stageOwner` | `INV-STAGE-OWNER`, `INV-STAGE-OWNER-BAD` |
| `INV-CANONICAL` | M9-H | M9-T | `store.AppendLedger` | `invocation_events` | `INV-CANONICAL`, `INV-CANONICAL-BAD` |
| `INV-BODY` | M9-H | M9-T | `store.AppendLedger` | `body` | `INV-BODY`, `INV-BODY-BAD` |
| `INV-ACK` | M9-H | M9-T | `store.AckDelivery` | `deliveryId,principal,taskId` | `INV-ACK`, `INV-ACK-BAD` |
| `INV-READ-NOT-ACK` | M9-H | M9-T | `store.ReadInbox` | `cursor` | `INV-READ-NOT-ACK`, `INV-READ-NOT-ACK-BAD` |
| `INV-DROP` | M9-H | M9-T | `store.AckDelivery` | `messageId,deliveryId` | `INV-DROP`, `INV-DROP-BAD` |
| `INV-DUP-ACK` | M9-H | M9-T | `store.AckDelivery` | `deliveryId` | `INV-DUP-ACK`, `INV-DUP-ACK-BAD` |
| `INV-LATE-COMMIT` | M9-N,M9-H | M9-T | `store.AppendLedger` | `invocation_ledgers.ledger_seq` | `INV-LATE-COMMIT`, `INV-LATE-COMMIT-BAD` |
| `INV-SSE` | M9-H | M9-T | `client.StreamRunEvents` | `Last-Event-ID` | `INV-SSE`, `INV-SSE-BAD` |
| `INV-CLAIM` | M9-N | M9-T | `store.ClaimInbox` | `fence` | `INV-CLAIM`, `INV-CLAIM-BAD` |
| `INV-PAGINATION` | M9-H | M9-T | `store.ReadInbox` | `cursor,limit` | `INV-PAGINATION`, `INV-PAGINATION-BAD` |
| `INV-COALESCE` | M9-N | M9-T | `store.CoalesceWake` | `wakeIntent` | `INV-COALESCE`, `INV-COALESCE-BAD` |
| `INV-CAPACITY` | M9-N | M9-N | `coordination.ClaimActivation` | `workerCapacity` | `INV-CAPACITY`, `INV-CAPACITY-BAD` |
| `INV-PARK` | M9-N | M9-N | `coordination.Park` | `state,blockers` | `INV-PARK`, `INV-PARK-BAD` |
| `INV-STEER` | M9-H | M9-N | `coordination.DeliverInput` | `capabilities.steer` | `INV-STEER`, `INV-STEER-BAD` |
| `INV-COMPLETED-WAKE` | M9-N | M9-N | `coordination.WakeCompleted` | `linkedFromRunId` | `INV-COMPLETED-WAKE`, `INV-COMPLETED-WAKE-BAD` |
| `INV-RETRY-EXEC` | M9-N | M9-N | `coordination.RetryExecution` | `runId,executionId` | `INV-RETRY-EXEC`, `INV-RETRY-EXEC-BAD` |
| `INV-PARK-CONTINUE` | M9-N | M9-N | `coordination.ResumePark` | `executionId` | `INV-PARK-CONTINUE`, `INV-PARK-CONTINUE-BAD` |
| `INV-NO-RESURRECT` | M9-N | M9-N | `coordination.Wake` | `status` | `INV-NO-RESURRECT`, `INV-NO-RESURRECT-BAD` |
| `INV-SUBTREE-CANCEL` | M9-N | M9-N | `coordination.CancelSubtree` | `descendants` | `INV-SUBTREE-CANCEL`, `INV-SUBTREE-CANCEL-BAD` |
| `INV-BOARD-RESUME` | M9-N | M9-N | `coordination.BoardResume` | `audit.board.resume` | `INV-BOARD-RESUME`, `INV-BOARD-RESUME-BAD` |
| `INV-FAIL-CHILD` | M9-N | M9-N | `coordination.Join` | `failedChildPolicy` | `INV-FAIL-CHILD`, `INV-FAIL-CHILD-BAD` |
| `INV-JOIN` | M9-N | M9-N | `coordination.Join` | `blockers` | `INV-JOIN`, `INV-JOIN-BAD` |
| `INV-DELEGATE-IDEMPOTENT` | M9-H | M9-N | `coordination.Delegate` | `idempotencyKey` | `INV-DELEGATE-IDEMPOTENT`, `INV-DELEGATE-IDEMPOTENT-BAD` |
| `INV-BUDGET-DEDUP` | M9-H,M10-G | M9-N | `coordination.SettleUsage` | `callId,attemptId` | `INV-BUDGET-DEDUP`, `INV-BUDGET-DEDUP-BAD` |
| `INV-ROLLUP` | M9-N,M10-G | M9-N | `coordination.Rollup` | `scopes` | `INV-ROLLUP`, `INV-ROLLUP-BAD` |
| `INV-RESERVE` | M9-H | M9-N | `coordination.ReserveAncestors` | `reservation` | `INV-RESERVE`, `INV-RESERVE-BAD` |
| `INV-CONCURRENT` | M9-N | M9-N | `coordination.ReserveAncestors` | `ceilingCents` | `INV-CONCURRENT`, `INV-CONCURRENT-BAD` |
| `INV-ALERT80` | M9-N | M9-N | `coordination.NoteThreshold` | `thresholdPercent` | `INV-ALERT80`, `INV-ALERT80-BAD` |
| `INV-MISSING-USAGE` | M9-H,M10-G | M9-N | `coordination.Admit` | `usageDeadline` | `INV-MISSING-USAGE`, `INV-MISSING-USAGE-BAD` |
| `INV-OVERSHOOT` | M10-G | M9-N | `coordination.Admit` | `overshootBeyondCeilingCents` | `INV-OVERSHOOT`, `INV-OVERSHOOT-BAD` |
| `INV-LATE-CHARGE` | M10-G | M9-N | `coordination.SettleUsage` | `quarantinedCents` | `INV-LATE-CHARGE`, `INV-LATE-CHARGE-BAD` |
| `INV-GATEWAY` | M10-G,M10-A | M10-S | `gateway.AllocateEngagement` | `gatewayCapCents` | `INV-GATEWAY`, `INV-GATEWAY-BAD` |
| `INV-SHARED-KEY` | M10-G | M9-N | `coordination.Admit` | `sharedKey` | `INV-SHARED-KEY`, `INV-SHARED-KEY-BAD` |
| `INV-BUDGET-OVERRIDE` | M9-N | M9-N | `coordination.AuthorizeCeiling` | `ceilingCents,audit` | `INV-BUDGET-OVERRIDE`, `INV-BUDGET-OVERRIDE-BAD` |
| `INV-TOKEN` | M9-H,M10-A | M9-H | `harness.IssueRunToken` | `grants,attenuatedFrom` | `INV-TOKEN`, `INV-TOKEN-BAD` |
| `INV-NO-DISPATCH` | M10-A | M9-H | `harness.IssueRunToken` | `roster.dispatch` | `INV-NO-DISPATCH`, `INV-NO-DISPATCH-BAD` |
| `INV-WORKFLOW` | M9-H | M9-H | `harness.Negotiate` | `workflow,internalWorkers` | `INV-WORKFLOW`, `INV-WORKFLOW-BAD` |
| `INV-SECRET-ALLOWLIST` | M9-H | M10-S | `secrets.Resolver.Resolve` | `name` | `INV-SECRET-ALLOWLIST`, `INV-SECRET-ALLOWLIST-BAD` |
| `INV-SECRET-REFRESH` | M9-H | M10-S | `secrets.Resolver.Resolve` | `injectionId` | `INV-SECRET-REFRESH`, `INV-SECRET-REFRESH-BAD` |
| `INV-SECRET-UNSAFE` | M9-H | M10-S | `secrets.Resolver.Resolve` | `path` | `INV-SECRET-UNSAFE`, `INV-SECRET-UNSAFE-BAD` |
| `INV-REDACTION` | M9-H,M10-S | M10-S | `secrets.Redactor` | `retainedLiterals` | `INV-REDACTION`, `INV-REDACTION-BAD` |
| `INV-ISOLATION` | M10-A,M10-G | M9-H | `harness.ProveCredentialBoundary` | `outcome` | `INV-ISOLATION`, `INV-ISOLATION-BAD` |
| `INV-READONLY-TOKEN` | M10-A | M9-H | `forge.MintReadToken` | `permissions.contents` | `INV-READONLY-TOKEN`, `INV-READONLY-TOKEN-BAD` |
| `INV-UNTRUSTED` | M10-S,M10-A | M9-H | `harness.TaskConstraints` | `dockerSocket,foreignCheckoutWrite` | `INV-UNTRUSTED`, `INV-UNTRUSTED-BAD` |
| `INV-TASK-REF` | M10-A | M9-T | `scanner.NativeBrief` | `taskRef` | `INV-TASK-REF`, `INV-TASK-REF-BAD` |
| `INV-LOCAL-CLI` | M10-A | M10-A | `scanner.LocalCLI` | `mode` | `INV-LOCAL-CLI`, `INV-LOCAL-CLI-BAD` |
| `INV-ART` | M10-A | M10-A | `scanner.MountWorkspace` | `art.perItem` | `INV-ART`, `INV-ART-BAD` |
| `INV-RETENTION` | M10-A | M8-I | `result.PutReport` | `retention` | `INV-RETENTION`, `INV-RETENTION-BAD` |
| `INV-BRIDGE` | M10-A | M10-A | `outbound.Enqueue` | `owners,sendAuthorized` | `INV-BRIDGE`, `INV-BRIDGE-BAD` |
| `INV-SECURITY-GATE` | LIVE-10,M10-A | M10-G | `attestation.Gate` | `g14ActualBackend,g18ActualBackend,gatewayCapProven` | `INV-SECURITY-GATE`, `INV-SECURITY-GATE-BAD` |
| `INV-ATTESTATION` | LIVE-10 | M10-G | `attestation.Bind` | `configDigest,codeDigest,imageDigest,adapterDigest,backendId` | `INV-ATTESTATION`, `INV-ATTESTATION-BAD` |
| `INV-LEGACY-ROUTES` | M8-I | M8-I | `api.Handler` | `POST /tasks,POST /agent/run` | `INV-LEGACY-ROUTES`, `INV-LEGACY-ROUTES-BAD` |

## Limitations

- Witnesses are examples. They do not exercise factoryd, Postgres, the console, a model, or a scanner.
- Claude Code protocol flags, resume, and steer support are pinned by M9-H against the CLI it selects. These witnesses set `negotiatedSteer: false` and do not claim a CLI version.
- G14, G18, host firewall, and CNI are gate inputs. The security witness records the closed state. It is not a passed probe.
- The 2500 cent cap is policy input, not a measured Atum scan cost.
- No migration, route, or store method is added here. Implementing workers own the next migration number at their base.
- Peter's roster, repository, image, and scanner-repository changes stay outside this contract.
