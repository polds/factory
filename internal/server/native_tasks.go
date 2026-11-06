package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/coordination/inbox"
	"github.com/polds/factory/internal/coordination/ledger"
	"github.com/polds/factory/internal/coordination/tasks"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func (f apiFacade) CreateNativeTask(ctx context.Context, factoryUID string, req api.NativeTaskRequest) (api.NativeTask, error) {
	return f.Server.CreateNativeTask(ctx, factoryUID, req)
}

func (f apiFacade) GetNativeTask(ctx context.Context, factoryUID, taskID string) (api.NativeTask, error) {
	return f.Server.GetNativeTask(ctx, factoryUID, taskID)
}

func (f apiFacade) CommentNativeTask(ctx context.Context, factoryUID, taskID string, req api.NativeCommentRequest) (api.NativeEventResult, error) {
	return f.Server.CommentNativeTask(ctx, factoryUID, taskID, req)
}

func (f apiFacade) AssignNativeTask(ctx context.Context, factoryUID, taskID string, req api.NativeAssignRequest) (api.NativeEventResult, error) {
	return f.Server.AssignNativeTask(ctx, factoryUID, taskID, req)
}

func (f apiFacade) ReadNativeInbox(ctx context.Context, factoryUID string, q api.NativeInboxQuery) (api.NativeInboxPage, error) {
	return f.Server.ReadNativeInbox(ctx, factoryUID, q)
}

func (f apiFacade) ApplyNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (api.NativeApplyResult, error) {
	return f.Server.ApplyNativeMessage(ctx, factoryUID, taskID, messageID)
}

func (f apiFacade) AckNativeDelivery(ctx context.Context, factoryUID, taskID, deliveryID string) (api.NativeAckResult, error) {
	return f.Server.AckNativeDelivery(ctx, factoryUID, taskID, deliveryID)
}

func (f apiFacade) RedeliverNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (api.NativeRedelivery, error) {
	return f.Server.RedeliverNativeMessage(ctx, factoryUID, taskID, messageID)
}

func (f apiFacade) ClaimNativeInbox(ctx context.Context, factoryUID, taskID, holder string) (api.NativeClaim, error) {
	return f.Server.ClaimNativeInbox(ctx, factoryUID, taskID, holder)
}

func (f apiFacade) GetNativeClaim(ctx context.Context, factoryUID, taskID string) (api.NativeClaim, error) {
	return f.Server.GetNativeClaim(ctx, factoryUID, taskID)
}

func (f apiFacade) GetNativeWake(ctx context.Context, factoryUID, taskID string) (api.NativeWake, error) {
	return f.Server.GetNativeWake(ctx, factoryUID, taskID)
}

func (f apiFacade) ReadNativeOutbox(ctx context.Context, factoryUID, invocationID string, after int64, limit int) (api.NativeOutboxPage, error) {
	return f.Server.ReadNativeOutbox(ctx, factoryUID, invocationID, after, limit)
}

// CreateNativeTask records a task and does not start an activation or a run.
func (s *Server) CreateNativeTask(ctx context.Context, factoryUID string, req api.NativeTaskRequest) (api.NativeTask, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeTask{}, err
	}
	task, err := tasks.Create(ctx, s.store, store.NativeTaskInput{
		FactoryID:    fac.ID,
		InvocationID: req.InvocationID,
		ParentTaskID: req.ParentTaskID,
		Assignee:     req.Assignee,
		BlockerIDs:   req.BlockerIDs,
		ActivationID: req.ActivationID,
		ExecutionID:  req.ExecutionID,
		BudgetScope:  req.BudgetScope,
	})
	if err != nil {
		return api.NativeTask{}, nativeStoreErr(err)
	}
	if s.log != nil {
		s.log.Info("native task", "task_id", task.ID, "invocation_id", task.InvocationID, "assignee", task.Assignee)
	}
	return wireNativeTask(task), nil
}

// GetNativeTask returns the task when it belongs to the factory.
func (s *Server) GetNativeTask(ctx context.Context, factoryUID, taskID string) (api.NativeTask, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeTask{}, err
	}
	task, err := s.store.GetNativeTask(ctx, fac.ID, taskID)
	if err != nil {
		return api.NativeTask{}, nativeStoreErr(err)
	}
	return wireNativeTask(task), nil
}

// CommentNativeTask appends a comment or mention. Mention does not wake.
func (s *Server) CommentNativeTask(ctx context.Context, factoryUID, taskID string, req api.NativeCommentRequest) (api.NativeEventResult, error) {
	task, err := s.nativeTask(ctx, factoryUID, taskID)
	if err != nil {
		return api.NativeEventResult{}, err
	}
	kind := req.Kind
	if kind == "" {
		kind = store.LedgerKindComment
	}
	if kind != store.LedgerKindComment && kind != store.LedgerKindMention {
		return api.NativeEventResult{}, api.ErrNativeInput
	}
	rec, err := ledger.Append(ctx, s.store, task.InvocationID, store.LedgerEntry{
		Kind:     kind,
		TaskID:   task.ID,
		Body:     req.Body,
		Assignee: task.Assignee,
		Wake:     true,
		Payload:  json.RawMessage(`{}`),
	})
	if err != nil {
		return api.NativeEventResult{}, nativeStoreErr(err)
	}
	return nativeEventResult(rec, kind), nil
}

