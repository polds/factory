package api

import (
	"errors"
	"net/http"
	"strings"
)

const decideSuffix = ":decide"

func (h *handler) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	wb, ok := h.requireWork(w)
	if !ok {
		return
	}
	var req TaskRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeReadErr(w, err)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeProblem(w, http.StatusBadRequest, "title is required")
		return
	}
	source, ok := normalizeSource(req.Source)
	if !ok {
		writeProblem(w, http.StatusBadRequest, "source is invalid")
		return
	}
	req.Title = title
	req.Source = source
	req.SourceRef = strings.TrimSpace(req.SourceRef)
	// The caller is the API key. Ignore any actor field in the JSON.
	req.CreatedBy = APIKeyPrincipal
	task, err := wb.CreateTask(r.Context(), r.PathValue("uid"), req)
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (h *handler) handleGetTask(w http.ResponseWriter, r *http.Request) {
	wb, ok := h.requireWork(w)
	if !ok {
		return
	}
	task, err := wb.GetTask(r.Context(), r.PathValue("uid"), r.PathValue("taskID"))
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *handler) handleListTasks(w http.ResponseWriter, r *http.Request) {
	wb, ok := h.requireWork(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	tasks, err := wb.ListTasks(r.Context(), r.PathValue("uid"), TaskFilter{
		Stage:  q.Get("stage"),
		Status: q.Get("status"),
		Source: q.Get("source"),
	})
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	if tasks == nil {
		tasks = []Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *handler) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	wb, ok := h.requireWork(w)
	if !ok {
		return
	}
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	task, err := wb.CancelTask(r.Context(), r.PathValue("uid"), r.PathValue("taskID"))
	if err != nil {
		h.writeStatusErr(w, err, "work item is terminal")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *handler) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	wb, ok := h.requireWork(w)
	if !ok {
		return
	}
	onlyPending, ok := parsePending(r.URL.Query().Get("pending"))
	if !ok {
		writeProblem(w, http.StatusBadRequest, "pending is invalid")
		return
	}
	approvals, err := wb.ListApprovals(r.Context(), r.PathValue("uid"), onlyPending)
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	if approvals == nil {
		approvals = []Approval{}
	}
	writeJSON(w, http.StatusOK, approvals)
}

func (h *handler) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	wb, ok := h.requireWork(w)
	if !ok {
		return
	}
	id, ok := decideApprovalID(r.PathValue("approvalID"))
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	var body decideBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeReadErr(w, err)
		return
	}
	decision, ok := normalizeDecision(body.Decision)
	if !ok {
		writeProblem(w, http.StatusBadRequest, "decision is invalid")
		return
	}
	// decidedBy is the API-key principal. A body field cannot choose it.
	approval, err := wb.DecideApproval(r.Context(), id, decision, APIKeyPrincipal, strings.TrimSpace(body.Note))
	if err != nil {
		h.writeStatusErr(w, err, "approval is decided")
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

// decideBody is the POST /approvals/{id}:decide JSON.
type decideBody struct {
	Decision string `json:"decision"`
	Note     string `json:"note,omitempty"`
}

func (h *handler) requireWork(w http.ResponseWriter) (WorkBackend, bool) {
	wb, ok := h.backend.(WorkBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return wb, true
}

func (h *handler) writeStatusErr(w http.ResponseWriter, err error, conflictDetail string) {
	if errors.Is(err, ErrConflict) {
		writeProblem(w, http.StatusConflict, conflictDetail)
		return
	}
	h.writeBackendErr(w, err)
}

func normalizeSource(source string) (string, bool) {
	switch strings.TrimSpace(source) {
	case "":
		return TaskSourceAPI, true
	case TaskSourceIssue, TaskSourceManual, TaskSourceAPI:
		return strings.TrimSpace(source), true
	default:
		return "", false
	}
}

func normalizeDecision(decision string) (string, bool) {
	switch strings.TrimSpace(decision) {
	case DecisionApprove, DecisionReject:
		return strings.TrimSpace(decision), true
	default:
		return "", false
	}
}

// parsePending reports whether the inbox query is pending-only.
// An empty value is the docs/04 inbox: pending gates only.
func parsePending(raw string) (onlyPending bool, ok bool) {
	switch raw {
	case "", "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}

func decideApprovalID(raw string) (string, bool) {
	id, ok := strings.CutSuffix(raw, decideSuffix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}
