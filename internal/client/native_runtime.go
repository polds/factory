package client

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// NativeRoleFile is one frozen role file. Bytes are content, not a grant.
type NativeRoleFile struct {
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	GitBlob string `json:"gitBlob"`
	Bytes   []byte `json:"bytes"`
}

// NativePersona is one frozen roster entry.
type NativePersona struct {
	Name            string           `json:"name"`
	ReportsTo       string           `json:"reportsTo"`
	SpawnableBy     []string         `json:"spawnableBy"`
	Role            string           `json:"role"`
	SourceReportsTo string           `json:"sourceReportsTo,omitempty"`
	SourceDottedTo  string           `json:"sourceDottedTo,omitempty"`
	Files           []NativeRoleFile `json:"files,omitempty"`
}

// NativeTreeRequest freezes a roster. Principal is not a field.
type NativeTreeRequest struct {
	Personas              []NativePersona        `json:"personas"`
	MaxDepth              int                    `json:"maxDepth"`
	AbsoluteMaxDepth      int                    `json:"absoluteMaxDepth"`
	AllowAncestorShortcut bool                   `json:"allowAncestorShortcut"`
	Delegation            string                 `json:"delegation"`
	Synthetic             bool                   `json:"synthetic"`
	NativeHierarchy       bool                   `json:"nativeHierarchy"`
	CeilingCents          int64                  `json:"ceilingCents"`
	Slots                 int                    `json:"slots"`
	MaxLive               int                    `json:"maxLive"`
	SharedKey             bool                   `json:"sharedKey"`
	AgentID               string                 `json:"agentId,omitempty"`
	WorkItemID            string                 `json:"workItemId,omitempty"`
	Stage                 string                 `json:"stage,omitempty"`
	SourceCommit          string                 `json:"sourceCommit,omitempty"`
	RosterSHA256          string                 `json:"rosterSha256,omitempty"`
	RosterGitBlob         string                 `json:"rosterGitBlob,omitempty"`
	BundleMode            string                 `json:"bundleMode,omitempty"`
	Publish               *NativePublishRequest  `json:"publish,omitempty"`
	Evidence              *NativeEvidenceRequest `json:"evidence,omitempty"`
}

// NativePublishRequest narrows issue key, title, body, and ref prefix.
// It has no repo. The repo comes from the definition.
type NativePublishRequest struct {
	IssueKey  string `json:"issueKey,omitempty"`
	Title     string `json:"title,omitempty"`
	Body      string `json:"body,omitempty"`
	RefPrefix string `json:"refPrefix,omitempty"`
}

// NativeEvidenceRequest narrows the package and the mutation path.
// Sentinel and break come from the definition.
type NativeEvidenceRequest struct {
	Package  string `json:"package,omitempty"`
	Mutation string `json:"mutation,omitempty"`
}

// NativeTreeOpened is the pinned policy.
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
}

// NativeDelegateResult is the one child for the key.
type NativeDelegateResult struct {
	ChildID          string `json:"childId"`
	ReservationID    string `json:"reservationId"`
	ReservationCents int64  `json:"reservationCents"`
	Replay           bool   `json:"replay"`
}

// NativeActivateRequest starts one fenced execution.
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

// OpenNativeTree posts the frozen roster.
func (c *Client) OpenNativeTree(ctx context.Context, factoryUID, invocationID string, req NativeTreeRequest) (NativeTreeOpened, error) {
	var out NativeTreeOpened
	path := "/api/v1/factory/" + url.PathEscape(factoryUID) + "/invocations/" + url.PathEscape(invocationID) + "/native-tree"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusOK)
	return out, err
}

// DelegateNative posts one child spawn.
func (c *Client) DelegateNative(ctx context.Context, factoryUID, taskID string, req NativeDelegateRequest) (NativeDelegateResult, error) {
	var out NativeDelegateResult
	path := nativeTaskPath(factoryUID, taskID) + "/delegate"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusOK)
	return out, err
}

// ActivateNative claims one fenced activation.
func (c *Client) ActivateNative(ctx context.Context, factoryUID, taskID string, req NativeActivateRequest) (NativeActivation, error) {
	var out NativeActivation
	path := nativeTaskPath(factoryUID, taskID) + "/activate"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusOK)
	return out, err
}

// NativeParkRequest records blockers and releases the worker slot.
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

// NativeCompleteRequest marks the task succeeded or failed.
type NativeCompleteRequest struct {
	Failed bool `json:"failed"`
}

// ParkNative releases the slot and records blockers.
func (c *Client) ParkNative(ctx context.Context, factoryUID, taskID string, req NativeParkRequest) (NativeActivation, error) {
	var out NativeActivation
	path := nativeTaskPath(factoryUID, taskID) + "/park"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusOK)
	return out, err
}

// JoinNative applies the frozen join edges. A parked parent with finished children resumes.
func (c *Client) JoinNative(ctx context.Context, factoryUID, taskID string) (NativeJoinResult, error) {
	var out NativeJoinResult
	path := nativeTaskPath(factoryUID, taskID) + "/join"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &out, http.StatusOK)
	return out, err
}

// WakeNativeRun posts one durable wake. A completed task opens a linked run.
func (c *Client) WakeNativeRun(ctx context.Context, factoryUID, taskID string, req NativeWakeRunRequest) (NativeWakeRunResult, error) {
	var out NativeWakeRunResult
	path := nativeTaskPath(factoryUID, taskID) + "/wake-run"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusOK)
	return out, err
}

// CompleteNative marks the task succeeded or failed.
func (c *Client) CompleteNative(ctx context.Context, factoryUID, taskID string, req NativeCompleteRequest) (NativeActivation, error) {
	var out NativeActivation
	path := nativeTaskPath(factoryUID, taskID) + "/complete"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusOK)
	return out, err
}
