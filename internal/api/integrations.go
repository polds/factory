package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/cron"
	"github.com/polds/factory/internal/ingress"
)

const (
	replaySuffix = ":replay"
	includeBody  = "body"
	maxListLimit = 1000
)

// DeliveryBackend lists and replays persisted deliveries.
// Handlers type-assert Backend to DeliveryBackend. A Backend that does not
// implement it answers 404 on the delivery routes.
//
// ReplayDelivery queues a stored delivery for another dispatcher pass and
// returns it with status received. It does not verify a signature. Signature
// headers are stripped before persistence, so they cannot be checked again,
// and this route has no parameter that skips verification on ingress.
type DeliveryBackend interface {
	ListDeliveries(ctx context.Context, factoryID, status string, limit int) ([]Delivery, error)
	ReplayDelivery(ctx context.Context, id string) (Delivery, error)
}

// ScheduleBackend is the docs/04 schedule CRUD surface plus pause and resume.
// Handlers type-assert Backend to ScheduleBackend.
type ScheduleBackend interface {
	ListSchedules(ctx context.Context, factoryID string) ([]Schedule, error)
	GetSchedule(ctx context.Context, id string) (Schedule, error)
	CreateSchedule(ctx context.Context, req ScheduleRequest) (Schedule, error)
	UpdateSchedule(ctx context.Context, id string, patch SchedulePatch) (Schedule, error)
	DeleteSchedule(ctx context.Context, id string) error
	SetSchedulePaused(ctx context.Context, id string, paused bool) (Schedule, error)
}

// ExternalRefBackend resolves a provider thread to the bound work item.
// Handlers type-assert Backend to ExternalRefBackend.
type ExternalRefBackend interface {
	RunByExternalReference(ctx context.Context, provider, ref string) (ExternalRun, error)
}

// InputBackend records an answer to a work item's pending question.
// The same answer is idempotent. A different answer returns ErrConflict.
// Starting the follow-up run is the server's job; this method only records
// the answer.
type InputBackend interface {
	AnswerInput(ctx context.Context, workItemID, text, answeredBy string) (InputAnswer, error)
}

// IngressMounts supplies the signature-authenticated handlers.
// MountIngress is consulted for github, slack, and linear. MountWebhook
// serves both /api/v1/webhooks/{webhook_uid} and /api/v1/webhooks/u/{url_token}.
// ok is false when that ingress is not configured; the route then answers 404
// and does not require an API key.
type IngressMounts interface {
	MountIngress(provider string) (http.Handler, bool)
	MountWebhook() (http.Handler, bool)
}

// inputBody is the POST /work-items/{id}/input JSON.
// The actor is the API key. A body field cannot choose it.
type inputBody struct {
	Text string `json:"text"`
}

func (h *handler) handleProviderIngress(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.serveMounted(w, r, func(m IngressMounts) (http.Handler, bool) {
			return m.MountIngress(provider)
		})
	}
}

func (h *handler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	h.serveMounted(w, r, func(m IngressMounts) (http.Handler, bool) {
		return m.MountWebhook()
	})
}

func (h *handler) serveMounted(w http.ResponseWriter, r *http.Request, pick func(IngressMounts) (http.Handler, bool)) {
	mounts, ok := h.backend.(IngressMounts)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	next, ok := pick(mounts)
	if !ok || next == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	limitIngressBody(next).ServeHTTP(w, r)
}

