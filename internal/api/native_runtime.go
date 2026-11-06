package api

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// NativeRuntime is the activation, delegation, and budget surface.
type NativeRuntime interface {
	OpenNativeTree(ctx context.Context, factoryUID, invocationID string, req NativeTreeRequest) (NativeTreeOpened, error)
	DelegateNative(ctx context.Context, factoryUID, taskID string, req NativeDelegateRequest) (NativeDelegateResult, error)
	ActivateNative(ctx context.Context, factoryUID, taskID string, req NativeActivateRequest) (NativeActivation, error)
	ParkNative(ctx context.Context, factoryUID, taskID string, req NativeParkRequest) (NativeActivation, error)
	WakeNativeRun(ctx context.Context, factoryUID, taskID string, req NativeWakeRunRequest) (NativeWakeRunResult, error)
	JoinNative(ctx context.Context, factoryUID, taskID string) (NativeJoinResult, error)
	CancelNativeSubtree(ctx context.Context, factoryUID, taskID string, pause bool) (NativeSubtreeResult, error)
	BoardResumeNative(ctx context.Context, factoryUID, taskID string, req NativeActivateRequest) (NativeBoardResume, error)
	CompleteNative(ctx context.Context, factoryUID, taskID string, failed bool) (NativeActivation, error)
	ReserveNative(ctx context.Context, factoryUID, invocationID string, req NativeReserveRequest) (NativeReservation, error)
	SettleNative(ctx context.Context, factoryUID, invocationID string, req NativeSettleRequest) (NativeSettlement, error)
	AdmitNative(ctx context.Context, factoryUID, invocationID string, req NativeAdmitRequest) (NativeAdmitResult, error)
	AuthorizeNativeCeiling(ctx context.Context, factoryUID string, req NativeCeilingRequest) (NativeCeilingAudit, error)
	ListNativeTrees(ctx context.Context, factoryUID string) ([]NativeTreeView, error)
}

// NativeTreeRequest freezes a roster. Principal is not a body field.
type NativeTreeRequest struct {
	Personas              []NativePersona `json:"personas"`
	MaxDepth              int             `json:"maxDepth"`
	AbsoluteMaxDepth      int             `json:"absoluteMaxDepth"`
	AllowAncestorShortcut bool            `json:"allowAncestorShortcut"`
	Delegation            string          `json:"delegation"`
	Synthetic             bool            `json:"synthetic"`
	NativeHierarchy       bool            `json:"nativeHierarchy"`
	CeilingCents          int64           `json:"ceilingCents"`
	Slots                 int             `json:"slots"`
	MaxLive               int             `json:"maxLive"`
	SharedKey             bool            `json:"sharedKey"`
	AgentID               string          `json:"agentId,omitempty"`
	WorkItemID            string          `json:"workItemId,omitempty"`
	Stage                 string          `json:"stage,omitempty"`
}

// NativePersona is one frozen roster entry.
type NativePersona struct {
	Name        string   `json:"name"`
	ReportsTo   string   `json:"reportsTo"`
	SpawnableBy []string `json:"spawnableBy"`
	Role        string   `json:"role"`
}

// NativeTreeOpened is the pinned policy and root task.
type NativeTreeOpened struct {
	Digest       string `json:"digest"`
	StageOwner   string `json:"stageOwner"`
	RootTaskID   string `json:"rootTaskId"`
	InvocationID string `json:"invocationId"`
}

// NativeDelegateRequest spawns one child.
type NativeDelegateRequest struct {
	Assignee       string     `json:"assignee"`
	IdempotencyKey string     `json:"idempotencyKey"`
	JoinPolicy     string     `json:"joinPolicy,omitempty"`
	ReserveCents   int64      `json:"reserveCents"`
	UsageDeadline  *time.Time `json:"usageDeadline,omitempty"`
	AgentID        string     `json:"agentId,omitempty"`
	WorkItemID     string     `json:"workItemId,omitempty"`
	Stage          string     `json:"stage,omitempty"`
}

// NativeDelegateResult is the one child for this key.
type NativeDelegateResult struct {
	ChildID          string `json:"childId"`
	ReservationID    string `json:"reservationId"`
	ReservationCents int64  `json:"reservationCents"`
	Replay           bool   `json:"replay"`
}

// NativeActivateRequest starts or resumes one activation.
type NativeActivateRequest struct {
	AgentID    string `json:"agentId,omitempty"`
	WorkItemID string `json:"workItemId,omitempty"`
	Stage      string `json:"stage,omitempty"`
}

