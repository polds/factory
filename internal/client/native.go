package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// NativeTaskRequest is the create body. The principal is the API key, not a field.
type NativeTaskRequest struct {
	InvocationID string   `json:"invocationId"`
	ParentTaskID string   `json:"parentTaskId,omitempty"`
	Assignee     string   `json:"assignee"`
	BlockerIDs   []string `json:"blockerIds,omitempty"`
	ActivationID string   `json:"activationId,omitempty"`
	ExecutionID  string   `json:"executionId,omitempty"`
	BudgetScope  string   `json:"budgetScope,omitempty"`
}

// NativeTask is a native task. LinearIssue is null.
type NativeTask struct {
	ID           string   `json:"id"`
	TaskRef      string   `json:"taskRef"`
	FactoryID    string   `json:"factoryId"`
	InvocationID string   `json:"invocationId"`
	ParentTaskID string   `json:"parentTaskId,omitempty"`
	Assignee     string   `json:"assignee"`
	BlockerIDs   []string `json:"blockerIds"`
	ActivationID string   `json:"activationId,omitempty"`
	ExecutionID  string   `json:"executionId,omitempty"`
	Status       string   `json:"status"`
	BudgetScope  string   `json:"budgetScope,omitempty"`
	LinearIssue  *string  `json:"linearIssue"`
}

// NativeCommentRequest is a comment or mention.
type NativeCommentRequest struct {
	Body string `json:"body"`
	Kind string `json:"kind,omitempty"`
}

// NativeAssignRequest routes the task to assignee.
type NativeAssignRequest struct {
	Assignee string `json:"assignee"`
}

// NativeEventResult is the committed ledger position.
type NativeEventResult struct {
	LedgerSeq   int64  `json:"ledgerSeq"`
	MessageID   string `json:"messageId"`
	DeliveryID  string `json:"deliveryId,omitempty"`
	WakePending bool   `json:"wakePending"`
}