// limitIngressBody rejects a body above the ingress cap before the provider
// handler reads it. The handler still verifies the signature over those raw
// bytes; this middleware does not parse the payload.
func limitIngressBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > ingress.MaxBody {
			if r.Body != nil {
				_ = r.Body.Close()
			}
			writeProblem(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, ingress.MaxBody+1))
		closeErr := r.Body.Close()
		if err != nil || closeErr != nil {
			writeProblem(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(data) > ingress.MaxBody {
			writeProblem(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		next.ServeHTTP(w, r)
	})
}

func (h *handler) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	db, ok := h.requireDeliveries(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if !validDeliveryStatus(status) {
		writeProblem(w, http.StatusBadRequest, "status is invalid")
		return
	}
	include, ok := parseIncludeBody(q.Get("include"))
	if !ok {
		writeProblem(w, http.StatusBadRequest, "include is invalid")
		return
	}
	// The route wrapper already requires a key. Checking again keeps the
	// body off the response if that wrapper is ever dropped.
	if include && !h.authorized(r) {
		writeProblem(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, ok := parseListLimit(q.Get("limit"))
	if !ok {
		writeProblem(w, http.StatusBadRequest, "limit is invalid")
		return
	}
	rows, err := db.ListDeliveries(r.Context(), r.PathValue("uid"), status, limit)
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	if rows == nil {
		rows = []Delivery{}
	}
	out := make([]Delivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, presentDelivery(row, include))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	db, ok := h.backend.(WebhookDeliveryBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if !validDeliveryStatus(status) {
		writeProblem(w, http.StatusBadRequest, "status is invalid")
		return
	}
	include, ok := parseIncludeBody(q.Get("include"))
	if !ok {
		writeProblem(w, http.StatusBadRequest, "include is invalid")
		return
	}
	if include && !h.authorized(r) {
		writeProblem(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, ok := parseListLimit(q.Get("limit"))
	if !ok {
		writeProblem(w, http.StatusBadRequest, "limit is invalid")
		return
	}
	rows, err := db.ListWebhookDeliveries(r.Context(), r.PathValue("uid"), r.PathValue("webhookUID"), status, limit)
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	if rows == nil {
		rows = []Delivery{}
	}
	out := make([]Delivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, presentDelivery(row, include))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleReplayDelivery(w http.ResponseWriter, r *http.Request) {
	db, ok := h.requireDeliveries(w)
	if !ok {
		return
	}
	id, ok := replayDeliveryID(r.PathValue("deliveryID"))
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	// Re-dispatch only. The verifier is not called, and query values cannot
	// ask for that: a stored delivery no longer has its signature headers.
	d, err := db.ReplayDelivery(r.Context(), id)
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	writeJSON(w, http.StatusOK, presentDelivery(d, false))
}

func (h *handler) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	sb, ok := h.requireSchedules(w)
	if !ok {
		return
	}
	rows, err := sb.ListSchedules(r.Context(), strings.TrimSpace(r.URL.Query().Get("factory")))
	if err != nil {
		h.writeStatusErr(w, err, "schedule exists")
		return
	}
	if rows == nil {
		rows = []Schedule{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handler) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	sb, ok := h.requireSchedules(w)
	if !ok {
		return
	}
	var req ScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeReadErr(w, err)
		return
	}
	req.FactoryID = strings.TrimSpace(req.FactoryID)
	req.Automation = strings.TrimSpace(req.Automation)
	req.Name = strings.TrimSpace(req.Name)
	req.Cron = strings.TrimSpace(req.Cron)
	if req.FactoryID == "" || req.Automation == "" || req.Cron == "" {
		writeProblem(w, http.StatusBadRequest, "factory, automation, and cron are required")
		return
	}
	if _, err := cron.Parse(req.Cron); err != nil {
		writeProblem(w, http.StatusBadRequest, "cron is invalid")
		return
	}
	row, err := sb.CreateSchedule(r.Context(), req)
	if err != nil {
		h.writeStatusErr(w, err, "schedule exists")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *handler) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	sb, ok := h.requireSchedules(w)
	if !ok {
		return
	}
	row, err := sb.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeStatusErr(w, err, "schedule exists")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *handler) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	sb, ok := h.requireSchedules(w)
	if !ok {
		return
	}
	var patch SchedulePatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeReadErr(w, err)
		return
	}
	if detail := normalizeSchedulePatch(&patch); detail != "" {
		writeProblem(w, http.StatusBadRequest, detail)
		return
	}
	row, err := sb.UpdateSchedule(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		h.writeStatusErr(w, err, "schedule exists")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *handler) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	sb, ok := h.requireSchedules(w)
	if !ok {
		return
	}
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	if err := sb.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		h.writeStatusErr(w, err, "schedule exists")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handlePauseSchedule(w http.ResponseWriter, r *http.Request) {
	h.setSchedulePaused(w, r, true)
}

func (h *handler) handleResumeSchedule(w http.ResponseWriter, r *http.Request) {
	h.setSchedulePaused(w, r, false)
}

func (h *handler) setSchedulePaused(w http.ResponseWriter, r *http.Request, paused bool) {
	sb, ok := h.requireSchedules(w)
	if !ok {
		return
	}
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	row, err := sb.SetSchedulePaused(r.Context(), r.PathValue("id"), paused)
	if err != nil {
		h.writeStatusErr(w, err, "schedule exists")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *handler) handleRunByExternalReference(w http.ResponseWriter, r *http.Request) {
	eb, ok := h.requireExternal(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	provider := strings.TrimSpace(q.Get("provider"))
	ref := strings.TrimSpace(q.Get("ref"))
	if provider == "" || ref == "" {
		writeProblem(w, http.StatusBadRequest, "provider and ref are required")
		return
	}
	row, err := eb.RunByExternalReference(r.Context(), provider, ref)
	if err != nil {
		h.writeStatusErr(w, err, "conflict")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *handler) handleAnswerInput(w http.ResponseWriter, r *http.Request) {
	ib, ok := h.requireInput(w)
	if !ok {
		return
	}
	var body inputBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeReadErr(w, err)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeProblem(w, http.StatusBadRequest, "text is required")
		return
	}
	row, err := ib.AnswerInput(r.Context(), r.PathValue("id"), text, APIKeyPrincipal)
	if err != nil {
		h.writeStatusErr(w, err, "answer conflicts")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *handler) requireDeliveries(w http.ResponseWriter) (DeliveryBackend, bool) {
	db, ok := h.backend.(DeliveryBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return db, true
}

func (h *handler) requireSchedules(w http.ResponseWriter) (ScheduleBackend, bool) {
	sb, ok := h.backend.(ScheduleBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return sb, true
}

func (h *handler) requireExternal(w http.ResponseWriter) (ExternalRefBackend, bool) {
	eb, ok := h.backend.(ExternalRefBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return eb, true
}

func (h *handler) requireInput(w http.ResponseWriter) (InputBackend, bool) {
	ib, ok := h.backend.(InputBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return ib, true
}

func replayDeliveryID(raw string) (string, bool) {
	id, ok := strings.CutSuffix(raw, replaySuffix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

func validDeliveryStatus(status string) bool {
	switch status {
	case "", ingress.StatusReceived, ingress.StatusMatched, ingress.StatusUnmatched, ingress.StatusIgnored, ingress.StatusFailed:
		return true
	default:
		return false
	}
}

func parseIncludeBody(raw string) (include bool, ok bool) {
	switch raw {
	case "":
		return false, true
	case includeBody:
		return true, true
	default:
		return false, false
	}
}

func parseListLimit(raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxListLimit {
		return 0, false
	}
	return n, true
}

func normalizeSchedulePatch(p *SchedulePatch) string {
	if p.Cron != nil {
		cronExpr := strings.TrimSpace(*p.Cron)
		p.Cron = &cronExpr
		if cronExpr == "" {
			return "cron is invalid"
		}
		if _, err := cron.Parse(cronExpr); err != nil {
			return "cron is invalid"
		}
	}
	if p.Automation != nil {
		automation := strings.TrimSpace(*p.Automation)
		p.Automation = &automation
		if automation == "" {
			return "automation is required"
		}
	}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		p.Name = &name
	}
	return ""
}

func presentDelivery(d Delivery, include bool) Delivery {
	out := d
	out.Headers = redactDeliveryHeaders(d.Headers)
	if out.MatchedAutomations == nil {
		out.MatchedAutomations = []string{}
	}
	if !include {
		out.Body = nil
		return out
	}
	out.Body = normalizeBody(d.Body)
	return out
}

func normalizeBody(b json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if json.Valid(b) {
		return append(json.RawMessage(nil), b...)
	}
	encoded, err := json.Marshal(string(b))
	if err != nil {
		return nil
	}
	return encoded
}

func redactDeliveryHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		if sensitiveDeliveryHeader(name) {
			continue
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sensitiveDeliveryHeader(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == "authorization", n == "cookie", n == "proxy-authorization":
		return true
	case strings.Contains(n, "signature"), strings.Contains(n, "secret"), strings.Contains(n, "token"):
		return true
	default:
		return false
	}
}
