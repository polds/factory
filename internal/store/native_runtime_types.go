package store

import (
	"errors"
	"time"

	"github.com/polds/factory/internal/definition"
)

const (
	NativeRunning   = "running"
	NativeParked    = "parked"
	NativeCompleted = "completed"
	NativeCancelled = "cancelled"
	NativePaused    = "paused"
	NativeFailed    = "failed"
	NativeBlocked   = "blocked"

	JoinFailParent  = "fail_parent"
	JoinBlockParent = "block_parent"

	// CeilingPrincipal is the audited actor for an authorized ceiling change.
	CeilingPrincipal = "api_key:ops"
)

var (
	// ErrSpawnDenied means the subtree is cancelled or paused, or the live cap is full.
	ErrSpawnDenied = errors.New("spawn_denied")
	// ErrBudgetExceeded means an ancestor ceiling, pause, or missing usage refused the spend.
	ErrBudgetExceeded = errors.New("budget_exceeded")
	// ErrNoResurrect means a cancelled task was woken without board resume.
	ErrNoResurrect = errors.New("cancelled_no_resurrect")
)

// BudgetDenied is a refused admission. Bound is the outstanding reservation.
type BudgetDenied struct {
	Bound int64
}

func (e *BudgetDenied) Error() string { return ErrBudgetExceeded.Error() }

func (e *BudgetDenied) Unwrap() error { return ErrBudgetExceeded }

// NativePolicy is the roster frozen on an invocation.
type NativePolicy struct {
	InvocationID          string
	FactoryID             string
	Digest                string
	NativeHierarchy       bool
	MaxDepth              int
	AbsoluteMaxDepth      int
	AllowAncestorShortcut bool
	Delegation            string
	StageOwner            string
	Personas              []definition.NativePersona
	CeilingCents          int64
	CapacitySlots         int
	MaxLive               int
	SharedKey             bool
	AgentID               string
	WorkItemID            string
	Stage                 string
	// SourceCommit, RosterSHA256, and RosterGitBlob are the pinned snapshot
	// identity stored in the personas jsonb envelope. They are empty for a
	// legacy witness policy.
	SourceCommit  string
	RosterSHA256  string
	RosterGitBlob string
	// BundleMode is the personas envelope mode. Empty is a legacy array policy.
	// role_files is inferred for a complete stored bundle that predates the field.
	BundleMode string
	CreatedAt  time.Time
}

// OpenTreeInput freezes the roster and inserts the root task. It does not claim a run.
type OpenTreeInput struct {
	FactoryID    string
	InvocationID string
	AgentID      string
	WorkItemID   string
	Stage        string
	Tree         definition.NativeTree
	CeilingCents int64
	Slots        int
	MaxLive      int
	SharedKey    bool
}

// DelegateInput spawns one child under a manager.
type DelegateInput struct {
	FactoryID      string
	InvocationID   string
	ParentTaskID   string
	Delegator      string
	Assignee       string
	IdempotencyKey string
	AgentID        string
	WorkItemID     string
	Stage          string
	JoinPolicy     string
	ReserveCents   int64
	UsageDeadline  *time.Time
}

// DelegateResult is the one child and the one reservation for this key.
type DelegateResult struct {
	Child            NativeTask
	ReservationID    string
	ReservationCents int64
	Replay           bool
}

// ClaimInput starts or resumes one activation. InvocationID comes from store.GetWake.
type ClaimInput struct {
	TaskID       string
	InvocationID string
	AgentID      string
	WorkItemID   string
	Stage        string
}

// Activation is one fenced execution of a native task.
type Activation struct {
	ID           string
	TaskID       string
	InvocationID string
	RunID        string
	ExecutionID  string
	Fence        int64
	State        string
	StageOwner   bool
	CapacityHeld bool
	PriorRunID   string
	Depth        int
	Delivery     string
}

// ParkInput releases the worker slot and records blockers.
type ParkInput struct {
	TaskID     string
	BlockerIDs []string
}

// WakeInput is the activation consumer for one durable wake.
type WakeInput struct {
	TaskID          string
	InvocationID    string
	AgentID         string
	WorkItemID      string
	Stage           string
	NegotiatedSteer bool
}

