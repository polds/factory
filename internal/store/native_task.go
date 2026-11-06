package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const nativeTaskColumns = `
    id, task_ref, factory_id, invocation_id, parent_task_id, assignee,
    blocker_ids, activation_id, execution_id, task_status, budget_scope,
    created_at, updated_at`

func scanNativeTask(row pgx.Row) (NativeTask, error) {
	var task NativeTask
	var parent, activation, execution sql.NullString
	err := row.Scan(
		&task.ID, &task.TaskRef, &task.FactoryID, &task.InvocationID, &parent,
		&task.Assignee, &task.BlockerIDs, &activation, &execution, &task.Status,
		&task.BudgetScope, &task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NativeTask{}, ErrNotFound
		}
		return NativeTask{}, err
	}
	task.ParentTaskID = parent.String
	task.ActivationID = activation.String
	task.ExecutionID = execution.String
	if task.BlockerIDs == nil {
		task.BlockerIDs = []string{}
	}
	return task, nil
}

// InsertNativeTask inserts one native task. It does not start an activation
// or an execution, and it does not write parent_run_id.
func (s *Store) InsertNativeTask(ctx context.Context, in NativeTaskInput) (NativeTask, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NativeTask{}, fmt.Errorf("store: insert native task: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	task, err := insertNativeTaskTx(ctx, tx, in)
	if err != nil {
		return NativeTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NativeTask{}, err
	}
	return task, nil
}

func insertNativeTaskTx(ctx context.Context, tx pgx.Tx, in NativeTaskInput) (NativeTask, error) {
	id, err := NewID(PrefixNativeTask)
	if err != nil {
		return NativeTask{}, err
	}
	if err := validateNativeTask(id, in); err != nil {
		return NativeTask{}, err
	}
	if in.BlockerIDs == nil {
		in.BlockerIDs = []string{}
	}
	var invocationFactory string
	err = tx.QueryRow(ctx, `SELECT factory_id FROM invocations WHERE id = $1`, in.InvocationID).Scan(&invocationFactory)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NativeTask{}, ErrNotFound
		}
		return NativeTask{}, fmt.Errorf("store: insert native task: %w", err)
	}
	if invocationFactory != in.FactoryID {
		return NativeTask{}, ErrNotFound
	}
	if err := assertNativeRefs(ctx, tx, id, in); err != nil {
		return NativeTask{}, err
	}
	task, err := scanNativeTask(tx.QueryRow(ctx, `
		INSERT INTO native_tasks (
		    id, task_ref, factory_id, invocation_id, parent_task_id, assignee,
		    blocker_ids, activation_id, execution_id, task_status, budget_scope)
		VALUES ($1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING `+nativeTaskColumns,
		id, in.FactoryID, in.InvocationID, nullableText(in.ParentTaskID), in.Assignee,
		in.BlockerIDs, nullableText(in.ActivationID), nullableText(in.ExecutionID),
		NativeTaskPending, in.BudgetScope,
	))
	if err != nil {
		return NativeTask{}, fmt.Errorf("store: insert native task: %w", err)
	}
	return task, nil
}

// GetNativeTask returns the task when it belongs to factoryID.
func (s *Store) GetNativeTask(ctx context.Context, factoryID, taskID string) (NativeTask, error) {
	task, err := scanNativeTask(s.pool.QueryRow(ctx, `
		SELECT `+nativeTaskColumns+`
		FROM native_tasks
		WHERE id = $1 AND factory_id = $2`, taskID, factoryID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return NativeTask{}, ErrNotFound
		}
		return NativeTask{}, fmt.Errorf("store: get native task: %w", err)
	}
	return task, nil
}

