# 14 — Native coordination spine

M9-T records native tasks, a per-invocation ledger, an assignee inbox, and one coalesced wake intent. M9-N activates that wake: it freezes `reportsTo`, delegates, parks, joins, and reserves ancestor budgets. Creating a task still does not start a run. Activation enqueues one run on the existing `run_queue` and does not insert an execution. A worker lease and assignment create the execution and record that worker. There is no native dispatch route and no `native-activation` worker. A queued run is not evidence that the task executed.

The contract remains `defined-factory/1`. `docs/13-defined-factory-contracts.md` states the rules. This page names the methods that implement the producer `M9-T` rows.

A persona with frozen role files supplies that seat's activation prompt from those four files, in `AGENTS.md`, `HEARTBEAT.md`, `SOUL.md`, `TOOLS.md` order, each prefixed by its raw byte length. A legacy persona array with no pins and no files keeps the prompt `native activation`. `bundleMode` `role_files` requires every selected seat to have those four files and requires `sourceCommit`, `rosterSha256`, and `rosterGitBlob`. A complete stored bundle that has no `bundleMode` is read as `role_files`. Pins with every file list empty, an unknown mode, or a short seat fail before the policy row is inserted and before a run is queued. The first valid open stays frozen, including file bytes, provenance, and mode.

A `role_files` claim appends one goal after that seat's bytes. The goal names the work item, the invocation, the native task, `target_repository`, and `pinned_head_sha`, then the work-item title and body with their raw byte lengths. The native task id is not `runs.parent_run_id`. Title and body are capped at 65536 bytes each and are not trimmed. An empty title, body, pin, or repository fails the claim before a queue row is inserted. A legacy claim does not require a body. The same goal is used for every seat. The prompt contains only that seat's files. The bytes are not grants: they do not add secrets, repositories, token scopes, model ids, or schedules, and the native tree list does not grow a persona payload. `Synthetic: true` is the existing roster gate, not evidence that the source organization executed or that a model ran. The source commit, roster content hashes, and source reporting edges stay on the stored policy and survive a reload. A `role_files` open whose `sourceCommit` is not 40 hex fails before that insert. A 40-hex commit is attested against a local git directory. The check does not fetch and does not write role bytes into the factory checkout. A legacy tree with no pins and no seat files is not a git lookup.

The harness checks out `pinned_head_sha` only when the worker passes one supported local commit override and the workspace is that repository. A `role_files` assignment sets `FACTORY_PRE_HARNESS_SETUP=disabled`. The harness then writes `task.json` from the queued prompt and, when `FACTORY_LOCAL_OBJECT_SOURCE` is a local repository that already contains the pin, copies that repository into an empty workspace without fetching. An untrusted setup command in that env or in `task.json` is not executed. The local source is refused when it is unsafe to copy: a symlink, a worktree gitfile, a filter driver, or an origin that is not a plain GitHub remote. The upstream worker `setup_command` is not an assignment field. Pinned `oz-agent-worker` `63dbb8a` runs a non-empty worker-config command before `factory-harness`. `FACTORY_PRE_HARNESS_SETUP` does not disable that command. Default Helm worker values omit it. This page does not prove a runtime backend or that `SourceCommit` contains the role blobs. A child-produced review commit is not stored. `SourceCommit` is not the checkout SHA.

A `role_files` evidence run is still one queued native run on `run_queue`. There is no second scheduler. When a frozen reviewer exists, only that seat receives `DREAMD_TEST_DSN`, and only when the produced SHA is ready and both `FACTORY_EVIDENCE_PACKAGE` and `FACTORY_EVIDENCE_MUTATION` are set. Any other assignee ignores those two variables. A tree with no reviewer keeps the previous behavior: a `role_files` run whose server environment names `FACTORY_EVIDENCE_PACKAGE` receives `DREAMD_TEST_DSN` for a database and role that factoryd created. The prompt does not choose the `go test` package or the mutation path. After the existing checkout gate, and before a model request, the harness retains the test transcript as `evidence.test`. `tool.result` stays a digest. `JoinNative` is unchanged and is not the evidence gate. A configured worker `setup_command` still runs before the harness when the worker config sets one. The disabled flag does not clear it. The proof pool still renders neither command. Missing `vector` is `pgvector_unavailable`, not a green suite, and the shipped Postgres image is unchanged.