// AssignNativeTask routes the task to the new assignee and records one wake.
func (s *Server) AssignNativeTask(ctx context.Context, factoryUID, taskID string, req api.NativeAssignRequest) (api.NativeEventResult, error) {
	task, err := s.nativeTask(ctx, factoryUID, taskID)
	if err != nil {
		return api.NativeEventResult{}, err
	}
	if req.Assignee == "" {
		return api.NativeEventResult{}, api.ErrNativeInput
	}
	payload, err := json.Marshal(map[string]string{"assignee": req.Assignee})
	if err != nil {
		return api.NativeEventResult{}, err
	}
	rec, err := ledger.Append(ctx, s.store, task.InvocationID, store.LedgerEntry{
		Kind:       store.LedgerKindAssign,
		TaskID:     task.ID,
		Assignee:   req.Assignee,
		Wake:       true,
		WakeReason: "assign",
		Payload:    payload,
		TaskUpdate: &store.NativeTaskUpdate{Assignee: req.Assignee, Status: store.NativeTaskPending},
	})
	if err != nil {
		return api.NativeEventResult{}, nativeStoreErr(err)
	}
	return nativeEventResult(rec, store.LedgerKindAssign), nil
}

// ReadNativeInbox reads one assignee projection and does not ack.
func (s *Server) ReadNativeInbox(ctx context.Context, factoryUID string, q api.NativeInboxQuery) (api.NativeInboxPage, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeInboxPage{}, err
	}
	page, err := inbox.Read(ctx, s.store, store.InboxQuery{
		FactoryID: fac.ID,
		Assignee:  q.Assignee,
		Cursor:    q.Cursor,
		Limit:     q.Limit,
		Wait:      q.Wait,
	})
	if err != nil {
		return api.NativeInboxPage{}, nativeStoreErr(err)
	}
	out := api.NativeInboxPage{Deliveries: make([]api.NativeDelivery, 0, len(page.Deliveries)), NextCursor: page.NextCursor}
	for _, row := range page.Deliveries {
		out.Deliveries = append(out.Deliveries, wireDelivery(row))
	}
	return out, nil
}

// ApplyNativeMessage records a durable apply for the authenticated principal.
func (s *Server) ApplyNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (api.NativeApplyResult, error) {
	if _, err := s.nativeTask(ctx, factoryUID, taskID); err != nil {
		return api.NativeApplyResult{}, err
	}
	applied, err := inbox.Apply(ctx, s.store, messageID, taskID, api.APIKeyPrincipal)
	if err != nil {
		return api.NativeApplyResult{}, nativeStoreErr(err)
	}
	return api.NativeApplyResult{MessageID: messageID, TaskID: taskID, Applied: applied}, nil
}

// AckNativeDelivery acks after a durable apply. A repeat does not apply again.
func (s *Server) AckNativeDelivery(ctx context.Context, factoryUID, taskID, deliveryID string) (api.NativeAckResult, error) {
	if _, err := s.nativeTask(ctx, factoryUID, taskID); err != nil {
		return api.NativeAckResult{}, err
	}
	ack, err := inbox.Ack(ctx, s.store, deliveryID, api.APIKeyPrincipal, taskID)
	if err != nil {
		return api.NativeAckResult{}, nativeStoreErr(err)
	}
	return api.NativeAckResult{
		DeliveryID: ack.DeliveryID,
		MessageID:  ack.MessageID,
		TaskID:     ack.TaskID,
		Duplicate:  ack.Duplicate,
	}, nil
}

// RedeliverNativeMessage creates a new delivery id and leaves prior acks in place.
func (s *Server) RedeliverNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (api.NativeRedelivery, error) {
	if _, err := s.nativeTask(ctx, factoryUID, taskID); err != nil {
		return api.NativeRedelivery{}, err
	}
	row, err := inbox.Redeliver(ctx, s.store, messageID, taskID)
	if err != nil {
		return api.NativeRedelivery{}, nativeStoreErr(err)
	}
	return api.NativeRedelivery{DeliveryID: row.DeliveryID, MessageID: row.MessageID, TaskID: row.TaskID}, nil
}

// ClaimNativeInbox claims the task inbox fence.
func (s *Server) ClaimNativeInbox(ctx context.Context, factoryUID, taskID, holder string) (api.NativeClaim, error) {
	if _, err := s.nativeTask(ctx, factoryUID, taskID); err != nil {
		return api.NativeClaim{}, err
	}
	claim, err := inbox.Claim(ctx, s.store, taskID, holder)
	if err != nil {
		return api.NativeClaim{}, nativeStoreErr(err)
	}
	return wireClaim(claim), nil
}