func validateNativeTask(id string, in NativeTaskInput) error {
	if in.FactoryID == "" || in.InvocationID == "" || strings.TrimSpace(in.Assignee) == "" {
		return fmt.Errorf("%w: factory, invocation, and assignee are required", ErrInputInvalid)
	}
	if !validPrefixedID(in.InvocationID, PrefixInvocation) {
		return fmt.Errorf("%w: invocation id", ErrInputInvalid)
	}
	if looksLikeRecordID(in.Assignee) {
		return fmt.Errorf("%w: assignee is not a task, activation, or execution id", ErrInputInvalid)
	}
	if in.ParentTaskID != "" && !validPrefixedID(in.ParentTaskID, PrefixNativeTask) {
		return fmt.Errorf("%w: parent task id", ErrInputInvalid)
	}
	if in.ActivationID != "" && !validPrefixedID(in.ActivationID, PrefixNativeActivation) {
		return fmt.Errorf("%w: activation id", ErrInputInvalid)
	}
	if in.ExecutionID != "" && !validPrefixedID(in.ExecutionID, PrefixExec) {
		return fmt.Errorf("%w: execution id", ErrInputInvalid)
	}
	for _, blocker := range in.BlockerIDs {
		if !validPrefixedID(blocker, PrefixNativeTask) {
			return fmt.Errorf("%w: blocker id", ErrInputInvalid)
		}
	}
	if !identitiesDistinct(id, in.Assignee, in.ParentTaskID, in.ActivationID, in.ExecutionID, in.BlockerIDs) {
		return fmt.Errorf("%w: task, assignee, blockers, activation, and execution must be distinct", ErrInputInvalid)
	}
	return nil
}

func assertNativeRefs(ctx context.Context, tx pgx.Tx, id string, in NativeTaskInput) error {
	if in.ParentTaskID != "" {
		if err := requireSiblingTask(ctx, tx, in.ParentTaskID, in); err != nil {
			return err
		}
	}
	seen := map[string]struct{}{}
	for _, blocker := range in.BlockerIDs {
		if _, ok := seen[blocker]; ok {
			return fmt.Errorf("%w: repeated blocker", ErrInputInvalid)
		}
		seen[blocker] = struct{}{}
		if blocker == id {
			return fmt.Errorf("%w: task cannot block itself", ErrInputInvalid)
		}
		if err := requireSiblingTask(ctx, tx, blocker, in); err != nil {
			return err
		}
	}
	return nil
}

func requireSiblingTask(ctx context.Context, tx pgx.Tx, taskID string, in NativeTaskInput) error {
	var factoryID, invocationID string
	err := tx.QueryRow(ctx, `
		SELECT factory_id, invocation_id FROM native_tasks WHERE id = $1`, taskID).Scan(&factoryID, &invocationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: native task %s", ErrInputInvalid, taskID)
		}
		return err
	}
	if factoryID != in.FactoryID || invocationID != in.InvocationID {
		return fmt.Errorf("%w: native task %s is outside this invocation", ErrInputInvalid, taskID)
	}
	return nil
}

func identitiesDistinct(id, assignee, parent, activation, execution string, blockers []string) bool {
	if assignee == "" {
		return false
	}
	seen := map[string]struct{}{id: {}, assignee: {}}
	if len(seen) != 2 {
		return false
	}
	for _, field := range []string{parent, activation, execution} {
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			return false
		}
		seen[field] = struct{}{}
	}
	for _, blocker := range blockers {
		if _, ok := seen[blocker]; ok {
			return false
		}
		seen[blocker] = struct{}{}
	}
	return true
}

func validPrefixedID(id, prefix string) bool {
	body, ok := strings.CutPrefix(id, prefix)
	if !ok || len(body) != 26 {
		return false
	}
	for _, r := range body {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) {
			return false
		}
	}
	return true
}

func looksLikeRecordID(value string) bool {
	prefixes := []string{
		PrefixNativeTask, PrefixNativeActivation, PrefixNativeMessage, PrefixNativeDelivery,
		PrefixExec, PrefixRun, PrefixInvocation, PrefixWorkItem,
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