When `FACTORY_PUBLISH_DRAFT_REPO` is `northstar-ai-labs/dreaming`, completing the invocation root publishes one draft pull request. The publication row is committed as `pending` before an installation token is minted. The token is `contents: write`, `pull_requests: write`, and `metadata: read` on that repository only. It is not placed in a task environment, event, or log. The publisher pushes `factory/dreaming-454-<invocation8>` at the reviewed SHA only after a fresh read shows the root is still completed, then creates the pull request with `draft: true` only after a second read. A root that is not completed, cancelled, or paused, a paused or missing-usage budget, missing named red and green evidence, a transcript that contains the truncated marker, a dirty tree, a head that is not the stored ready SHA, or a missing `factoryEvidencePass` sentinel leaves the row unopened. A non-draft response or HTTP 422 marks the row `failed` and does not open a ready pull request. A second invocation for `dreaming#454` does not mint or push. The harness `DELETE` of the produced bundle returns success and keeps the object until the row is `opened` or `failed`, or the invocation is terminal and has no publication row.

## Identities

| Id | Prefix | Meaning |
|---|---|---|
| Task | `ntask_` | The native task. `task_ref` equals this id. |
| Message | `nmsg_` | One canonical ledger event. |
| Delivery | `ndlv_` | One assignee projection of a message. |
| Activation | `nact_` | One fenced claim. `POST .../activate` enqueues the run and reserves an execution id. It does not insert `executions`. |
| Execution | `exec_` | Inserted by worker `StartExecution` after lease. That call binds the activation to the execution it opened. A reserved id, or an orphaned, failed, cancelled, or completed execution, is replaced. A live execution is not. |

Task, assignee, parent task, blockers, activation id, and execution id are different fields. `parent_task_id` is not `runs.parent_run_id`. `linear_issue` is not a column. The HTTP task body returns `linearIssue: null`.

`INV-TASK-REF` stays with M10-A. The scanner baseline is still `internal/normalize/linear/doc.go` (`linear_issue`). Factory stores `task_ref` as the native task id and does not claim the scanner is done.

## Ledger

`AppendLedger` locks `invocation_ledgers` `FOR UPDATE`, allocates the next `ledger_seq` for that invocation, and commits `invocation_events`, `invocation_ledger_state`, and `invocation_outbox` in the same transaction. A rollback removes all of them. Two invocations each start at sequence 1. This path does not write `run_events` and does not call `nextval`. Per-run SSE cursors stay on `run_events.seq` (`Last-Event-ID`).

The inbox body lives on `invocation_events.body`. `native_deliveries` has no body column. Read joins the event.

A comment or assign body longer than 8192 bytes is `input_invalid`. The check is the raw byte length before redaction. `ledger.PrepareBody` then applies `definition.RedactInline` (`Bearer <token>` becomes `Bearer [REDACTED]`). `DeliveryBodyCap` stays `256 << 10`. The API body limit stays `1 << 20`.

## Inbox, ack, and claim

Read and long-poll do not ack. Ack is `POST` after a durable apply, unique on `(delivery_id, principal, task_id)`. The principal is the API key (`api-key`), never a JSON field. Ack before apply is `not_applied`. A different principal is `principal_rejected`. A different task is not found. A second ack returns `duplicate: true` and does not increment `native_applies.apply_count`.

A dropped read leaves the delivery unacked. Redeliver reads the message task and compares it to the path task before inserting a new `ndlv_` for the same `nmsg_`. A different task inserts nothing. Redeliver does not apply or ack. Apply of that message stays once.

The first claim inserts fence 1. A later claim, including one from the holder, returns HTTP 409 `claim_fenced`. The holder and fence stay unchanged.

Cursor is opaque (`n1.<ledger_seq>.<delivery_id>`). Limit is 1 through 100 and defaults to 50. A numeric cursor is `input_invalid`.

## Wake

