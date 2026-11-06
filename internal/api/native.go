package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const maxNativeWait = 30 * time.Second

// NativeBackend is the native task, inbox, and wake surface.
// A Backend that does not implement it answers 404. These routes do not
// start executions or advertise native dispatch.
type NativeBackend interface {
	CreateNativeTask(ctx context.Context, factoryUID string, req NativeTaskRequest) (NativeTask, error)
	GetNativeTask(ctx context.Context, factoryUID, taskID string) (NativeTask, error)
	CommentNativeTask(ctx context.Context, factoryUID, taskID string, req NativeCommentRequest) (NativeEventResult, error)
	AssignNativeTask(ctx context.Context, factoryUID, taskID string, req NativeAssignRequest) (NativeEventResult, error)
	ReadNativeInbox(ctx context.Context, factoryUID string, q NativeInboxQuery) (NativeInboxPage, error)
	ApplyNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (NativeApplyResult, error)
	AckNativeDelivery(ctx context.Context, factoryUID, taskID, deliveryID string) (NativeAckResult, error)
	RedeliverNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (NativeRedelivery, error)
	ClaimNativeInbox(ctx context.Context, factoryUID, taskID, holder string) (NativeClaim, error)
	GetNativeClaim(ctx context.Context, factoryUID, taskID string) (NativeClaim, error)
	GetNativeWake(ctx context.Context, factoryUID, taskID string) (NativeWake, error)
	ReadNativeOutbox(ctx context.Context, factoryUID, invocationID string, after int64, limit int) (NativeOutboxPage, error)
}

// NativeTaskRequest creates one task. Principal is not a body field.
type NativeTaskRequest struct {
	InvocationID string   `json:"invocationId"`
	ParentTaskID string   `json:"parentTaskId,omitempty"`
	Assignee     string   `json:"assignee"`
	BlockerIDs   []string `json:"blockerIds,omitempty"`
	ActivationID string   `json:"activationId,omitempty"`
	ExecutionID  string   `json:"executionId,omitempty"`
	BudgetScope  string   `json:"budgetScope,omitempty"`
}

// NativeTask is the wire task. LinearIssue is always null.
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

// NativeCommentRequest is a comment or a mention. Mention does not wake.
type NativeCommentRequest struct {
	Body string `json:"body"`
	Kind string `json:"kind,omitempty"`
}

// NativeAssignRequest routes the task to a new assignee and records a wake.
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

// NativeInboxQuery is the inbox read. Wait long-polls and does not ack.
type NativeInboxQuery struct {
	Assignee string
	Cursor   string
	Limit    int
	Wait     time.Duration
}