// NativeActivation is one fenced execution.
type NativeActivation struct {
	ID           string `json:"id"`
	TaskID       string `json:"taskId"`
	RunID        string `json:"runId"`
	ExecutionID  string `json:"executionId"`
	Fence        int64  `json:"fence"`
	State        string `json:"state"`
	StageOwner   bool   `json:"stageOwner"`
	CapacityHeld bool   `json:"capacityHeld"`
	PriorRunID   string `json:"priorRunId,omitempty"`
}

// NativeParkRequest records blockers and releases the slot.
type NativeParkRequest struct {
	BlockerIDs []string `json:"blockerIds"`
}

// NativeWakeRunRequest delivers input to a live task.
type NativeWakeRunRequest struct {
	NegotiatedSteer bool `json:"negotiatedSteer"`
}

// NativeWakeRunResult is how the wake was applied.
type NativeWakeRunResult struct {
	Delivery    string           `json:"delivery"`
	Activation  NativeActivation `json:"activation"`
	PriorRunID  string           `json:"priorRunId,omitempty"`
	PriorStatus string           `json:"priorStatus,omitempty"`
	LinkedRunID string           `json:"linkedRunId,omitempty"`
}

// NativeJoinResult does not include a synthesized child.
type NativeJoinResult struct {
	ParentTaskID       string   `json:"parentTaskId"`
	Policy             string   `json:"policy"`
	Status             string   `json:"status"`
	Synthesized        bool     `json:"synthesized"`
	SpawnedReplacement bool     `json:"spawnedReplacement"`
	Unresolved         []string `json:"unresolved"`
	RunID              string   `json:"runId,omitempty"`
	StageOwner         bool     `json:"stageOwner"`
}

// NativeSubtreeResult is the cascaded set.
type NativeSubtreeResult struct {
	Root        string   `json:"root"`
	Descendants []string `json:"descendants"`
	State       string   `json:"state"`
	LaterSpawn  string   `json:"laterSpawn"`
}

// NativeBoardResume quotes the prior terminal status.
type NativeBoardResume struct {
	TaskID      string `json:"taskId"`
	PriorRunID  string `json:"priorRunId"`
	PriorStatus string `json:"priorStatus"`
	NewRunID    string `json:"newRunId"`
	AuditID     string `json:"auditId"`
}

// NativeReserveRequest holds ancestor cents.
type NativeReserveRequest struct {
	IdempotencyKey string     `json:"idempotencyKey"`
	Cents          int64      `json:"cents"`
	ScopeIDs       []string   `json:"scopeIds"`
	UsageDeadline  *time.Time `json:"usageDeadline,omitempty"`
	CeilingCents   int64      `json:"ceilingCents"`
	SharedKey      bool       `json:"sharedKey"`
	TaskID         string     `json:"taskId,omitempty"`
}

// NativeReservation is one hold.
type NativeReservation struct {
	ID       string   `json:"id"`
	Cents    int64    `json:"cents"`
	State    string   `json:"state"`
	ScopeIDs []string `json:"scopeIds"`
	Replay   bool     `json:"replay"`
}

// NativeSettleRequest counts one call and attempt.
type NativeSettleRequest struct {
	ReservationID string `json:"reservationId,omitempty"`
	CallID        string `json:"callId"`
	AttemptID     string `json:"attemptId"`
	Cents         int64  `json:"cents"`
	Failed        bool   `json:"failed"`
}

// NativeSettlement is the counted charge.
type NativeSettlement struct {
	CallID           string `json:"callId"`
	AttemptID        string `json:"attemptId"`
	CountedCents     int64  `json:"countedCents"`
	QuarantinedCents int64  `json:"quarantinedCents"`
	Duplicate        bool   `json:"duplicate"`
	Paused           bool   `json:"paused"`
	Code             string `json:"code,omitempty"`
}

// NativeAdmitRequest asks whether new spend is allowed.
type NativeAdmitRequest struct {
	ScopeIDs  []string `json:"scopeIds"`
	SharedKey bool     `json:"sharedKey"`
}

// NativeAdmitResult reports the outstanding reservation bound.
type NativeAdmitResult struct {
	Admitted            bool  `json:"admitted"`
	OvershootBoundCents int64 `json:"overshootBoundCents"`
	GuaranteesCeiling   bool  `json:"guaranteesCeiling"`
}

// NativeCeilingRequest raises one ceiling. Principal is not a body field.
type NativeCeilingRequest struct {
	ScopeID    string `json:"scopeId"`
	NewCeiling int64  `json:"newCeiling"`
}

// NativeCeilingAudit is the override. SpentCents is 0.
type NativeCeilingAudit struct {
	ID           string `json:"id"`
	ScopeID      string `json:"scopeId"`
	Principal    string `json:"principal"`
	PriorCeiling int64  `json:"priorCeiling"`
	NewCeiling   int64  `json:"newCeiling"`
	SpentCents   int64  `json:"spentCents"`
}