// GetNativeClaim returns the current fence.
func (s *Server) GetNativeClaim(ctx context.Context, factoryUID, taskID string) (api.NativeClaim, error) {
	if _, err := s.nativeTask(ctx, factoryUID, taskID); err != nil {
		return api.NativeClaim{}, err
	}
	claim, err := s.store.GetInboxClaim(ctx, taskID)
	if err != nil {
		return api.NativeClaim{}, nativeStoreErr(err)
	}
	return wireClaim(claim), nil
}

// GetNativeWake returns the pending wake intent. It does not activate the task.
func (s *Server) GetNativeWake(ctx context.Context, factoryUID, taskID string) (api.NativeWake, error) {
	if _, err := s.nativeTask(ctx, factoryUID, taskID); err != nil {
		return api.NativeWake{}, err
	}
	wake, err := s.store.GetWake(ctx, taskID)
	if err != nil {
		return api.NativeWake{}, nativeStoreErr(err)
	}
	return api.NativeWake{
		TaskID:        wake.TaskID,
		Assignee:      wake.Assignee,
		Reason:        wake.Reason,
		Pending:       wake.Pending,
		CoalesceCount: wake.CoalesceCount,
	}, nil
}

// ReadNativeOutbox replays the invocation outbox in ledger order.
func (s *Server) ReadNativeOutbox(ctx context.Context, factoryUID, invocationID string, after int64, limit int) (api.NativeOutboxPage, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeOutboxPage{}, err
	}
	if _, err := s.store.GetInvocation(ctx, fac.ID, invocationID); err != nil {
		return api.NativeOutboxPage{}, nativeStoreErr(err)
	}
	records, err := s.store.ReadOutbox(ctx, invocationID, after, limit)
	if err != nil {
		return api.NativeOutboxPage{}, nativeStoreErr(err)
	}
	page := api.NativeOutboxPage{Records: make([]api.NativeOutboxRecord, 0, len(records))}
	for _, rec := range records {
		page.Records = append(page.Records, api.NativeOutboxRecord{
			LedgerSeq: rec.LedgerSeq,
			Kind:      rec.Kind,
			Payload:   rec.Payload,
		})
	}
	return page, nil
}

func (s *Server) nativeTask(ctx context.Context, factoryUID, taskID string) (store.NativeTask, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return store.NativeTask{}, err
	}
	task, err := s.store.GetNativeTask(ctx, fac.ID, taskID)
	if err != nil {
		return store.NativeTask{}, nativeStoreErr(err)
	}
	return task, nil
}

func nativeEventResult(rec store.LedgerRecord, kind string) api.NativeEventResult {
	return api.NativeEventResult{
		LedgerSeq:   rec.LedgerSeq,
		MessageID:   rec.MessageID,
		DeliveryID:  rec.DeliveryID,
		WakePending: ledger.Wakes(kind),
	}
}

func wireNativeTask(task store.NativeTask) api.NativeTask {
	blockers := task.BlockerIDs
	if blockers == nil {
		blockers = []string{}
	}
	return api.NativeTask{
		ID:           task.ID,
		TaskRef:      task.TaskRef,
		FactoryID:    task.FactoryID,
		InvocationID: task.InvocationID,
		ParentTaskID: task.ParentTaskID,
		Assignee:     task.Assignee,
		BlockerIDs:   blockers,
		ActivationID: task.ActivationID,
		ExecutionID:  task.ExecutionID,
		Status:       task.Status,
		BudgetScope:  task.BudgetScope,
	}
}

func wireDelivery(row store.InboxDelivery) api.NativeDelivery {
	return api.NativeDelivery{
		DeliveryID:   row.DeliveryID,
		MessageID:    row.MessageID,
		InvocationID: row.InvocationID,
		TaskID:       row.TaskID,
		Assignee:     row.Assignee,
		Body:         row.Body,
		Kind:         row.Kind,
		LedgerSeq:    row.LedgerSeq,
	}
}

func wireClaim(claim store.InboxClaim) api.NativeClaim {
	return api.NativeClaim{
		TaskID:   claim.TaskID,
		Holder:   claim.Holder,
		Fence:    claim.Fence,
		Accepted: claim.Accepted,
	}
}

func nativeStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return api.ErrNotFound
	case errors.Is(err, store.ErrClaimFenced):
		return api.ErrNativeClaimFenced
	case errors.Is(err, store.ErrInputInvalid):
		return api.ErrNativeInput
	case errors.Is(err, store.ErrPrincipalRejected):
		return api.ErrNativePrincipal
	case errors.Is(err, store.ErrNotApplied):
		return api.ErrNativeNotApplied
	case errors.Is(err, definition.ErrDelegationDenied):
		return api.ErrNativeDelegation
	case errors.Is(err, definition.ErrDepthExceeded):
		return api.ErrNativeDepth
	case errors.Is(err, definition.ErrNativeTree), errors.Is(err, definition.ErrLegacyHierarchy):
		return api.ErrNativeInput
	case errors.Is(err, store.ErrSpawnDenied):
		return api.ErrNativeSpawn
	case errors.Is(err, store.ErrBudgetExceeded):
		return api.ErrNativeBudget
	case errors.Is(err, store.ErrNoResurrect):
		return api.ErrNativeNoResurrect
	default:
		return err
	}
}

var _ api.NativeBackend = apiFacade{}
