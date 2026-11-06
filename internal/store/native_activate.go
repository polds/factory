package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ClaimNativeActivation reads the wake row and starts one fenced execution.
// A second claim while the task is running is claim_fenced.
func (s *Store) ClaimNativeActivation(ctx context.Context, in ClaimInput) (Activation, error) {
	if in.TaskID == "" || in.InvocationID == "" {
		return Activation{}, fmt.Errorf("%w: claim task and invocation", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Activation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	act, err := claimTx(ctx, tx, in)
	if err != nil {
		return Activation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Activation{}, err
	}
	return act, nil
}

func claimTx(ctx context.Context, tx pgx.Tx, in ClaimInput) (Activation, error) {
	wake, err := scanWake(tx.QueryRow(ctx, `
		SELECT task_id, invocation_id, assignee, reason, pending, coalesce_count, updated_at
		FROM native_wake_intents
		WHERE task_id = $1
		FOR UPDATE`, in.TaskID))
	if err != nil {
		return Activation{}, err
	}
	if wake.InvocationID != in.InvocationID {
		return Activation{}, fmt.Errorf("%w: wake invocation", ErrInputInvalid)
	}
	head, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return Activation{}, err
	}
	if head.InvocationID != wake.InvocationID {
		return Activation{}, ErrNotFound
	}
	switch head.Status {
	case NativeRunning:
		return Activation{}, ErrClaimFenced
	case NativeCancelled:
		return Activation{}, ErrNoResurrect
	case NativeTaskPending:
		return startActivationTx(ctx, tx, head, in, "")
	case NativeParked:
		return resumeParkTx(ctx, tx, head, in)
	default:
		return Activation{}, fmt.Errorf("%w: status %s", ErrInputInvalid, head.Status)
	}
}

// ResumePark continues a parked task with a new execution of the same run.
func (s *Store) ResumePark(ctx context.Context, in ClaimInput) (Activation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Activation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return Activation{}, err
	}
	if head.Status != NativeParked {
		return Activation{}, fmt.Errorf("%w: not parked", ErrInputInvalid)
	}
	if in.InvocationID == "" {
		in.InvocationID = head.InvocationID
	}
	act, err := resumeParkTx(ctx, tx, head, in)
	if err != nil {
		return Activation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Activation{}, err
	}
	return act, nil
}

