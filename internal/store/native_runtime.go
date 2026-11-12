package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/definition"
)

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
	ready, err := definition.CheckBundle(in.Tree)
	if err != nil {
		return NativePolicy{}, NativeTask{}, fmt.Errorf("%w: %w", ErrInputInvalid, err)
	}
	in.Tree = ready
	checked, err := definition.ValidateNativeTree(in.Tree)
	if err != nil {
		return NativePolicy{}, NativeTask{}, err
	}
	body, err := marshalNativePersonas(in.Tree)
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
		SourceCommit:          in.Tree.SourceCommit,
		RosterSHA256:          in.Tree.RosterSHA256,
		RosterGitBlob:         in.Tree.RosterGitBlob,
		BundleMode:            in.Tree.BundleMode,
		Synthetic:             in.Tree.Synthetic,
		SyntheticStored:       true,
		Implementer:           in.Tree.Implementer,
		Reviewer:              in.Tree.Reviewer,
		RosterPath:            in.Tree.RosterPath,
		PathTemplate:          in.Tree.PathTemplate,
		SeatFiles:             append([]string{}, in.Tree.SeatFiles...),
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
	// A row opened before the flag was stored has SyntheticStored false.
	// Its digest was computed with Synthetic true, so reload supplies that.
	synthetic := true
	if p.SyntheticStored {
		synthetic = p.Synthetic
	}
	return definition.NativeTree{
		NativeHierarchy:       p.NativeHierarchy,
		Personas:              p.Personas,
		MaxDepth:              p.MaxDepth,
		AbsoluteMaxDepth:      p.AbsoluteMaxDepth,
		AllowAncestorShortcut: p.AllowAncestorShortcut,
		Delegation:            p.Delegation,
		Synthetic:             synthetic,
		SourceCommit:          p.SourceCommit,
		RosterSHA256:          p.RosterSHA256,
		RosterGitBlob:         p.RosterGitBlob,
		BundleMode:            p.BundleMode,
		Implementer:           p.Implementer,
		Reviewer:              p.Reviewer,
		RosterPath:            p.RosterPath,
		PathTemplate:          p.PathTemplate,
		SeatFiles:             append([]string{}, p.SeatFiles...),
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
	doc, err := unmarshalNativePersonas(personas)
	if err != nil {
		return NativePolicy{}, err
	}
	policy.Personas = doc.Personas
	policy.SourceCommit = doc.SourceCommit
	policy.RosterSHA256 = doc.RosterSHA256
	policy.RosterGitBlob = doc.RosterGitBlob
	policy.BundleMode = doc.BundleMode
	policy.Synthetic = doc.Synthetic
	policy.SyntheticStored = doc.SyntheticStored
	policy.Implementer = doc.Implementer
	policy.Reviewer = doc.Reviewer
	policy.RosterPath = doc.RosterPath
	policy.PathTemplate = doc.PathTemplate
	policy.SeatFiles = doc.SeatFiles
	return policy, nil
}

type personaEnvelope struct {
	Personas        []definition.NativePersona
	SourceCommit    string
	RosterSHA256    string
	RosterGitBlob   string
	BundleMode      string
	Synthetic       bool
	SyntheticStored bool
	Implementer     string
	Reviewer        string
	RosterPath      string
	PathTemplate    string
	SeatFiles       []string
}