One `native_wake_intents` row per task. Comment and assign set it pending and route `assignee` to the task's current assignee. Reassign updates that same row and increments `coalesce_count`. Mention does not wake. The row survives a new store connection. `GetWake` returns `InvocationID`. The HTTP wake body does not. Reading the wake does not start a run. `ClaimActivation` enqueues one, using that stored invocation id. The run stays `queued`. Lease moves it to `assigned`. `StartExecution` moves it to `running` under the worker id and binds that execution. Binding replaces a reserved id or a non-live execution. It does not replace an execution that is still `assigned` or `running`. Task status `running` means the activation holds the capacity slot, including while the run is still queued. Success completion requires that live execution; a queued run is not execution success. Pause removes the queue row and, when a worker execution is live, cancels that execution and replaces its token before releasing capacity. The paused run is not left `assigned` or `running` and is not marked succeeded. Board resume remains the audited path for a cancelled task.

## HTTP

All routes use the factory API key. An unknown factory or a task in another factory is 404.

| Method | Path |
|---|---|
| POST | `/api/v1/factory/{uid}/native-tasks` |
| GET | `/api/v1/factory/{uid}/native-tasks/{taskID}` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/comments` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/assign` |
| GET | `/api/v1/factory/{uid}/native-inbox?assignee&cursor&limit&waitMs` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/messages/{messageID}/apply` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/deliveries/{deliveryID}/ack` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/messages/{messageID}/redeliver` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/claim` |
| GET | `/api/v1/factory/{uid}/native-tasks/{taskID}/claim` |
| GET | `/api/v1/factory/{uid}/native-tasks/{taskID}/wake` |
| GET | `/api/v1/factory/{uid}/invocations/{invocationID}/outbox?after&limit` |
| POST | `/api/v1/factory/{uid}/invocations/{invocationID}/native-tree` |
| GET | `/api/v1/factory/{uid}/native-trees` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/delegate` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/activate` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/park` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/wake-run` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/join` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/cancel-subtree` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/pause-subtree` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/board-resume` |
| POST | `/api/v1/factory/{uid}/native-tasks/{taskID}/complete` |
| POST | `/api/v1/factory/{uid}/invocations/{invocationID}/budget/reserve` |
| POST | `/api/v1/factory/{uid}/invocations/{invocationID}/budget/settle` |
| POST | `/api/v1/factory/{uid}/invocations/{invocationID}/budget/admit` |
| POST | `/api/v1/factory/{uid}/budget/ceiling` |

Apply, ack, and redeliver bodies are empty or `{}`. A `principal` field is `input_invalid`.

## Signatures N and H call

