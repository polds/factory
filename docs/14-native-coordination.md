# 14 — Native coordination spine

This is the M9-T surface. It records native tasks, a per-invocation ledger, an assignee inbox, and one coalesced wake intent. It does not start an execution, claim an activation, enforce `reportsTo`, or reserve a budget. M9-N consumes the wake later. M9-H calls the HTTP routes and the client below. There is no native dispatch route.

The contract remains `defined-factory/1`. `docs/13-defined-factory-contracts.md` states the rules. This page names the methods that implement the producer `M9-T` rows.

## Identities

| Id | Prefix | Meaning |
|---|---|---|
| Task | `ntask_` | The native task. `task_ref` equals this id. |
| Message | `nmsg_` | One canonical ledger event. |
| Delivery | `ndlv_` | One assignee projection of a message. |
| Activation | `nact_` | Stored only. Insert and assign do not start it. |
| Execution | `exec_` | Stored only. Insert and assign do not start it. |

Task, assignee, parent task, blockers, activation id, and execution id are different fields. `parent_task_id` is not `runs.parent_run_id`. `linear_issue` is not a column. The HTTP task body returns `linearIssue: null`.

`INV-TASK-REF` stays with M10-A. The scanner baseline is still `internal/normalize/linear/doc.go` (`linear_issue`). Factory stores `task_ref` as the native task id and does not claim the scanner is done.

## Ledger

`AppendLedger` locks `invocation_ledgers` `FOR UPDATE`, allocates the next `ledger_seq` for that invocation, and commits `invocation_events`, `invocation_ledger_state`, and `invocation_outbox` in the same transaction. A rollback removes all of them. Two invocations each start at sequence 1. This path does not write `run_events` and does not call `nextval`. Per-run SSE cursors stay on `run_events.seq` (`Last-Event-ID`).

The inbox body lives on `invocation_events.body`. `native_deliveries` has no body column. Read joins the event.

A comment or assign body longer than 8192 bytes is `input_invalid`. The check is the raw byte length before redaction. `ledger.PrepareBody` then applies `definition.RedactInline` (`Bearer <token>` becomes `Bearer [REDACTED]`). `DeliveryBodyCap` stays `256 << 10`. The API body limit stays `1 << 20`.

## Inbox, ack, and claim

Read and long-poll do not ack. Ack is `POST` after a durable apply, unique on `(delivery_id, principal, task_id)`. The principal is the API key (`api-key`), never a JSON field. Ack before apply is `not_applied`. A different principal is `principal_rejected`. A different task is not found. A second ack returns `duplicate: true` and does not increment `native_applies.apply_count`.

A dropped read leaves the delivery unacked. Redeliver inserts a new `ndlv_` for the same `nmsg_` and does not apply or ack. Apply of that message stays once.

The first claim inserts fence 1. A later claim, including one from the holder, returns HTTP 409 `claim_fenced`. The holder and fence stay unchanged.

Cursor is opaque (`n1.<ledger_seq>.<delivery_id>`). Limit is 1 through 100 and defaults to 50. A numeric cursor is `input_invalid`.

## Wake

One `native_wake_intents` row per task. Comment and assign set it pending and route `assignee` to the task's current assignee. Reassign updates that same row and increments `coalesce_count`. Mention, result, and completion do not wake. The row survives a new store connection. Nothing here starts a run.

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
inbox.Redeliver(ctx, st, messageID string) (store.InboxDelivery, error)

store.InsertNativeTask(ctx, NativeTaskInput) (NativeTask, error)
store.GetNativeTask(ctx, factoryID, taskID string) (NativeTask, error)
store.AppendLedger(ctx, invocationID string, LedgerEntry) (LedgerRecord, error)
store.ReadOutbox(ctx, invocationID string, afterSeq int64, limit int) ([]OutboxRow, error)
store.ReadInbox(ctx, InboxQuery) (InboxPage, error)
store.ApplyMessage(ctx, messageID, taskID, principal string) (bool, error)
store.AckDelivery(ctx, deliveryID, principal, taskID string) (AckResult, error)
store.Redeliver(ctx, messageID string) (InboxDelivery, error)
store.ClaimInbox(ctx, taskID, holder string) (InboxClaim, error)
store.GetInboxClaim(ctx, taskID string) (InboxClaim, error)
store.CoalesceWake(ctx, WakeIntent) (WakeIntent, error)
store.GetWake(ctx, taskID string) (WakeIntent, error)

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
```

`GetNativeClaim` and `ReadNativeOutbox` are on `api.NativeBackend` and the server. The Go client does not wrap them yet.

## Optional items

Orphan repair is in. An accepted invocation with a null `work_item_id` is linked to the existing work item for that source ref, or one new item is created. A stage run starts only when that item has none. Replay does not insert a second item or a second run.

Workspace root is not plumbed. `result.Publish` is called from `internal/server/m8_api.go` with an empty root. That file is outside this change, and no stored workspace root is available to pass. An empty root still fails artifact paths closed. Body-only reports are unchanged.