// marshalNativePersonas stores persona JSON. A legacy tree with no source pin
// and no declared seats stays a JSON array. A pinned snapshot stores the
// source commit, roster hashes, and the synthetic flag in the same jsonb
// value so reload does not keep only the digest.
func marshalNativePersonas(tree definition.NativeTree) ([]byte, error) {
	pinned := tree.SourceCommit != "" || tree.RosterSHA256 != "" || tree.RosterGitBlob != "" || tree.BundleMode != ""
	extra := tree.Implementer != "" || tree.Reviewer != "" || tree.RosterPath != "" || tree.PathTemplate != "" || len(tree.SeatFiles) > 0 || !tree.Synthetic
	if !pinned && !extra {
		return json.Marshal(tree.Personas)
	}
	syn := tree.Synthetic
	return json.Marshal(struct {
		Personas      []definition.NativePersona `json:"personas"`
		SourceCommit  string                     `json:"sourceCommit,omitempty"`
		RosterSHA256  string                     `json:"rosterSha256,omitempty"`
		RosterGitBlob string                     `json:"rosterGitBlob,omitempty"`
		BundleMode    string                     `json:"bundleMode,omitempty"`
		Synthetic     *bool                      `json:"synthetic,omitempty"`
		Implementer   string                     `json:"implementer,omitempty"`
		Reviewer      string                     `json:"reviewer,omitempty"`
		RosterPath    string                     `json:"rosterPath,omitempty"`
		PathTemplate  string                     `json:"pathTemplate,omitempty"`
		SeatFiles     []string                   `json:"seatFiles,omitempty"`
	}{
		Personas:      tree.Personas,
		SourceCommit:  tree.SourceCommit,
		RosterSHA256:  tree.RosterSHA256,
		RosterGitBlob: tree.RosterGitBlob,
		BundleMode:    tree.BundleMode,
		Synthetic:     &syn,
		Implementer:   tree.Implementer,
		Reviewer:      tree.Reviewer,
		RosterPath:    tree.RosterPath,
		PathTemplate:  tree.PathTemplate,
		SeatFiles:     tree.SeatFiles,
	})
}

func unmarshalNativePersonas(raw []byte) (personaEnvelope, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return personaEnvelope{}, fmt.Errorf("%w: personas", ErrInputInvalid)
	}
	if trimmed[0] == '[' {
		var personas []definition.NativePersona
		if err := json.Unmarshal(trimmed, &personas); err != nil {
			return personaEnvelope{}, err
		}
		mode, err := definition.CheckBundle(definition.NativeTree{Personas: personas})
		if err != nil {
			return personaEnvelope{}, fmt.Errorf("%w: %w", ErrInputInvalid, err)
		}
		if mode.BundleMode != "" {
			return personaEnvelope{}, fmt.Errorf("%w: bundle metadata", ErrInputInvalid)
		}
		return personaEnvelope{Personas: personas}, nil
	}
	var doc struct {
		Personas      []definition.NativePersona `json:"personas"`
		SourceCommit  string                     `json:"sourceCommit"`
		RosterSHA256  string                     `json:"rosterSha256"`
		RosterGitBlob string                     `json:"rosterGitBlob"`
		BundleMode    string                     `json:"bundleMode"`
		Synthetic     *bool                      `json:"synthetic"`
		Implementer   string                     `json:"implementer"`
		Reviewer      string                     `json:"reviewer"`
		RosterPath    string                     `json:"rosterPath"`
		PathTemplate  string                     `json:"pathTemplate"`
		SeatFiles     []string                   `json:"seatFiles"`
	}
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return personaEnvelope{}, err
	}
	if doc.Personas == nil {
		return personaEnvelope{}, fmt.Errorf("%w: personas", ErrInputInvalid)
	}
	ready, err := definition.CheckBundle(definition.NativeTree{
		Personas:      doc.Personas,
		SourceCommit:  doc.SourceCommit,
		RosterSHA256:  doc.RosterSHA256,
		RosterGitBlob: doc.RosterGitBlob,
		BundleMode:    doc.BundleMode,
	})
	if err != nil {
		return personaEnvelope{}, fmt.Errorf("%w: %w", ErrInputInvalid, err)
	}
	out := personaEnvelope{
		Personas:      doc.Personas,
		SourceCommit:  doc.SourceCommit,
		RosterSHA256:  doc.RosterSHA256,
		RosterGitBlob: doc.RosterGitBlob,
		BundleMode:    ready.BundleMode,
		Implementer:   doc.Implementer,
		Reviewer:      doc.Reviewer,
		RosterPath:    doc.RosterPath,
		PathTemplate:  doc.PathTemplate,
		SeatFiles:     doc.SeatFiles,
	}
	if doc.Synthetic != nil {
		out.Synthetic = *doc.Synthetic
		out.SyntheticStored = true
	}
	return out, nil
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
	// The run is queued the same way as every other worker run. Activation does
	// not insert an execution or mark the run running.
	r.ParentRunID = parentRunID
	if err := validateNewRun(r); err != nil {
		return "", err
	}
	id, err := NewID(PrefixRun)
	if err != nil {
		return "", err
	}
	if err := insertRunTx(ctx, tx, id, r); err != nil {
		return "", err
	}
	if err := notifyRun(ctx, tx, id); err != nil {
		return "", err
	}
	return id, nil
}