```text
tasks.Create(ctx, st, store.NativeTaskInput) (store.NativeTask, error)
ledger.PrepareBody(body string) (string, error)
ledger.Wakes(kind string) bool
ledger.Append(ctx, st, invocationID, store.LedgerEntry) (store.LedgerRecord, error)
inbox.Read(ctx, st, store.InboxQuery) (store.InboxPage, error)
inbox.Apply(ctx, st, messageID, taskID, principal string) (bool, error)
inbox.Ack(ctx, st, deliveryID, principal, taskID string) (store.AckResult, error)
inbox.Claim(ctx, st, taskID, holder string) (store.InboxClaim, error)
inbox.Redeliver(ctx, st, messageID, taskID string) (store.InboxDelivery, error)

store.InsertNativeTask(ctx, NativeTaskInput) (NativeTask, error)
store.GetNativeTask(ctx, factoryID, taskID string) (NativeTask, error)
store.AppendLedger(ctx, invocationID string, LedgerEntry) (LedgerRecord, error)
store.ReadOutbox(ctx, invocationID string, afterSeq int64, limit int) ([]OutboxRow, error)
store.ReadInbox(ctx, InboxQuery) (InboxPage, error)
store.ApplyMessage(ctx, messageID, taskID, principal string) (bool, error)
store.AckDelivery(ctx, deliveryID, principal, taskID string) (AckResult, error)
store.Redeliver(ctx, messageID, taskID string) (InboxDelivery, error)
store.ClaimInbox(ctx, taskID, holder string) (InboxClaim, error)
store.GetInboxClaim(ctx, taskID string) (InboxClaim, error)
store.CoalesceWake(ctx, WakeIntent) (WakeIntent, error)
store.GetWake(ctx, taskID string) (WakeIntent, error)

coordination.OpenTree(ctx, st, store.OpenTreeInput) (store.NativePolicy, store.NativeTask, error)
coordination.Delegate(ctx, st, store.DelegateInput) (store.DelegateResult, error)
coordination.ClaimActivation(ctx, st, taskID, store.ClaimInput) (store.Activation, error)
coordination.Park(ctx, st, store.ParkInput) (store.Activation, error)
coordination.Wake(ctx, st, store.WakeInput) (store.WakeResult, error)
coordination.WakeCompleted(ctx, st, taskID, store.WakeInput) (store.WakeResult, error)
coordination.RetryExecution(ctx, st, taskID) (store.Activation, error)
coordination.ResumePark(ctx, st, store.ClaimInput) (store.Activation, error)
coordination.Join(ctx, st, store.JoinInput) (store.JoinResult, error)
coordination.CancelSubtree(ctx, st, taskID) (store.SubtreeResult, error)
coordination.BoardResume(ctx, st, store.BoardResumeInput) (store.BoardResumeResult, error)
coordination.Admit(ctx, st, store.AdmitInput) (store.AdmitResult, error)
coordination.ReserveAncestors(ctx, st, store.ReserveInput) (store.Reservation, error)
coordination.SettleUsage(ctx, st, store.SettleInput) (store.Settlement, error)
coordination.AuthorizeCeiling(ctx, st, store.CeilingInput) (store.CeilingAudit, error)

client.CreateNativeTask(ctx, factoryUID, NativeTaskRequest) (NativeTask, error)
client.GetNativeTask(ctx, factoryUID, taskID) (NativeTask, error)
client.CommentNativeTask(ctx, factoryUID, taskID, NativeCommentRequest) (NativeEventResult, error)
client.AssignNativeTask(ctx, factoryUID, taskID, assignee) (NativeEventResult, error)
client.ReadNativeInbox(ctx, factoryUID, assignee, cursor, limit, wait) (NativeInboxPage, error)
client.ApplyNativeMessage(ctx, factoryUID, taskID, messageID) (NativeApplyResult, error)
client.AckNativeDelivery(ctx, factoryUID, taskID, deliveryID) (NativeAckResult, error)
client.RedeliverNativeMessage(ctx, factoryUID, taskID, messageID) (NativeRedelivery, error)
client.ClaimNativeInbox(ctx, factoryUID, taskID, holder) (NativeClaim, error)
client.GetNativeWake(ctx, factoryUID, taskID) (NativeWake, error)
client.GetNativeClaim(ctx, factoryUID, taskID) (NativeClaim, error)
client.ReadNativeOutbox(ctx, factoryUID, invocationID, after, limit) (NativeOutboxPage, error)
client.OpenNativeTree(ctx, factoryUID, invocationID, NativeTreeRequest) (NativeTreeOpened, error)
client.DelegateNative(ctx, factoryUID, taskID, NativeDelegateRequest) (NativeDelegateResult, error)
client.ActivateNative(ctx, factoryUID, taskID, NativeActivateRequest) (NativeActivation, error)
```

`ClaimActivation` calls `store.GetWake`. That row includes `InvocationID`. The HTTP wake DTO does not. `client.NativeOutboxRecord.Payload` matches `api.NativeOutboxRecord` and `store.OutboxRow`.

A runtime refusal uses the same transaction as the refused spawn. `delegation_denied` is 403. `depth_exceeded`, `spawn_denied`, `budget_exceeded`, `claim_fenced`, and `cancelled_no_resurrect` are 409. `input_invalid` is 400. Ceiling and board-resume bodies have no principal field. The audited principal is `api_key:ops`.

## Optional items

Orphan repair is in. An accepted invocation with a null `work_item_id` is linked to the existing work item for that source ref, or one new item is created. The check that the item has no stage run and the insert of that run commit in the same transaction, for triage and for building. Replay does not insert a second item or a second run.

Workspace root is not plumbed. `result.Publish` is called from `internal/server/m8_api.go` with an empty root. That file is outside this change, and no stored workspace root is available to pass. An empty root still fails artifact paths closed. Body-only reports are unchanged.
