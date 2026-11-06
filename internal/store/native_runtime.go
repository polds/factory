package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/definition"
)

// nativeBookkeepingHash is 32 zero bytes. It is not a credential. It fills
// executions.run_token_sha256 for a coordination execution that the worker
// queue does not lease.
var nativeBookkeepingHash = make([]byte, 32)

type taskHead struct {
	ID           string
	FactoryID    string
	InvocationID string
	ParentTaskID string
	Assignee     string
	Status       string
	Depth        int
	StageOwner   bool
}

// OpenNativeTree freezes the roster and inserts the root task. A second call
// returns the frozen policy and does not apply a reloaded definition.
func (s *Store) OpenNativeTree(ctx context.Context, in OpenTreeInput) (NativePolicy, NativeTask, error) {
	if in.Slots < 1 || in.MaxLive < 1 {
		return NativePolicy{}, NativeTask{}, fmt.Errorf("%w: slots and max live", ErrInputInvalid)
	}
	if in.Stage == "" {
		in.Stage = StageBuilding
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockInvocation(ctx, tx, in.FactoryID, in.InvocationID); err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	if existing, err := loadPolicyTx(ctx, tx, in.InvocationID); err == nil {
		task, taskErr := rootTaskTx(ctx, tx, in.InvocationID)
		if taskErr != nil {
			return NativePolicy{}, NativeTask{}, taskErr
		}
		if err := tx.Commit(ctx); err != nil {
			return NativePolicy{}, NativeTask{}, err
		}
		return existing, task, nil
	} else if !errors.Is(err, ErrNotFound) {
		return NativePolicy{}, NativeTask{}, err
	}
	if !in.Tree.NativeHierarchy {
		return NativePolicy{}, NativeTask{}, fmt.Errorf("%w: native hierarchy is off", ErrInputInvalid)
	}
	checked, err := definition.ValidateNativeTree(in.Tree)
	if err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	body, err := json.Marshal(in.Tree.Personas)
	if err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	policy := NativePolicy{
		InvocationID:          in.InvocationID,
		FactoryID:             in.FactoryID,
		Digest:                checked.Digest,
		NativeHierarchy:       true,
		MaxDepth:              in.Tree.MaxDepth,
		AbsoluteMaxDepth:      in.Tree.AbsoluteMaxDepth,
		AllowAncestorShortcut: in.Tree.AllowAncestorShortcut,
		Delegation:            in.Tree.Delegation,
		StageOwner:            checked.Root,
		Personas:              in.Tree.Personas,
		CeilingCents:          in.CeilingCents,
		CapacitySlots:         in.Slots,
		MaxLive:               in.MaxLive,
		SharedKey:             in.SharedKey,
		AgentID:               in.AgentID,
		WorkItemID:            in.WorkItemID,
		Stage:                 in.Stage,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_policies (
		    invocation_id, factory_id, digest, native_hierarchy, max_depth,
		    absolute_max_depth, allow_ancestor_shortcut, delegation, stage_owner,
		    personas, ceiling_cents, capacity_slots, max_live, shared_key,
		    agent_id, work_item_id, stage)
		VALUES ($1,$2,$3,true,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		policy.InvocationID, policy.FactoryID, policy.Digest, policy.MaxDepth,
		policy.AbsoluteMaxDepth, policy.AllowAncestorShortcut, policy.Delegation,
		policy.StageOwner, body, policy.CeilingCents, policy.CapacitySlots,
		policy.MaxLive, policy.SharedKey, policy.AgentID, policy.WorkItemID, policy.Stage,
	); err != nil {
		return NativePolicy{}, NativeTask{}, fmt.Errorf("store: native policy: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_capacity (invocation_id, slots, held)
		VALUES ($1, $2, 0)`, in.InvocationID, in.Slots); err != nil {
		return NativePolicy{}, NativeTask{}, fmt.Errorf("store: native capacity: %w", err)
	}
	root := NativeTaskInput{
		FactoryID:    in.FactoryID,
		InvocationID: in.InvocationID,
		Assignee:     checked.Root,
	}
	task, err := insertNativeTaskTx(ctx, tx, root)
	if err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks
		SET task_depth = 0, stage_owner = true, updated_at = now()
		WHERE id = $1`, task.ID); err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	task.Status = NativeTaskPending
	if _, err := coalesceWakeTx(ctx, tx, WakeIntent{
		TaskID:       task.ID,
		InvocationID: in.InvocationID,
		Assignee:     checked.Root,
		Reason:       "open",
	}); err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	return policy, task, nil
}

// GetNativePolicy returns the frozen roster.
func (s *Store) GetNativePolicy(ctx context.Context, invocationID string) (NativePolicy, error) {
	policy, err := loadPolicyTx(ctx, s.pool, invocationID)
	if err != nil {
		return NativePolicy{}, err
	}
	return policy, nil
}

// DelegateNative checks the manager, depth, count, cancel gate, and ancestor
// budgets in one transaction. The idempotency key inserts one child and one
// reservation. A refusal inserts neither.
func (s *Store) DelegateNative(ctx context.Context, in DelegateInput) (DelegateResult, error) {
	if in.IdempotencyKey == "" || in.Assignee == "" || in.ParentTaskID == "" {
		return DelegateResult{}, fmt.Errorf("%w: delegate key, assignee, and parent", ErrInputInvalid)
	}
	if in.JoinPolicy == "" {
		in.JoinPolicy = JoinFailParent
	}
	if in.JoinPolicy != JoinFailParent && in.JoinPolicy != JoinBlockParent {
		return DelegateResult{}, fmt.Errorf("%w: join policy", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DelegateResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockKey(ctx, tx, in.InvocationID+":"+in.IdempotencyKey); err != nil {
		return DelegateResult{}, err
	}
	if replay, ok, err := loadDelegationTx(ctx, tx, in.InvocationID, in.IdempotencyKey); err != nil {
		return DelegateResult{}, err
	} else if ok {
		if err := tx.Commit(ctx); err != nil {
			return DelegateResult{}, err
		}
		return replay, nil
	}
	policy, err := loadPolicyTx(ctx, tx, in.InvocationID)
	if err != nil {
		return DelegateResult{}, err
	}
	if err := lockPolicyTx(ctx, tx, in.InvocationID); err != nil {
		return DelegateResult{}, err
	}
	parent, err := lockTask(ctx, tx, in.ParentTaskID)
	if err != nil {
		return DelegateResult{}, err
	}
	if parent.InvocationID != in.InvocationID || parent.FactoryID != in.FactoryID {
		return DelegateResult{}, ErrNotFound
	}
	delegator := in.Delegator
	if delegator == "" {
		delegator = parent.Assignee
	}
	if delegator != parent.Assignee {
		return DelegateResult{}, definition.ErrDelegationDenied
	}
	if _, err := definition.ValidateNativeTree(policy.tree()); err != nil {
		return DelegateResult{}, err
	}
	target, ok := personaByName(policy.Personas, in.Assignee)
	if !ok {
		return DelegateResult{}, definition.ErrDelegationDenied
	}
	if err := definition.AllowDelegate(target, delegator, definition.Ancestors(personaMap(policy.Personas), in.Assignee), policy.AllowAncestorShortcut); err != nil {
		return DelegateResult{}, err
	}
	childDepth := parent.Depth + 1
	if err := definition.CheckDepth(childDepth, policy.MaxDepth, policy.AbsoluteMaxDepth); err != nil {
		return DelegateResult{}, err
	}
	if err := assertSpawnable(ctx, tx, parent); err != nil {
		return DelegateResult{}, err
	}
	var live int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM native_tasks
		WHERE invocation_id = $1
		  AND task_status IN ($2, $3, $4)`,
		in.InvocationID, NativeTaskPending, NativeRunning, NativeParked,
	).Scan(&live); err != nil {
		return DelegateResult{}, err
	}
	if live >= policy.MaxLive {
		return DelegateResult{}, fmt.Errorf("%w: live count", ErrSpawnDenied)
	}
	scopes := AncestorScopes(in.FactoryID, in.InvocationID, delegator, in.Assignee)
	reservation, err := reserveTx(ctx, tx, ReserveInput{
		InvocationID:   in.InvocationID,
		TaskID:         in.ParentTaskID,
		IdempotencyKey: "delegate:" + in.IdempotencyKey,
		Cents:          in.ReserveCents,
		ScopeIDs:       scopes,
		UsageDeadline:  in.UsageDeadline,
		CeilingCents:   policy.CeilingCents,
		SharedKey:      policy.SharedKey,
	})
	if err != nil {
		return DelegateResult{}, err
	}
	child, err := insertNativeTaskTx(ctx, tx, NativeTaskInput{
		FactoryID:    in.FactoryID,
		InvocationID: in.InvocationID,
		ParentTaskID: parent.ID,
		Assignee:     in.Assignee,
	})
	if err != nil {
		return DelegateResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks
		SET task_depth = $2, stage_owner = false, updated_at = now()
		WHERE id = $1`, child.ID, childDepth); err != nil {
		return DelegateResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_delegations (
		    invocation_id, idempotency_key, parent_task_id, child_task_id, reservation_id)
		VALUES ($1, $2, $3, $4, $5)`,
		in.InvocationID, in.IdempotencyKey, parent.ID, child.ID, reservation.ID); err != nil {
		return DelegateResult{}, fmt.Errorf("store: delegation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_joins (parent_task_id, child_task_id, required, policy)
		VALUES ($1, $2, true, $3)`, parent.ID, child.ID, in.JoinPolicy); err != nil {
		return DelegateResult{}, fmt.Errorf("store: join edge: %w", err)
	}
	if _, err := coalesceWakeTx(ctx, tx, WakeIntent{
		TaskID:       child.ID,
		InvocationID: in.InvocationID,
		Assignee:     in.Assignee,
		Reason:       "delegate",
	}); err != nil {
		return DelegateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DelegateResult{}, err
	}
	return DelegateResult{Child: child, ReservationID: reservation.ID, ReservationCents: reservation.Cents}, nil
}

func (p NativePolicy) tree() definition.NativeTree {
	return definition.NativeTree{
		NativeHierarchy:       p.NativeHierarchy,
		Personas:              p.Personas,
		MaxDepth:              p.MaxDepth,
		AbsoluteMaxDepth:      p.AbsoluteMaxDepth,
		AllowAncestorShortcut: p.AllowAncestorShortcut,
		Delegation:            p.Delegation,
		Synthetic:             true,
	}
}

func personaByName(personas []definition.NativePersona, name string) (definition.NativePersona, bool) {
	for _, p := range personas {
		if p.Name == name {
			return p, true
		}
	}
	return definition.NativePersona{}, false
}

func personaMap(personas []definition.NativePersona) map[string]definition.NativePersona {
	out := make(map[string]definition.NativePersona, len(personas))
	for _, p := range personas {
		out[p.Name] = p
	}
	return out
}

// AncestorScopes are one factory line for this invocation, plus the invocation, lead, and agent accounts.
// The factory line includes the invocation id, so spend does not mix across invocations.
// Callers lock them in scope_id order.
func AncestorScopes(factoryID, invocationID, lead, agent string) []string {
	ids := []string{
		"factory:" + factoryID + ":" + invocationID,
		"invocation:" + invocationID,
		"lead:" + invocationID + ":" + lead,
		"agent:" + invocationID + ":" + agent,
	}
	sort.Strings(ids)
	return ids
}

func lockPolicyTx(ctx context.Context, tx pgx.Tx, invocationID string) error {
	var locked string
	err := tx.QueryRow(ctx, `
		SELECT invocation_id FROM native_policies WHERE invocation_id = $1 FOR UPDATE`, invocationID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: lock policy: %w", err)
	}
	return nil
}

func lockInvocation(ctx context.Context, tx pgx.Tx, factoryID, invocationID string) error {
	var got string
	err := tx.QueryRow(ctx, `
		SELECT factory_id FROM invocations WHERE id = $1 FOR UPDATE`, invocationID).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && got != factoryID) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: lock invocation: %w", err)
	}
	return nil
}

func lockKey(ctx context.Context, tx pgx.Tx, key string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("store: lock key: %w", err)
	}
	return nil
}

func lockTask(ctx context.Context, tx pgx.Tx, taskID string) (taskHead, error) {
	var head taskHead
	var parent *string
	err := tx.QueryRow(ctx, `
		SELECT id, factory_id, invocation_id, parent_task_id, assignee,
		       task_status, task_depth, stage_owner
		FROM native_tasks
		WHERE id = $1
		FOR UPDATE`, taskID).Scan(
		&head.ID, &head.FactoryID, &head.InvocationID, &parent, &head.Assignee,
		&head.Status, &head.Depth, &head.StageOwner,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return taskHead{}, ErrNotFound
	}
	if err != nil {
		return taskHead{}, fmt.Errorf("store: lock task: %w", err)
	}
	if parent != nil {
		head.ParentTaskID = *parent
	}
	return head, nil
}

func assertSpawnable(ctx context.Context, tx pgx.Tx, parent taskHead) error {
	switch parent.Status {
	case NativeTaskPending, NativeRunning, NativeParked:
	default:
		return fmt.Errorf("%w: parent %s", ErrSpawnDenied, parent.Status)
	}
	cursor := parent.ID
	seen := map[string]struct{}{}
	for cursor != "" {
		if _, ok := seen[cursor]; ok {
			return ErrSpawnDenied
		}
		seen[cursor] = struct{}{}
		var state *string
		err := tx.QueryRow(ctx, `SELECT state FROM native_subtree_gates WHERE task_id = $1`, cursor).Scan(&state)
		if err == nil {
			return fmt.Errorf("%w: gated", ErrSpawnDenied)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var parentID *string
		if err := tx.QueryRow(ctx, `SELECT parent_task_id FROM native_tasks WHERE id = $1`, cursor).Scan(&parentID); err != nil {
			return err
		}
		if parentID == nil {
			break
		}
		cursor = *parentID
	}
	return nil
}

func loadPolicyTx(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, invocationID string) (NativePolicy, error) {
	var policy NativePolicy
	var personas []byte
	err := q.QueryRow(ctx, `
		SELECT invocation_id, factory_id, digest, native_hierarchy, max_depth,
		       absolute_max_depth, allow_ancestor_shortcut, delegation, stage_owner,
		       personas, ceiling_cents, capacity_slots, max_live, shared_key,
		       agent_id, work_item_id, stage, created_at
		FROM native_policies
		WHERE invocation_id = $1`, invocationID).Scan(
		&policy.InvocationID, &policy.FactoryID, &policy.Digest, &policy.NativeHierarchy,
		&policy.MaxDepth, &policy.AbsoluteMaxDepth, &policy.AllowAncestorShortcut,
		&policy.Delegation, &policy.StageOwner, &personas, &policy.CeilingCents,
		&policy.CapacitySlots, &policy.MaxLive, &policy.SharedKey, &policy.AgentID,
		&policy.WorkItemID, &policy.Stage, &policy.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return NativePolicy{}, ErrNotFound
	}
	if err != nil {
		return NativePolicy{}, fmt.Errorf("store: native policy: %w", err)
	}
	if err := json.Unmarshal(personas, &policy.Personas); err != nil {
		return NativePolicy{}, err
	}
	return policy, nil
}

func rootTaskTx(ctx context.Context, tx pgx.Tx, invocationID string) (NativeTask, error) {
	task, err := scanNativeTask(tx.QueryRow(ctx, `
		SELECT `+nativeTaskColumns+`
		FROM native_tasks
		WHERE invocation_id = $1 AND parent_task_id IS NULL
		ORDER BY created_at, id
		LIMIT 1`, invocationID))
	if err != nil {
		return NativeTask{}, err
	}
	return task, nil
}

func loadDelegationTx(ctx context.Context, tx pgx.Tx, invocationID, key string) (DelegateResult, bool, error) {
	var childID, reservationID string
	err := tx.QueryRow(ctx, `
		SELECT child_task_id, reservation_id
		FROM native_delegations
		WHERE invocation_id = $1 AND idempotency_key = $2`, invocationID, key).Scan(&childID, &reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DelegateResult{}, false, nil
	}
	if err != nil {
		return DelegateResult{}, false, err
	}
	child, err := scanNativeTask(tx.QueryRow(ctx, `
		SELECT `+nativeTaskColumns+` FROM native_tasks WHERE id = $1`, childID))
	if err != nil {
		return DelegateResult{}, false, err
	}
	var cents int64
	if err := tx.QueryRow(ctx, `SELECT cents FROM budget_reservations WHERE id = $1`, reservationID).Scan(&cents); err != nil {
		return DelegateResult{}, false, err
	}
	return DelegateResult{Child: child, ReservationID: reservationID, ReservationCents: cents, Replay: true}, true, nil
}

func insertNativeRunTx(ctx context.Context, tx pgx.Tx, r NewRun, parentRunID string) (string, error) {
	// parent_run_id is the stage predecessor. A completed-task wake stores the
	// prior activation run here so the new run is linked. The delegation edge
	// is parent_task_id, not parent_run_id.
	r.ParentRunID = parentRunID
	if err := validateNewRun(r); err != nil {
		return "", err
	}
	id, err := NewID(PrefixRun)
	if err != nil {
		return "", err
	}
	if err := bindRunLinkage(ctx, tx, r); err != nil {
		return "", err
	}
	purpose, err := normalizePurpose(r.Purpose)
	if err != nil {
		return "", err
	}
	var agentID any
	if r.AgentID != "" {
		agentID = r.AgentID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (
		    id, factory_id, agent_id, status, priority, pool, prompt, title, repo,
		    work_item_id, parent_run_id, conversation_id, stage, model_id, purpose,
		    started_at)
		VALUES (
		    $1, $2, $3, $4, $5, $6, $7, $8, $9,
		    NULLIF($10, ''), NULLIF($11, ''), $12, $13, $14, $15, clock_timestamp())`,
		id, r.FactoryID, agentID, StatusRunning, int(r.Priority), r.Pool, r.Prompt, r.Title, r.Repo,
		r.WorkItemID, r.ParentRunID, r.ConversationID, r.Stage, r.ModelID, purpose,
	); err != nil {
		return "", fmt.Errorf("store: native run: %w", err)
	}
	return id, nil
}

func insertNativeExecTx(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	id, err := NewID(PrefixExec)
	if err != nil {
		return "", err
	}
	var attempt int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(attempt), 0) + 1 FROM executions WHERE run_id = $1`, runID).Scan(&attempt); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO executions (id, run_id, attempt, worker_id, state, run_token_sha256)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, runID, attempt, nativeWorkerID, ExecRunning, nativeBookkeepingHash); err != nil {
		return "", fmt.Errorf("store: native execution: %w", err)
	}
	return id, nil
}

func finishExecTx(ctx context.Context, tx pgx.Tx, execID, state string) error {
	if execID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE executions
		SET state = $2, ended_at = clock_timestamp()
		WHERE id = $1 AND state IN ($3, $4)`,
		execID, state, ExecRunning, ExecAssigned); err != nil {
		return fmt.Errorf("store: finish native execution: %w", err)
	}
	return nil
}

func terminalNativeRunTx(ctx context.Context, tx pgx.Tx, runID, taskID, status, execID string) error {
	if runID == "" {
		return nil
	}
	var existing string
	err := tx.QueryRow(ctx, `SELECT status FROM native_terminal_evidence WHERE run_id = $1`, runID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, ended_at = clock_timestamp()
		WHERE id = $1 AND status IN ($3, $4, $5)`,
		runID, status, StatusQueued, StatusAssigned, StatusRunning); err != nil {
		return fmt.Errorf("store: terminal native run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_terminal_evidence (run_id, task_id, status, execution_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (run_id) DO NOTHING`, runID, taskID, status, execID); err != nil {
		return fmt.Errorf("store: terminal evidence: %w", err)
	}
	return nil
}

func acquireCapacityTx(ctx context.Context, tx pgx.Tx, invocationID string) error {
	var held int
	err := tx.QueryRow(ctx, `
		UPDATE native_capacity
		SET held = held + 1
		WHERE invocation_id = $1 AND held < slots
		RETURNING held`, invocationID).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: capacity", ErrSpawnDenied)
	}
	if err != nil {
		return fmt.Errorf("store: acquire capacity: %w", err)
	}
	return nil
}