func reserveNativeExecutionID() (string, error) {
	return NewID(PrefixExec)
}

func finishExecTx(ctx context.Context, tx pgx.Tx, execID, state string) (int64, error) {
	if execID == "" {
		return 0, nil
	}
	revoked, err := revokedTokenHash()
	if err != nil {
		return 0, err
	}
	// Retire the worker token with the execution. A later retry or pause must
	// not authenticate as this attempt.
	tag, err := tx.Exec(ctx, `
		UPDATE executions
		SET state = $2, ended_at = clock_timestamp(), run_token_sha256 = $5
		WHERE id = $1 AND state IN ($3, $4)`,
		execID, state, ExecRunning, ExecAssigned, revoked)
	if err != nil {
		return 0, fmt.Errorf("store: finish native execution: %w", err)
	}
	return tag.RowsAffected(), nil
}

func revokedTokenHash() ([]byte, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("store: revoke run token: %w", err)
	}
	return buf, nil
}

func terminalNativeRunTx(ctx context.Context, tx pgx.Tx, runID, taskID, status, execID string) error {
	if runID == "" {
		return nil
	}
	// Queue before the run row. Lease locks the queue row, then the run.
	if err := lockRunQueueTx(ctx, tx, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		return fmt.Errorf("store: terminal native run: %w", err)
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
		SET status = $2,
		    ended_at = clock_timestamp(),
		    cancel_requested = CASE WHEN $2 = $6 THEN true ELSE cancel_requested END
		WHERE id = $1 AND status IN ($3, $4, $5)`,
		runID, status, StatusQueued, StatusAssigned, StatusRunning, StatusCancelled); err != nil {
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

func lockRunQueueTx(ctx context.Context, tx pgx.Tx, runID string) error {
	rows, err := tx.Query(ctx, `SELECT run_id FROM run_queue WHERE run_id = $1 FOR UPDATE`, runID)
	if err != nil {
		return fmt.Errorf("store: lock native queue: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("store: lock native queue: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: lock native queue: %w", err)
	}
	return nil
}

// PrepareNativeTerminal locks a native task and its queue row before the run
// row. Lease locks the queue row and then the run, so a native terminal that
// already holds the run must not take the queue lock afterwards. A run with
// no native activation locks nothing.
func PrepareNativeTerminal(ctx context.Context, tx pgx.Tx, runID string) error {
	if runID == "" {
		return nil
	}
	var taskID string
	err := tx.QueryRow(ctx, `
		SELECT task_id FROM native_activations
		WHERE run_id = $1
		ORDER BY fence DESC
		LIMIT 1`, runID).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: native terminal lock: %w", err)
	}
	if _, err := lockTask(ctx, tx, taskID); err != nil {
		return err
	}
	if _, err := latestActivationByRunTx(ctx, tx, runID); err != nil {
		return err
	}
	return lockRunQueueTx(ctx, tx, runID)
}

// ReconcileNativeTerminal writes native task evidence for a run that reached
// a terminal status on the worker path. A legacy run is unchanged. A native
// task that is not running keeps its status. Evidence is inserted once.
func ReconcileNativeTerminal(ctx context.Context, tx pgx.Tx, runID, runStatus, execID string) error {
	return reconcileNativeTerminalTx(ctx, tx, runID, runStatus, execID)
}

func reconcileNativeTerminalTx(ctx context.Context, tx pgx.Tx, runID, runStatus, execID string) error {
	nativeStatus, ok := nativeStatusForRun(runStatus)
	if !ok || runID == "" {
		return nil
	}
	act, err := latestActivationByRunTx(ctx, tx, runID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if execID == "" {
		execID = act.ExecutionID
	}
	if err := bindNativeExecutionTx(ctx, tx, act, execID); err != nil {
		return err
	}
	head, err := lockTask(ctx, tx, act.TaskID)
	if err != nil {
		return err
	}
	if err := terminalNativeRunTx(ctx, tx, runID, act.TaskID, runStatus, execID); err != nil {
		return err
	}
	if head.Status != NativeRunning {
		return nil
	}
	if act.CapacityHeld {
		if err := releaseCapacityTx(ctx, tx, head.InvocationID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks SET task_status = $2, execution_id = $3, updated_at = now()
		WHERE id = $1`, act.TaskID, nativeStatus, execID); err != nil {
		return fmt.Errorf("store: native terminal task: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_activations
		SET state = $2, capacity_held = false, execution_id = $3
		WHERE id = $1`, act.ID, nativeStatus, execID); err != nil {
		return fmt.Errorf("store: native terminal activation: %w", err)
	}
	return nil
}

func nativeStatusForRun(status string) (string, bool) {
	switch status {
	case StatusSucceeded:
		return NativeCompleted, true
	case StatusFailed:
		return NativeFailed, true
	case StatusCancelled:
		return NativeCancelled, true
	default:
		return "", false
	}
}

func latestActivationByRunTx(ctx context.Context, tx pgx.Tx, runID string) (Activation, error) {
	var act Activation
	var prior *string
	err := tx.QueryRow(ctx, `
		SELECT id, task_id, invocation_id, run_id, execution_id, fence, state,
		       stage_owner, capacity_held, prior_run_id
		FROM native_activations
		WHERE run_id = $1
		ORDER BY fence DESC
		LIMIT 1
		FOR UPDATE`, runID).Scan(
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

func bindNativeExecutionTx(ctx context.Context, tx pgx.Tx, act Activation, execID string) error {
	if execID == "" || act.ID == "" || act.ExecutionID == execID {
		return nil
	}
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM executions WHERE id = $1`, act.ExecutionID).Scan(&state)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("store: bind native execution: %w", err)
	}
	// A reserved id has no row. Rewind and lease expiry leave an orphaned,
	// failed, cancelled, or completed row. Only a live execution stays bound.
	if err == nil && (state == ExecAssigned || state == ExecRunning) {
		return fmt.Errorf("store: native execution %s is live", act.ExecutionID)
	}
	if _, err := tx.Exec(ctx, `UPDATE native_activations SET execution_id = $2 WHERE id = $1`, act.ID, execID); err != nil {
		return fmt.Errorf("store: bind native execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE native_tasks SET execution_id = $2
		WHERE id = $1 AND activation_id = $3`, act.TaskID, execID, act.ID); err != nil {
		return fmt.Errorf("store: bind native execution: %w", err)
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

func nativeRunInput(ctx context.Context, tx pgx.Tx, policy NativePolicy, fallback ClaimInput, repo, assignee string) (NewRun, error) {
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
	prompt, goalWork, err := nativePrompt(ctx, tx, policy, fallback, assignee)
	if err != nil {
		return NewRun{}, err
	}
	if work == "" {
		work = goalWork
	}
	return NewRun{
		FactoryID:  policy.FactoryID,
		AgentID:    agent,
		Pool:       "default",
		Priority:   PriorityInteractive,
		Prompt:     prompt,
		Title:      "native activation",
		Repo:       repo,
		WorkItemID: work,
		Stage:      stage,
	}, nil
}

func nativePrompt(ctx context.Context, tx pgx.Tx, policy NativePolicy, fallback ClaimInput, assignee string) (string, string, error) {
	switch policy.BundleMode {
	case "":
		if policy.SourceCommit != "" || policy.RosterSHA256 != "" || policy.RosterGitBlob != "" {
			return "", "", fmt.Errorf("%w: bundle metadata", ErrInputInvalid)
		}
		prompt, err := definition.NativeSeatPrompt(policy.Personas, assignee)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrInputInvalid, err)
		}
		return prompt, "", nil
	case definition.BundleModeRoleFiles:
		seat, err := definition.NativeSeatPrompt(policy.Personas, assignee)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrInputInvalid, err)
		}
		goal, err := loadNativeGoal(ctx, tx, policy, fallback)
		if err != nil {
			return "", "", err
		}
		prompt, err := definition.AppendNativeGoal(seat, goal)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrInputInvalid, err)
		}
		return prompt, goal.WorkItemID, nil
	default:
		return "", "", fmt.Errorf("%w: unknown bundle mode", ErrInputInvalid)
	}
}

func loadNativeGoal(ctx context.Context, tx pgx.Tx, policy NativePolicy, fallback ClaimInput) (definition.NativeGoal, error) {
	var pin, repo, work string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(pinned_head_sha, ''), COALESCE(target_repository, ''), COALESCE(work_item_id, '')
		FROM invocations WHERE id = $1`, policy.InvocationID).Scan(&pin, &repo, &work)
	if errors.Is(err, pgx.ErrNoRows) {
		return definition.NativeGoal{}, fmt.Errorf("%w: invocation", ErrInputInvalid)
	}
	if err != nil {
		return definition.NativeGoal{}, err
	}
	if work == "" {
		work = policy.WorkItemID
	}
	if work == "" {
		work = fallback.WorkItemID
	}
	var title, body string
	if work != "" {
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(title, ''), COALESCE(body, '')
			FROM work_items WHERE id = $1`, work).Scan(&title, &body)
		if errors.Is(err, pgx.ErrNoRows) {
			return definition.NativeGoal{}, fmt.Errorf("%w: work item", ErrInputInvalid)
		}
		if err != nil {
			return definition.NativeGoal{}, err
		}
	}
	return definition.NativeGoal{
		WorkItemID:    work,
		InvocationID:  policy.InvocationID,
		TaskID:        fallback.TaskID,
		Repository:    repo,
		PinnedHeadSHA: pin,
		Title:         title,
		Body:          body,
	}, nil
}

func invocationRepoTx(ctx context.Context, tx pgx.Tx, invocationID string) (string, error) {
	var repo string
	err := tx.QueryRow(ctx, `SELECT COALESCE(target_repository, '') FROM invocations WHERE id = $1`, invocationID).Scan(&repo)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: native target: %w", err)
	}
	return repo, nil
}

// requeueNativeRunTx puts an existing non-terminal native run back on the
// one queue row workers lease. Park and retry keep that same run.
func requeueNativeRunTx(ctx context.Context, tx pgx.Tx, runID string) error {
	if err := lockRunQueueTx(ctx, tx, runID); err != nil {
		return err
	}
	n, err := markRunQueuedTx(ctx, tx, runID, true)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: native run is terminal", ErrInputInvalid)
	}
	n, err = touchRunQueueTx(ctx, tx, runID, false)
	if err != nil {
		return err
	}
	if n == 0 {
		if err := insertRunQueueTx(ctx, tx, runID); err != nil {
			return err
		}
	}
	return notifyRun(ctx, tx, runID)
}

// fencePausedNativeRunTx makes a paused task's run impossible to start or
// finish as success. A live worker execution is cancelled and its token is
// replaced before the caller releases capacity.
func fencePausedNativeRunTx(ctx context.Context, tx pgx.Tx, runID string) error {
	if runID == "" {
		return nil
	}
	if err := lockRunQueueTx(ctx, tx, runID); err != nil {
		return err
	}
	revoked, err := revokedTokenHash()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE executions
		SET state = $2, ended_at = clock_timestamp(), run_token_sha256 = $3
		WHERE run_id = $1 AND state IN ($4, $5)`,
		runID, ExecCancelled, revoked, ExecAssigned, ExecRunning); err != nil {
		return fmt.Errorf("store: pause native run: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		return fmt.Errorf("store: pause native run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, worker_id = NULL
		WHERE id = $1 AND status IN ($3, $4)`,
		runID, StatusQueued, StatusAssigned, StatusRunning); err != nil {
		return fmt.Errorf("store: pause native run: %w", err)
	}
	return nil
}

// parkNativeRunTx drops the queue row so a parked task cannot be leased.
// An assigned or running run returns to queued and keeps its id.
func parkNativeRunTx(ctx context.Context, tx pgx.Tx, runID string) error {
	if runID == "" {
		return nil
	}
	if err := lockRunQueueTx(ctx, tx, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		return fmt.Errorf("store: park native run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, worker_id = NULL
		WHERE id = $1 AND status IN ($3, $4)`,
		runID, StatusQueued, StatusAssigned, StatusRunning); err != nil {
		return fmt.Errorf("store: park native run: %w", err)
	}
	return nil
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