func startActivationTx(ctx context.Context, tx pgx.Tx, head taskHead, in ClaimInput, priorRun string) (Activation, error) {
	policy, err := loadPolicyTx(ctx, tx, head.InvocationID)
	if err != nil {
		return Activation{}, err
	}
	if err := acquireCapacityTx(ctx, tx, head.InvocationID); err != nil {
		return Activation{}, err
	}
	run, err := insertNativeRunTx(ctx, tx, nativeRunInput(policy, in), priorRun)
	if err != nil {
		return Activation{}, err
	}
	execID, err := insertNativeExecTx(ctx, tx, run)
	if err != nil {
		return Activation{}, err
	}
	actID, err := NewID(PrefixNativeActivation)
	if err != nil {
		return Activation{}, err
	}
	act := Activation{
		ID:           actID,
		TaskID:       head.ID,
		InvocationID: head.InvocationID,
		RunID:        run,
		ExecutionID:  execID,
		Fence:        1,
		State:        NativeRunning,
		StageOwner:   head.StageOwner,
		CapacityHeld: true,
		PriorRunID:   priorRun,
		Depth:        head.Depth,
	}
	if prior, err := latestActivationTx(ctx, tx, head.ID); err == nil {
		act.Fence = prior.Fence + 1
	} else if !errors.Is(err, ErrNotFound) {
		return Activation{}, err
	}
	if err := insertActivationTx(ctx, tx, act); err != nil {
		return Activation{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_wake_intents SET pending = false, updated_at = now() WHERE task_id = $1`, head.ID); err != nil {
		return Activation{}, err
	}
	return act, nil
}

func resumeParkTx(ctx context.Context, tx pgx.Tx, head taskHead, in ClaimInput) (Activation, error) {
	prior, err := latestActivationTx(ctx, tx, head.ID)
	if err != nil {
		return Activation{}, err
	}
	if err := acquireCapacityTx(ctx, tx, head.InvocationID); err != nil {
		return Activation{}, err
	}
	execID, err := insertNativeExecTx(ctx, tx, prior.RunID)
	if err != nil {
		return Activation{}, err
	}
	actID, err := NewID(PrefixNativeActivation)
	if err != nil {
		return Activation{}, err
	}
	act := Activation{
		ID:           actID,
		TaskID:       head.ID,
		InvocationID: head.InvocationID,
		RunID:        prior.RunID,
		ExecutionID:  execID,
		Fence:        prior.Fence + 1,
		State:        NativeRunning,
		StageOwner:   head.StageOwner,
		CapacityHeld: true,
		PriorRunID:   prior.PriorRunID,
		Depth:        head.Depth,
	}
	if err := insertActivationTx(ctx, tx, act); err != nil {
		return Activation{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_wake_intents SET pending = false, updated_at = now() WHERE task_id = $1`, head.ID); err != nil {
		return Activation{}, err
	}
	_ = in
	return act, nil
}

// ParkNative releases the worker slot and records blockers on the task.
// capacityReleased is true once the activation no longer holds a slot.
func (s *Store) ParkNative(ctx context.Context, in ParkInput) (Activation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Activation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return Activation{}, err
	}
	if head.Status != NativeRunning {
		return Activation{}, fmt.Errorf("%w: park requires running", ErrInputInvalid)
	}
	act, err := latestActivationTx(ctx, tx, head.ID)
	if err != nil {
		return Activation{}, err
	}
	if err := finishExecTx(ctx, tx, act.ExecutionID, ExecCompleted); err != nil {
		return Activation{}, err
	}
	if act.CapacityHeld {
		if err := releaseCapacityTx(ctx, tx, head.InvocationID); err != nil {
			return Activation{}, err
		}
	}
	blockers := in.BlockerIDs
	if blockers == nil {
		blockers = []string{}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks
		SET task_status = $2, blocker_ids = $3, updated_at = now()
		WHERE id = $1`, head.ID, NativeParked, blockers); err != nil {
		return Activation{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_activations
		SET state = $2, capacity_held = false
		WHERE id = $1`, act.ID, NativeParked); err != nil {
		return Activation{}, err
	}
	act.State = NativeParked
	act.CapacityHeld = false
	if err := tx.Commit(ctx); err != nil {
		return Activation{}, err
	}
	return act, nil
}