func releaseCapacityTx(ctx context.Context, tx pgx.Tx, invocationID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE native_capacity
		SET held = held - 1
		WHERE invocation_id = $1 AND held > 0`, invocationID); err != nil {
		return fmt.Errorf("store: release capacity: %w", err)
	}
	return nil
}

func latestActivationTx(ctx context.Context, tx pgx.Tx, taskID string) (Activation, error) {
	var act Activation
	var prior *string
	err := tx.QueryRow(ctx, `
		SELECT id, task_id, invocation_id, run_id, execution_id, fence, state,
		       stage_owner, capacity_held, prior_run_id
		FROM native_activations
		WHERE task_id = $1
		ORDER BY fence DESC
		LIMIT 1
		FOR UPDATE`, taskID).Scan(
		&act.ID, &act.TaskID, &act.InvocationID, &act.RunID, &act.ExecutionID,
		&act.Fence, &act.State, &act.StageOwner, &act.CapacityHeld, &prior,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Activation{}, ErrNotFound
	}
	if err != nil {
		return Activation{}, err
	}
	if prior != nil {
		act.PriorRunID = *prior
	}
	return act, nil
}

func insertActivationTx(ctx context.Context, tx pgx.Tx, act Activation) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_activations (
		    id, task_id, invocation_id, run_id, execution_id, fence, state,
		    stage_owner, capacity_held, prior_run_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10, ''))`,
		act.ID, act.TaskID, act.InvocationID, act.RunID, act.ExecutionID, act.Fence,
		act.State, act.StageOwner, act.CapacityHeld, act.PriorRunID); err != nil {
		return fmt.Errorf("store: activation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks
		SET task_status = $2, activation_id = $3, execution_id = $4, updated_at = now()
		WHERE id = $1`, act.TaskID, act.State, act.ID, act.ExecutionID); err != nil {
		return err
	}
	return nil
}

func nativeRunInput(policy NativePolicy, fallback ClaimInput) NewRun {
	agent := fallback.AgentID
	if agent == "" {
		agent = policy.AgentID
	}
	work := fallback.WorkItemID
	if work == "" {
		work = policy.WorkItemID
	}
	stage := fallback.Stage
	if stage == "" {
		stage = policy.Stage
	}
	return NewRun{
		FactoryID:  policy.FactoryID,
		AgentID:    agent,
		Pool:       "default",
		Priority:   PriorityInteractive,
		Prompt:     "native activation",
		Title:      "native activation",
		WorkItemID: work,
		Stage:      stage,
	}
}

// NativeRunMayAdvance reports whether a terminal run may move the work item.
// A native child is known and may not. A run with no native activation is legacy.
func (s *Store) NativeRunMayAdvance(ctx context.Context, runID string) (may bool, known bool, err error) {
	var owner bool
	err = s.pool.QueryRow(ctx, `
		SELECT stage_owner
		FROM native_activations
		WHERE run_id = $1
		ORDER BY fence DESC
		LIMIT 1`, runID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("store: stage owner: %w", err)
	}
	return owner, true, nil
}

// ListNativeTrees returns operator views for one factory.
func (s *Store) ListNativeTrees(ctx context.Context, factoryID string) ([]NativeTreeView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT invocation_id FROM native_policies
		WHERE factory_id = $1
		ORDER BY created_at DESC
		LIMIT 20`, factoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]NativeTreeView, 0, len(ids))
	for _, id := range ids {
		view, err := s.nativeTree(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Store) nativeTree(ctx context.Context, invocationID string) (NativeTreeView, error) {
	policy, err := loadPolicyTx(ctx, s.pool, invocationID)
	if err != nil {
		return NativeTreeView{}, err
	}
	view := NativeTreeView{InvocationID: invocationID, StageOwner: policy.StageOwner, Nodes: []NativeTreeNode{}, Budgets: []BudgetScope{}, Blockers: []NativeBlocker{}}
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.assignee, COALESCE(t.parent_task_id, ''), t.task_status,
		       t.task_depth, t.stage_owner, COALESCE(a.run_id, '')
		FROM native_tasks t
		LEFT JOIN LATERAL (
		    SELECT run_id FROM native_activations
		    WHERE task_id = t.id
		    ORDER BY fence DESC
		    LIMIT 1
		) a ON true
		WHERE t.invocation_id = $1
		ORDER BY t.task_depth, t.id`, invocationID)
	if err != nil {
		return NativeTreeView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var node NativeTreeNode
		if err := rows.Scan(&node.TaskID, &node.Assignee, &node.ParentTaskID, &node.Status, &node.Depth, &node.StageOwner, &node.RunID); err != nil {
			return NativeTreeView{}, err
		}
		view.Nodes = append(view.Nodes, node)
	}
	if err := rows.Err(); err != nil {
		return NativeTreeView{}, err
	}
	brows, err := s.pool.Query(ctx, `
		SELECT scope_id, kind, ceiling_cents, spent_cents, reserved_cents,
		       paused, missing_usage, guarantees_ceiling, shared_key
		FROM budget_scopes
		WHERE invocation_id = $1
		ORDER BY scope_id`, invocationID)
	if err != nil {
		return NativeTreeView{}, err
	}
	defer brows.Close()
	for brows.Next() {
		var scope BudgetScope
		if err := brows.Scan(&scope.ScopeID, &scope.Kind, &scope.CeilingCents, &scope.SpentCents, &scope.ReservedCents, &scope.Paused, &scope.MissingUsage, &scope.GuaranteesCeiling, &scope.SharedKey); err != nil {
			return NativeTreeView{}, err
		}
		view.Budgets = append(view.Budgets, scope)
	}
	if err := brows.Err(); err != nil {
		return NativeTreeView{}, err
	}
	jrows, err := s.pool.Query(ctx, `
		SELECT j.parent_task_id, j.child_task_id, j.policy, c.task_status
		FROM native_joins j
		JOIN native_tasks c ON c.id = j.child_task_id
		JOIN native_tasks p ON p.id = j.parent_task_id
		WHERE p.invocation_id = $1
		ORDER BY j.parent_task_id, j.child_task_id`, invocationID)
	if err != nil {
		return NativeTreeView{}, err
	}
	defer jrows.Close()
	for jrows.Next() {
		var edge NativeBlocker
		if err := jrows.Scan(&edge.ParentTaskID, &edge.ChildTaskID, &edge.Policy, &edge.ChildStatus); err != nil {
			return NativeTreeView{}, err
		}
		view.Blockers = append(view.Blockers, edge)
	}
	return view, jrows.Err()
}

// CapacityHeld is the number of native slots currently taken.
func (s *Store) CapacityHeld(ctx context.Context, invocationID string) (held, slots int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT held, slots FROM native_capacity WHERE invocation_id = $1`, invocationID).Scan(&held, &slots)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	return held, slots, err
}