// NativeInboxPage is one inbox page. NextCursor is opaque.
type NativeInboxPage struct {
	Deliveries []NativeDelivery `json:"deliveries"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// NativeDelivery is one projected delivery.
type NativeDelivery struct {
	DeliveryID   string `json:"deliveryId"`
	MessageID    string `json:"messageId"`
	InvocationID string `json:"invocationId"`
	TaskID       string `json:"taskId"`
	Assignee     string `json:"assignee"`
	Body         string `json:"body"`
	Kind         string `json:"kind"`
	LedgerSeq    int64  `json:"ledgerSeq"`
}

// NativeApplyResult reports the durable apply.
type NativeApplyResult struct {
	MessageID string `json:"messageId"`
	TaskID    string `json:"taskId"`
	Applied   bool   `json:"applied"`
}

// NativeAckResult is an explicit ack.
type NativeAckResult struct {
	DeliveryID string `json:"deliveryId"`
	MessageID  string `json:"messageId"`
	TaskID     string `json:"taskId"`
	Duplicate  bool   `json:"duplicate"`
}

// NativeRedelivery is a new delivery of an existing message.
type NativeRedelivery struct {
	DeliveryID string `json:"deliveryId"`
	MessageID  string `json:"messageId"`
	TaskID     string `json:"taskId"`
}

// NativeClaim is the inbox fence.
type NativeClaim struct {
	TaskID   string `json:"taskId"`
	Holder   string `json:"holder"`
	Fence    int64  `json:"fence"`
	Accepted bool   `json:"accepted"`
}

// NativeWake is a pending coalesced wake intent.
type NativeWake struct {
	TaskID        string `json:"taskId"`
	Assignee      string `json:"assignee"`
	Reason        string `json:"reason"`
	Pending       bool   `json:"pending"`
	CoalesceCount int64  `json:"coalesceCount"`
}

// NativeOutboxPage replays committed outbox rows.
type NativeOutboxPage struct {
	Records []NativeOutboxRecord `json:"records"`
}

// NativeOutboxRecord is one outbox row.
type NativeOutboxRecord struct {
	LedgerSeq int64  `json:"ledgerSeq"`
	Kind      string `json:"kind"`
}

// CreateNativeTask posts a native task.
func (c *Client) CreateNativeTask(ctx context.Context, factoryUID string, req NativeTaskRequest) (NativeTask, error) {
	var out NativeTask
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/factory/"+url.PathEscape(factoryUID)+"/native-tasks", req, &out, http.StatusCreated)
	return out, err
}

// GetNativeTask reads one native task.
func (c *Client) GetNativeTask(ctx context.Context, factoryUID, taskID string) (NativeTask, error) {
	var out NativeTask
	path := "/api/v1/factory/" + url.PathEscape(factoryUID) + "/native-tasks/" + url.PathEscape(taskID)
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK)
	return out, err
}

// CommentNativeTask appends a comment or mention.
func (c *Client) CommentNativeTask(ctx context.Context, factoryUID, taskID string, req NativeCommentRequest) (NativeEventResult, error) {
	var out NativeEventResult
	path := "/api/v1/factory/" + url.PathEscape(factoryUID) + "/native-tasks/" + url.PathEscape(taskID) + "/comments"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusCreated)
	return out, err
}

// AssignNativeTask routes the task to the assignee.
func (c *Client) AssignNativeTask(ctx context.Context, factoryUID, taskID, assignee string) (NativeEventResult, error) {
	var out NativeEventResult
	path := "/api/v1/factory/" + url.PathEscape(factoryUID) + "/native-tasks/" + url.PathEscape(taskID) + "/assign"
	err := c.doJSON(ctx, http.MethodPost, path, NativeAssignRequest{Assignee: assignee}, &out, http.StatusOK)
	return out, err
}

// ReadNativeInbox reads one assignee inbox. wait long-polls and does not ack.
func (c *Client) ReadNativeInbox(ctx context.Context, factoryUID, assignee, cursor string, limit int, wait time.Duration) (NativeInboxPage, error) {
	q := url.Values{}
	q.Set("assignee", assignee)
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if wait > 0 {
		q.Set("waitMs", strconv.FormatInt(wait.Milliseconds(), 10))
	}
	var out NativeInboxPage
	err := c.doJSONQuery(ctx, http.MethodGet, "/api/v1/factory/"+url.PathEscape(factoryUID)+"/native-inbox", q, nil, &out, http.StatusOK)
	return out, err
}

// ApplyNativeMessage records a durable apply.
func (c *Client) ApplyNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (NativeApplyResult, error) {
	var out NativeApplyResult
	path := nativeTaskPath(factoryUID, taskID) + "/messages/" + url.PathEscape(messageID) + "/apply"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &out, http.StatusOK)
	return out, err
}

// AckNativeDelivery acknowledges one delivery.
func (c *Client) AckNativeDelivery(ctx context.Context, factoryUID, taskID, deliveryID string) (NativeAckResult, error) {
	var out NativeAckResult
	path := nativeTaskPath(factoryUID, taskID) + "/deliveries/" + url.PathEscape(deliveryID) + "/ack"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &out, http.StatusOK)
	return out, err
}

// RedeliverNativeMessage creates a new delivery id for the message.
func (c *Client) RedeliverNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (NativeRedelivery, error) {
	var out NativeRedelivery
	path := nativeTaskPath(factoryUID, taskID) + "/messages/" + url.PathEscape(messageID) + "/redeliver"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &out, http.StatusCreated)
	return out, err
}

// ClaimNativeInbox claims the inbox fence.
func (c *Client) ClaimNativeInbox(ctx context.Context, factoryUID, taskID, holder string) (NativeClaim, error) {
	var out NativeClaim
	path := nativeTaskPath(factoryUID, taskID) + "/claim"
	err := c.doJSON(ctx, http.MethodPost, path, map[string]string{"holder": holder}, &out, http.StatusOK)
	return out, err
}

// GetNativeWake reads the pending wake intent.
func (c *Client) GetNativeWake(ctx context.Context, factoryUID, taskID string) (NativeWake, error) {
	var out NativeWake
	path := nativeTaskPath(factoryUID, taskID) + "/wake"
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK)
	return out, err
}

func nativeTaskPath(factoryUID, taskID string) string {
	return "/api/v1/factory/" + url.PathEscape(factoryUID) + "/native-tasks/" + url.PathEscape(taskID)
}
