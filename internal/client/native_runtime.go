package client

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// NativePersona is one frozen roster entry.
type NativePersona struct {
	Name        string   `json:"name"`
	ReportsTo   string   `json:"reportsTo"`
	SpawnableBy []string `json:"spawnableBy"`
	Role        string   `json:"role"`
}

// NativeTreeRequest freezes a roster. Principal is not a field.
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