// NativeInboxPage is one opaque cursor page.
type NativeInboxPage struct {
	Deliveries []NativeDelivery `json:"deliveries"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// NativeDelivery is one inbox projection row.
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

// NativeApplyResult reports whether this call inserted the durable apply.
type NativeApplyResult struct {
	MessageID string `json:"messageId"`
	TaskID    string `json:"taskId"`
	Applied   bool   `json:"applied"`
}

// NativeAckResult is an explicit ack. Duplicate means the row already existed.
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

// NativeWake is the pending coalesced wake. It does not start an execution.
type NativeWake struct {
	TaskID        string `json:"taskId"`
	Assignee      string `json:"assignee"`
	Reason        string `json:"reason"`
	Pending       bool   `json:"pending"`
	CoalesceCount int64  `json:"coalesceCount"`
}

// NativeOutboxPage is a ledger-ordered outbox replay page.
type NativeOutboxPage struct {
	Records []NativeOutboxRecord `json:"records"`
}

// NativeOutboxRecord is one committed outbox row.
type NativeOutboxRecord struct {
	LedgerSeq int64           `json:"ledgerSeq"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

func (h *handler) registerNative(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/factory/{uid}/native-tasks", h.withAPIKey(h.handleCreateNativeTask))
	m.HandleFunc("GET /api/v1/factory/{uid}/native-tasks/{taskID}", h.withAPIKey(h.handleGetNativeTask))
	m.HandleFunc("POST /api/v1/factory/{uid}/native-tasks/{taskID}/comments", h.withAPIKey(h.handleCommentNativeTask))
	m.HandleFunc("POST /api/v1/factory/{uid}/native-tasks/{taskID}/assign", h.withAPIKey(h.handleAssignNativeTask))
	m.HandleFunc("GET /api/v1/factory/{uid}/native-inbox", h.withAPIKey(h.handleReadNativeInbox))
	m.HandleFunc("POST /api/v1/factory/{uid}/native-tasks/{taskID}/messages/{messageID}/apply", h.withAPIKey(h.handleApplyNativeMessage))
	m.HandleFunc("POST /api/v1/factory/{uid}/native-tasks/{taskID}/deliveries/{deliveryID}/ack", h.withAPIKey(h.handleAckNativeDelivery))
	m.HandleFunc("POST /api/v1/factory/{uid}/native-tasks/{taskID}/messages/{messageID}/redeliver", h.withAPIKey(h.handleRedeliverNativeMessage))
	m.HandleFunc("POST /api/v1/factory/{uid}/native-tasks/{taskID}/claim", h.withAPIKey(h.handleClaimNativeInbox))
	m.HandleFunc("GET /api/v1/factory/{uid}/native-tasks/{taskID}/claim", h.withAPIKey(h.handleGetNativeClaim))
	m.HandleFunc("GET /api/v1/factory/{uid}/native-tasks/{taskID}/wake", h.withAPIKey(h.handleGetNativeWake))
	m.HandleFunc("GET /api/v1/factory/{uid}/invocations/{invocationID}/outbox", h.withAPIKey(h.handleReadNativeOutbox))
}

func (h *handler) nativeBackend(w http.ResponseWriter) (NativeBackend, bool) {
	nb, ok := h.backend.(NativeBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return nb, true
}

func (h *handler) handleCreateNativeTask(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	var req NativeTaskRequest
	if !decodeNative(w, r, &req) {
		return
	}
	task, err := nb.CreateNativeTask(r.Context(), r.PathValue("uid"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (h *handler) handleGetNativeTask(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	task, err := nb.GetNativeTask(r.Context(), r.PathValue("uid"), r.PathValue("taskID"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *handler) handleCommentNativeTask(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	var req NativeCommentRequest
	if !decodeNative(w, r, &req) {
		return
	}
	result, err := nb.CommentNativeTask(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *handler) handleAssignNativeTask(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	var req NativeAssignRequest
	if !decodeNative(w, r, &req) {
		return
	}
	result, err := nb.AssignNativeTask(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) handleReadNativeInbox(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	q, err := nativeInboxQuery(r)
	if err != nil {
		writeProblemReason(w, http.StatusBadRequest, "input_invalid")
		return
	}
	page, err := nb.ReadNativeInbox(r.Context(), r.PathValue("uid"), q)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	if page.Deliveries == nil {
		page.Deliveries = []NativeDelivery{}
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *handler) handleApplyNativeMessage(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	if !rejectNativeBody(w, r) {
		return
	}
	result, err := nb.ApplyNativeMessage(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), r.PathValue("messageID"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) handleAckNativeDelivery(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	if !rejectNativeBody(w, r) {
		return
	}
	result, err := nb.AckNativeDelivery(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), r.PathValue("deliveryID"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) handleRedeliverNativeMessage(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	if !rejectNativeBody(w, r) {
		return
	}
	result, err := nb.RedeliverNativeMessage(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), r.PathValue("messageID"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *handler) handleClaimNativeInbox(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	var req struct {
		Holder string `json:"holder"`
	}
	if !decodeNative(w, r, &req) {
		return
	}
	claim, err := nb.ClaimNativeInbox(r.Context(), r.PathValue("uid"), r.PathValue("taskID"), req.Holder)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (h *handler) handleGetNativeClaim(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	claim, err := nb.GetNativeClaim(r.Context(), r.PathValue("uid"), r.PathValue("taskID"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (h *handler) handleGetNativeWake(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	wake, err := nb.GetNativeWake(r.Context(), r.PathValue("uid"), r.PathValue("taskID"))
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wake)
}

func (h *handler) handleReadNativeOutbox(w http.ResponseWriter, r *http.Request) {
	nb, ok := h.nativeBackend(w)
	if !ok {
		return
	}
	after, limit, err := nativeOutboxQuery(r)
	if err != nil {
		writeProblemReason(w, http.StatusBadRequest, "input_invalid")
		return
	}
	page, err := nb.ReadNativeOutbox(r.Context(), r.PathValue("uid"), r.PathValue("invocationID"), after, limit)
	if err != nil {
		h.writeNativeErr(w, err)
		return
	}
	if page.Records == nil {
		page.Records = []NativeOutboxRecord{}
	}
	writeJSON(w, http.StatusOK, page)
}

func nativeInboxQuery(r *http.Request) (NativeInboxQuery, error) {
	q := r.URL.Query()
	var out NativeInboxQuery
	out.Assignee = q.Get("assignee")
	out.Cursor = q.Get("cursor")
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return NativeInboxQuery{}, err
		}
		out.Limit = n
	}
	if raw := q.Get("waitMs"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return NativeInboxQuery{}, errors.New("wait")
		}
		out.Wait = time.Duration(n) * time.Millisecond
		if out.Wait > maxNativeWait {
			return NativeInboxQuery{}, errors.New("wait")
		}
	}
	return out, nil
}

func nativeOutboxQuery(r *http.Request) (int64, int, error) {
	q := r.URL.Query()
	var after int64
	if raw := q.Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, err
		}
		after = n
	}
	var limit int
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, 0, err
		}
		limit = n
	}
	return after, limit, nil
}

func decodeNative(w http.ResponseWriter, r *http.Request, dest any) bool {
	body, err := readLimited(w, r.Body, maxBodyBytes)
	if err != nil {
		writeReadErr(w, err)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		writeProblemReason(w, http.StatusBadRequest, "input_invalid")
		return false
	}
	return true
}

func rejectNativeBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := readLimited(w, r.Body, maxBodyBytes)
	if err != nil {
		writeReadErr(w, err)
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(body, &extra); err != nil || len(extra) > 0 {
		writeProblemReason(w, http.StatusBadRequest, "input_invalid")
		return false
	}
	return true
}

func (h *handler) writeNativeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrNativeClaimFenced):
		writeProblemReason(w, http.StatusConflict, "claim_fenced")
	case errors.Is(err, ErrNativeInput):
		writeProblemReason(w, http.StatusBadRequest, "input_invalid")
	case errors.Is(err, ErrNativePrincipal):
		writeProblemReason(w, http.StatusForbidden, "principal_rejected")
	case errors.Is(err, ErrNativeNotApplied):
		writeProblemReason(w, http.StatusConflict, "not_applied")
	default:
		h.writeBackendErr(w, err)
	}
}

var (
	// ErrNativeClaimFenced is a lost inbox claim.
	ErrNativeClaimFenced = errors.New("claim_fenced")
	// ErrNativeInput is a closed-schema or contract rejection.
	ErrNativeInput = errors.New("input_invalid")
	// ErrNativePrincipal is an ack principal that does not match the apply.
	ErrNativePrincipal = errors.New("principal_rejected")
	// ErrNativeNotApplied means ack ran before a durable apply.
	ErrNativeNotApplied = errors.New("not_applied")
)