// NativeTreeView is the operator read.
type NativeTreeView struct {
	InvocationID string             `json:"invocationId"`
	StageOwner   string             `json:"stageOwner"`
	Nodes        []NativeTreeNode   `json:"nodes"`
	Budgets      []NativeBudget     `json:"budgets"`
	Blockers     []NativeBlockerDTO `json:"blockers"`
}

// NativeTreeNode is one task in the operator view.
type NativeTreeNode struct {
	TaskID       string `json:"taskId"`
	Assignee     string `json:"assignee"`
	ParentTaskID string `json:"parentTaskId,omitempty"`
	Status       string `json:"status"`
	Depth        int    `json:"depth"`
	StageOwner   bool   `json:"stageOwner"`
	RunID        string `json:"runId,omitempty"`
}

// NativeBudget is one ancestor account.
type NativeBudget struct {
	ScopeID           string `json:"scopeId"`
	Kind              string `json:"kind"`
	CeilingCents      int64  `json:"ceilingCents"`
	SpentCents        int64  `json:"spentCents"`
	ReservedCents     int64  `json:"reservedCents"`
	Paused            bool   `json:"paused"`
	MissingUsage      bool   `json:"missingUsage"`
	GuaranteesCeiling bool   `json:"guaranteesCeiling"`
}

// NativeBlockerDTO is one required child edge.
type NativeBlockerDTO struct {
	ParentTaskID string `json:"parentTaskId"`
	ChildTaskID  string `json:"childTaskId"`
	Policy       string `json:"policy"`
	ChildStatus  string `json:"childStatus"`
}

func (h *handler) handleOpenNativeTree(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeTreeRequest
	if !decodeNative(w, r, &req) {
		return
	}
	opened, err := rt.OpenNativeTree(r.Context(), r.PathValue("uid"), r.PathValue("invocationID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, opened)
}

func (h *handler) handleDelegateNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeDelegateRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.DelegateNative(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleActivateNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeActivateRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.ActivateNative(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleParkNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeParkRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.ParkNative(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleWakeNativeRun(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeWakeRunRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.WakeNativeRun(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleJoinNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	if !rejectNativeBody(w, r) {
		return
	}
	out, err := rt.JoinNative(r.Context(), r.PathValue("uid"), r.PathValue("taskID"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	if out.Unresolved == nil {
		out.Unresolved = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleCancelNativeSubtree(w http.ResponseWriter, r *http.Request) {
	h.finishSubtree(w, r, false)
}

func (h *handler) handlePauseNativeSubtree(w http.ResponseWriter, r *http.Request) {
	h.finishSubtree(w, r, true)
}

func (h *handler) finishSubtree(w http.ResponseWriter, r *http.Request, pause bool) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	if !rejectNativeBody(w, r) {
		return
	}
	out, err := rt.CancelNativeSubtree(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), pause)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	if out.Descendants == nil {
		out.Descendants = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleBoardResumeNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeActivateRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.BoardResumeNative(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleCompleteNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req struct {
		Failed bool `json:"failed"`
	}
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.CompleteNative(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req.Failed)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleReserveNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeReserveRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.ReserveNative(r.Context(), r.PathValue("uid"), r.PathValue("invocationID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleSettleNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeSettleRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.SettleNative(r.Context(), r.PathValue("uid"), r.PathValue("invocationID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleAdmitNative(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeAdmitRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.AdmitNative(r.Context(), r.PathValue("uid"), r.PathValue("invocationID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleAuthorizeNativeCeiling(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	var req NativeCeilingRequest
	if !decodeNative(w, r, &req) {
		return
	}
	out, err := rt.AuthorizeNativeCeiling(r.Context(), r.PathValue("uid"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleListNativeTrees(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.nativeRuntime(w)
	if !ok {
		return
	}
	out, err := rt.ListNativeTrees(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	if out == nil {
		out = []NativeTreeView{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) nativeRuntime(w http.ResponseWriter) (NativeRuntime, bool) {
	rt, ok := h.backend.(NativeRuntime)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return rt, true
}

var (
	// ErrNativeDelegation means the delegator is not the direct manager.
	ErrNativeDelegation = errors.New("delegation_denied")
	// ErrNativeDepth means the task chain is deeper than the frozen max.
	ErrNativeDepth = errors.New("depth_exceeded")
	// ErrNativeSpawn means the subtree is gated or the live cap is full.
	ErrNativeSpawn = errors.New("spawn_denied")
	// ErrNativeBudget means an ancestor ceiling, pause, or missing usage refused the spend.
	ErrNativeBudget = errors.New("budget_exceeded")
	// ErrNativeNoResurrect means a cancelled task was woken without board resume.
	ErrNativeNoResurrect = errors.New("cancelled_no_resurrect")
)