// WakeResult is how the wake was applied. Prior terminal rows are not rewritten.
type WakeResult struct {
	Delivery    string
	Activation  Activation
	PriorRunID  string
	PriorStatus string
	LinkedRunID string
}

// JoinInput evaluates required children for one parent.
type JoinInput struct {
	TaskID string
}

// JoinResult never synthesizes a replacement child.
type JoinResult struct {
	ParentTaskID       string
	Policy             string
	Status             string
	Synthesized        bool
	SpawnedReplacement bool
	Unresolved         []string
	RunID              string
	StageOwner         bool
}

// SubtreeInput cancels or pauses a task and its descendants.
type SubtreeInput struct {
	TaskID string
	Pause  bool
}

// SubtreeResult is the cascaded set. LaterSpawn is spawn_denied.
type SubtreeResult struct {
	Root        string
	Descendants []string
	State       string
	LaterSpawn  string
}

// BoardResumeInput is an authorized resume. It does not edit the cancelled run.
type BoardResumeInput struct {
	TaskID     string
	Principal  string
	AgentID    string
	WorkItemID string
	Stage      string
}

// BoardResumeResult quotes the prior terminal status and the new run.
type BoardResumeResult struct {
	TaskID      string
	PriorRunID  string
	PriorStatus string
	NewRunID    string
	AuditID     string
}

// ReserveInput locks ancestor scopes in scope_id order.
type ReserveInput struct {
	InvocationID   string
	TaskID         string
	IdempotencyKey string
	Cents          int64
	ScopeIDs       []string
	UsageDeadline  *time.Time
	CeilingCents   int64
	SharedKey      bool
}

// Reservation is one atomic ancestor hold.
type Reservation struct {
	ID           string
	InvocationID string
	Cents        int64
	State        string
	ScopeIDs     []string
	Replay       bool
}

// SettleInput counts one call and attempt. Cents may exceed the reservation.
type SettleInput struct {
	InvocationID  string
	ReservationID string
	CallID        string
	AttemptID     string
	Cents         int64
	Failed        bool
}

// Settlement is the one counted charge. Excess is quarantined, not spent.
type Settlement struct {
	CallID           string
	AttemptID        string
	CountedCents     int64
	QuarantinedCents int64
	Duplicate        bool
	Paused           bool
	Code             string
}

// RefundInput subtracts cents once. CallID and AttemptID dedup the refund.
type RefundInput struct {
	InvocationID string
	ScopeIDs     []string
	CallID       string
	AttemptID    string
	Cents        int64
}

// AdmitInput refuses new spend when usage is missing or a scope is paused.
type AdmitInput struct {
	InvocationID string
	ScopeIDs     []string
	SharedKey    bool
	Now          time.Time
}

// AdmitResult reports the outstanding reservation as the overshoot bound.
type AdmitResult struct {
	Admitted            bool
	OvershootBoundCents int64
	GuaranteesCeiling   bool
}

// CeilingInput changes one ceiling and writes the audit row in that transaction.
type CeilingInput struct {
	ScopeID    string
	Principal  string
	NewCeiling int64
}

// CeilingAudit is the override. SpentCents on the audit is 0.
type CeilingAudit struct {
	ID           string
	ScopeID      string
	Principal    string
	PriorCeiling int64
	NewCeiling   int64
	SpentCents   int64
}

// BudgetScope is one ancestor account.
type BudgetScope struct {
	ScopeID           string
	Kind              string
	CeilingCents      int64
	SpentCents        int64
	ReservedCents     int64
	Paused            bool
	MissingUsage      bool
	GuaranteesCeiling bool
	SharedKey         bool
	Alerts            int
}

// NativeTreeNode is one task in the operator view.
type NativeTreeNode struct {
	TaskID       string
	Assignee     string
	ParentTaskID string
	Status       string
	Depth        int
	StageOwner   bool
	RunID        string
}

// NativeBlocker is one required child edge.
type NativeBlocker struct {
	ParentTaskID string
	ChildTaskID  string
	Policy       string
	ChildStatus  string
}

// NativeTreeView is the operator tree, budget, and blocker read.
type NativeTreeView struct {
	InvocationID string
	StageOwner   string
	Nodes        []NativeTreeNode
	Budgets      []BudgetScope
	Blockers     []NativeBlocker
}