// ConsumeNativeWake applies one durable wake.
// negotiatedSteer false keeps a running recipient pending instead of steering it.
func (s *Store) ConsumeNativeWake(ctx context.Context, in WakeInput) (WakeResult, error) {
	if in.InvocationID == "" {
		return WakeResult{}, fmt.Errorf("%w: wake invocation", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WakeResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return WakeResult{}, err
	}
	if head.InvocationID != in.InvocationID {
		return WakeResult{}, fmt.Errorf("%w: wake invocation", ErrInputInvalid)
	}
	var result WakeResult
	switch head.Status {
	case NativeRunning:
		delivery := "pending"
		if in.NegotiatedSteer {
			delivery = "steer"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO native_input (task_id, delivery, negotiated_steer)
			VALUES ($1, $2, $3)
			ON CONFLICT (task_id) DO UPDATE
			SET delivery = EXCLUDED.delivery,
			    negotiated_steer = EXCLUDED.negotiated_steer,
			    updated_at = now()`, head.ID, delivery, in.NegotiatedSteer); err != nil {
			return WakeResult{}, err
		}
		if delivery == "steer" {
			if _, err := tx.Exec(ctx, `
				UPDATE native_wake_intents SET pending = false, updated_at = now() WHERE task_id = $1`, head.ID); err != nil {
				return WakeResult{}, err
			}
		}
		act, err := latestActivationTx(ctx, tx, head.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return WakeResult{}, err
		}
		act.Delivery = delivery
		result = WakeResult{Delivery: delivery, Activation: act}
	case NativeParked:
		act, err := resumeParkTx(ctx, tx, head, ClaimInput{
			TaskID: head.ID, InvocationID: head.InvocationID, AgentID: in.AgentID,
			WorkItemID: in.WorkItemID, Stage: in.Stage,
		})
		if err != nil {
			return WakeResult{}, err
		}
		result = WakeResult{Delivery: "resumed", Activation: act}
	case NativeCompleted:
		result, err = wakeCompletedTx(ctx, tx, head, in)
		if err != nil {
			return WakeResult{}, err
		}
	case NativeCancelled:
		return WakeResult{}, ErrNoResurrect
	case NativeTaskPending:
		act, err := claimTx(ctx, tx, ClaimInput{
			TaskID: head.ID, InvocationID: in.InvocationID, AgentID: in.AgentID,
			WorkItemID: in.WorkItemID, Stage: in.Stage,
		})
		if err != nil {
			return WakeResult{}, err
		}
		result = WakeResult{Delivery: "activated", Activation: act}
	default:
		return WakeResult{}, fmt.Errorf("%w: status %s", ErrInputInvalid, head.Status)
	}
	if err := tx.Commit(ctx); err != nil {
		return WakeResult{}, err
	}
	return result, nil
}

// WakeCompleted opens a new linked run. The prior terminal row stays immutable.
func (s *Store) WakeCompleted(ctx context.Context, in WakeInput) (WakeResult, error) {
	if in.TaskID == "" || in.InvocationID == "" {
		return WakeResult{}, fmt.Errorf("%w: wake invocation", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WakeResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return WakeResult{}, err
	}
	if head.Status != NativeCompleted || head.InvocationID != in.InvocationID {
		return WakeResult{}, fmt.Errorf("%w: not completed", ErrInputInvalid)
	}
	result, err := wakeCompletedTx(ctx, tx, head, in)
	if err != nil {
		return WakeResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WakeResult{}, err
	}
	return result, nil
}

func wakeCompletedTx(ctx context.Context, tx pgx.Tx, head taskHead, in WakeInput) (WakeResult, error) {
	prior, err := latestActivationTx(ctx, tx, head.ID)
	if err != nil {
		return WakeResult{}, err
	}
	var priorStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, prior.RunID).Scan(&priorStatus); err != nil {
		return WakeResult{}, err
	}
	var evidence string
	if err := tx.QueryRow(ctx, `SELECT status FROM native_terminal_evidence WHERE run_id = $1`, prior.RunID).Scan(&evidence); err != nil {
		return WakeResult{}, fmt.Errorf("%w: missing terminal evidence", ErrInputInvalid)
	}
	act, err := startActivationTx(ctx, tx, head, ClaimInput{
		TaskID: head.ID, InvocationID: head.InvocationID, AgentID: in.AgentID,
		WorkItemID: in.WorkItemID, Stage: in.Stage,
	}, prior.RunID)
	if err != nil {
		return WakeResult{}, err
	}
	var after string
	if err := tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, prior.RunID).Scan(&after); err != nil {
		return WakeResult{}, err
	}
	if after != priorStatus || after != evidence {
		return WakeResult{}, fmt.Errorf("%w: terminal evidence changed", ErrImmutable)
	}
	return WakeResult{
		Delivery:    "linked",
		Activation:  act,
		PriorRunID:  prior.RunID,
		PriorStatus: priorStatus,
		LinkedRunID: act.RunID,
	}, nil
}

// RetryExecution opens a new execution of the same run.
func (s *Store) RetryExecution(ctx context.Context, taskID string) (Activation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Activation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := lockTask(ctx, tx, taskID)
	if err != nil {
		return Activation{}, err
	}
	if head.Status != NativeRunning {
		return Activation{}, fmt.Errorf("%w: retry requires running", ErrInputInvalid)
	}
	prior, err := latestActivationTx(ctx, tx, taskID)
	if err != nil {
		return Activation{}, err
	}
	if err := finishExecTx(ctx, tx, prior.ExecutionID, ExecFailed); err != nil {
		return Activation{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_activations SET capacity_held = false WHERE id = $1`, prior.ID); err != nil {
		return Activation{}, err
	}
	execID, err := insertNativeExecTx(ctx, tx, prior.RunID)
	if err != nil {
		return Activation{}, err
	}
	actID, err := NewID(PrefixNativeActivation)
	if err != nil {
		return Activation{}, err
	}
	act := prior
	act.ID = actID
	act.ExecutionID = execID
	act.Fence = prior.Fence + 1
	act.State = NativeRunning
	act.CapacityHeld = true
	if err := insertActivationTx(ctx, tx, act); err != nil {
		return Activation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Activation{}, err
	}
	return act, nil
}

// CompleteNative marks the task completed and freezes terminal evidence.
func (s *Store) CompleteNative(ctx context.Context, taskID string, failed bool) (Activation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Activation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	act, err := completeTx(ctx, tx, taskID, failed)
	if err != nil {
		return Activation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Activation{}, err
	}
	return act, nil
}

func completeTx(ctx context.Context, tx pgx.Tx, taskID string, failed bool) (Activation, error) {
	head, err := lockTask(ctx, tx, taskID)
	if err != nil {
		return Activation{}, err
	}
	act, err := latestActivationTx(ctx, tx, taskID)
	if err != nil {
		return Activation{}, err
	}
	status := NativeCompleted
	runStatus := StatusSucceeded
	execState := ExecCompleted
	if failed {
		status = NativeFailed
		runStatus = StatusFailed
		execState = ExecFailed
	}
	if err := finishExecTx(ctx, tx, act.ExecutionID, execState); err != nil {
		return Activation{}, err
	}
	if act.CapacityHeld {
		if err := releaseCapacityTx(ctx, tx, head.InvocationID); err != nil {
			return Activation{}, err
		}
	}
	if err := terminalNativeRunTx(ctx, tx, act.RunID, taskID, runStatus, act.ExecutionID); err != nil {
		return Activation{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks SET task_status = $2, updated_at = now() WHERE id = $1`, taskID, status); err != nil {
		return Activation{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_activations SET state = $2, capacity_held = false WHERE id = $1`, act.ID, status); err != nil {
		return Activation{}, err
	}
	act.State = status
	act.CapacityHeld = false
	act.StageOwner = head.StageOwner
	return act, nil
}

// JoinNative applies fail_parent or block_parent. It does not synthesize a
// replacement while required children are live, and it does not spawn one after.
func (s *Store) JoinNative(ctx context.Context, in JoinInput) (JoinResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return JoinResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return JoinResult{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT j.child_task_id, j.policy, c.task_status
		FROM native_joins j
		JOIN native_tasks c ON c.id = j.child_task_id
		WHERE j.parent_task_id = $1 AND j.required
		ORDER BY j.child_task_id`, in.TaskID)
	if err != nil {
		return JoinResult{}, err
	}
	type child struct {
		id, policy, status string
	}
	var children []child
	for rows.Next() {
		var c child
		if err := rows.Scan(&c.id, &c.policy, &c.status); err != nil {
			rows.Close()
			return JoinResult{}, err
		}
		children = append(children, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return JoinResult{}, err
	}
	result := JoinResult{ParentTaskID: head.ID, Status: head.Status, StageOwner: head.StageOwner}
	if len(children) == 0 {
		result.Policy = JoinFailParent
		if err := tx.Commit(ctx); err != nil {
			return JoinResult{}, err
		}
		return result, nil
	}
	result.Policy = children[0].policy
	var unresolved []string
	failed := false
	for _, c := range children {
		switch c.status {
		case NativeCompleted:
		case NativeFailed, NativeCancelled:
			failed = true
		default:
			unresolved = append(unresolved, c.id)
		}
	}
	result.Unresolved = unresolved
	if len(unresolved) > 0 {
		// unresolved children block synthesis. No replacement is spawned.
		if err := tx.Commit(ctx); err != nil {
			return JoinResult{}, err
		}
		return result, nil
	}
	if failed {
		next := NativeFailed
		if result.Policy == JoinBlockParent {
			next = NativeBlocked
		}
		if act, actErr := latestActivationTx(ctx, tx, head.ID); actErr == nil {
			result.RunID = act.RunID
			result.StageOwner = head.StageOwner
		}
		if _, err := tx.Exec(ctx, `
			UPDATE native_tasks SET task_status = $2, updated_at = now() WHERE id = $1`, head.ID, next); err != nil {
			return JoinResult{}, err
		}
		if next == NativeFailed {
			if _, err := completeTx(ctx, tx, head.ID, true); err != nil && !errors.Is(err, ErrNotFound) {
				return JoinResult{}, err
			}
		}
		result.Status = next
		if err := tx.Commit(ctx); err != nil {
			return JoinResult{}, err
		}
		return result, nil
	}
	if head.Status == NativeParked {
		if _, err := resumeParkTx(ctx, tx, head, ClaimInput{TaskID: head.ID, InvocationID: head.InvocationID}); err != nil {
			return JoinResult{}, err
		}
		result.Status = NativeRunning
	}
	act, err := latestActivationTx(ctx, tx, head.ID)
	if err == nil {
		result.RunID = act.RunID
		result.StageOwner = head.StageOwner
	}
	if err := tx.Commit(ctx); err != nil {
		return JoinResult{}, err
	}
	return result, nil
}

// CancelNativeSubtree cancels or pauses the task and its descendants.
// Later spawn is spawn_denied. Terminal evidence already recorded is left as it is.
func (s *Store) CancelNativeSubtree(ctx context.Context, in SubtreeInput) (SubtreeResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SubtreeResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockTask(ctx, tx, in.TaskID); err != nil {
		return SubtreeResult{}, err
	}
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE tree AS (
		    SELECT id FROM native_tasks WHERE id = $1
		    UNION ALL
		    SELECT c.id FROM native_tasks c JOIN tree t ON c.parent_task_id = t.id
		)
		SELECT id FROM tree`, in.TaskID)
	if err != nil {
		return SubtreeResult{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return SubtreeResult{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return SubtreeResult{}, err
	}
	state := NativeCancelled
	runStatus := StatusCancelled
	if in.Pause {
		state = NativePaused
		runStatus = ""
	}
	for _, id := range ids {
		head, err := lockTask(ctx, tx, id)
		if err != nil {
			return SubtreeResult{}, err
		}
		if head.Status == NativeCompleted {
			if _, err := tx.Exec(ctx, `
				INSERT INTO native_subtree_gates (task_id, state)
				VALUES ($1, $2)
				ON CONFLICT (task_id) DO NOTHING`, id, gateState(in.Pause)); err != nil {
				return SubtreeResult{}, err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE native_tasks SET task_status = $2, updated_at = now() WHERE id = $1`, id, state); err != nil {
			return SubtreeResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO native_subtree_gates (task_id, state)
			VALUES ($1, $2)
			ON CONFLICT (task_id) DO UPDATE SET state = EXCLUDED.state`, id, gateState(in.Pause)); err != nil {
			return SubtreeResult{}, err
		}
		act, err := latestActivationTx(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return SubtreeResult{}, err
		}
		if act.CapacityHeld {
			if err := releaseCapacityTx(ctx, tx, head.InvocationID); err != nil {
				return SubtreeResult{}, err
			}
		}
		if !in.Pause {
			if err := finishExecTx(ctx, tx, act.ExecutionID, ExecCancelled); err != nil {
				return SubtreeResult{}, err
			}
			if err := terminalNativeRunTx(ctx, tx, act.RunID, id, runStatus, act.ExecutionID); err != nil {
				return SubtreeResult{}, err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE native_activations
			SET state = $2, capacity_held = false
			WHERE id = $1`, act.ID, state); err != nil {
			return SubtreeResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SubtreeResult{}, err
	}
	desc := []string{}
	if len(ids) > 1 {
		desc = ids[1:]
	}
	return SubtreeResult{Root: in.TaskID, Descendants: desc, State: state, LaterSpawn: "spawn_denied"}, nil
}

func gateState(pause bool) string {
	if pause {
		return NativePaused
	}
	return NativeCancelled
}

// BoardResumeNative records an audit row and a new run. The cancelled run's terminal
// evidence is not updated.
func (s *Store) BoardResumeNative(ctx context.Context, in BoardResumeInput) (BoardResumeResult, error) {
	if in.Principal != CeilingPrincipal {
		return BoardResumeResult{}, ErrPrincipalRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BoardResumeResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return BoardResumeResult{}, err
	}
	if head.Status != NativeCancelled {
		return BoardResumeResult{}, fmt.Errorf("%w: not cancelled", ErrInputInvalid)
	}
	prior, err := latestActivationTx(ctx, tx, in.TaskID)
	if err != nil {
		return BoardResumeResult{}, err
	}
	var priorStatus string
	var ended *time.Time
	if err := tx.QueryRow(ctx, `SELECT status, ended_at FROM runs WHERE id = $1`, prior.RunID).Scan(&priorStatus, &ended); err != nil {
		return BoardResumeResult{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM native_subtree_gates WHERE task_id = $1`, in.TaskID); err != nil {
		return BoardResumeResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks SET task_status = $2, updated_at = now() WHERE id = $1`, in.TaskID, NativeTaskPending); err != nil {
		return BoardResumeResult{}, err
	}
	act, err := startActivationTx(ctx, tx, taskHead{
		ID: head.ID, FactoryID: head.FactoryID, InvocationID: head.InvocationID,
		Assignee: head.Assignee, Status: NativeTaskPending, Depth: head.Depth, StageOwner: head.StageOwner,
	}, ClaimInput{
		TaskID: in.TaskID, InvocationID: head.InvocationID, AgentID: in.AgentID,
		WorkItemID: in.WorkItemID, Stage: in.Stage,
	}, prior.RunID)
	if err != nil {
		return BoardResumeResult{}, err
	}
	auditID, err := NewID(PrefixNativeResume)
	if err != nil {
		return BoardResumeResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_board_resumes (
		    id, task_id, principal, prior_run_id, new_run_id, prior_status)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		auditID, in.TaskID, in.Principal, prior.RunID, act.RunID, priorStatus); err != nil {
		return BoardResumeResult{}, err
	}
	var after string
	var endedAfter *time.Time
	if err := tx.QueryRow(ctx, `SELECT status, ended_at FROM runs WHERE id = $1`, prior.RunID).Scan(&after, &endedAfter); err != nil {
		return BoardResumeResult{}, err
	}
	if after != priorStatus || !sameInstant(ended, endedAfter) {
		return BoardResumeResult{}, fmt.Errorf("%w: cancelled run changed", ErrImmutable)
	}
	if err := tx.Commit(ctx); err != nil {
		return BoardResumeResult{}, err
	}
	return BoardResumeResult{
		TaskID: in.TaskID, PriorRunID: prior.RunID, PriorStatus: priorStatus,
		NewRunID: act.RunID, AuditID: auditID,
	}, nil
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
