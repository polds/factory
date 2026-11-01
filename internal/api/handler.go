// Package api is the factoryd REST surface for factories, agents, runs,
// work items, approvals, deliveries, and schedules.
package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/ingress"
)

const (
	maxBodyBytes   = 1 << 20
	maxIngestBytes = 4 << 20
)

type handler struct {
	backend Backend
	apiKey  string
	events  http.Handler
	mux     *http.ServeMux
}

// NewHandler serves the factoryd REST API.
//
// Every route requires Authorization: Bearer apiKey except GET /healthz,
// POST /api/v1/agent/runs/{runId}/events:ingest, GET
// /api/v1/factory-files/schemas/…, POST /api/v1/ingress/{github,slack,linear},
// and POST /api/v1/webhooks/…. Ingest forwards its bearer token to
// Backend.IngestEvents and does not compare it to apiKey. Ingress and
// webhook routes are signature-authenticated by the mounted handler. They
// have no flag that skips verification, and a body over 1 MiB is rejected
// before that handler runs.
//
// events serves GET /api/v1/agent/runs/{runId}/events after the API key is
// accepted. A nil events handler responds 404 on that route.
//
// Work-item and approval routes call WorkBackend when b implements it, and
// respond 404 otherwise. Delivery, schedule, external-reference, and input
// routes do the same with DeliveryBackend, ScheduleBackend,
// ExternalRefBackend, and InputBackend. Ingress routes call IngressMounts.
// Schema routes are mounted by RegisterSchemaRoutes and delegate to
// SchemaCatalog when b implements it.
//
// Console routes call ConsoleBackend when b implements it, and respond
// 501 problem+json when it does not. GET …/steer is excluded from the
// API-key check and forwards its bearer token to NextSteers, like ingest.
func NewHandler(b Backend, apiKey string, events http.Handler) http.Handler {
	h := &handler{
		backend: b,
		apiKey:  apiKey,
		events:  events,
		mux:     http.NewServeMux(),
	}
	h.route()
	return h
}

func (h *handler) route() {
	m := h.mux
	m.HandleFunc("GET /healthz", h.handleHealth)
	m.HandleFunc("GET /api/v1/factory", h.withAPIKey(h.handleListFactories))
	m.HandleFunc("GET /api/v1/factory/{uid}", h.withAPIKey(h.handleGetFactory))
	m.HandleFunc("POST /api/v1/factory/{uid}/definition:validate", h.withAPIKey(h.handleValidate))
	m.HandleFunc("POST /api/v1/factory/{uid}/definition:apply", h.withAPIKey(h.handleApply))
	// D1. T89 implements publish. Until then the handler returns 501.
	// A git-backed factory is 409 once that implementation lands.
	m.HandleFunc("PUT /api/v1/factory/{name}/definition", h.withAPIKey(h.handlePutDefinition))
	m.HandleFunc("GET /api/v1/factory/{uid}/agents", h.withAPIKey(h.handleListAgents))
	m.HandleFunc("GET /api/v1/factory/{uid}/runs", h.withAPIKey(h.handleListFactoryRuns))
	m.HandleFunc("POST /api/v1/agent/run", h.withAPIKey(h.handleStartRun))
	m.HandleFunc("GET /api/v1/agent/runs", h.withAPIKey(h.handleListRuns))
	m.HandleFunc("GET /api/v1/agent/runs/{runId}", h.withAPIKey(h.handleGetRun))
	m.HandleFunc("POST /api/v1/agent/runs/{runId}/cancel", h.withAPIKey(h.handleCancel))
	m.HandleFunc("GET /api/v1/agent/runs/{runId}/events", h.withAPIKey(h.handleEvents))
	m.HandleFunc("POST /api/v1/agent/runs/{runId}/events:ingest", h.handleIngest)

	// docs/04 work items and approvals. POST /approvals/{id}:decide is one
	// path segment; ServeMux wildcards occupy the whole segment, so the
	// handler requires the :decide suffix on {approvalID}.
	m.HandleFunc("GET /api/v1/factory/{uid}/tasks", h.withAPIKey(h.handleListTasks))
	m.HandleFunc("POST /api/v1/factory/{uid}/tasks", h.withAPIKey(h.handleCreateTask))
	m.HandleFunc("GET /api/v1/factory/{uid}/tasks/{taskID}", h.withAPIKey(h.handleGetTask))
	m.HandleFunc("POST /api/v1/factory/{uid}/tasks/{taskID}/cancel", h.withAPIKey(h.handleCancelTask))
	m.HandleFunc("GET /api/v1/factory/{uid}/approvals", h.withAPIKey(h.handleListApprovals))
	m.HandleFunc("POST /api/v1/approvals/{approvalID}", h.withAPIKey(h.handleDecideApproval))

	// docs/04 schedules and docs/07 ingress. Replay is one path segment;
	// the :replay suffix is parsed like :decide.
	m.HandleFunc("GET /api/v1/factory/{uid}/deliveries", h.withAPIKey(h.handleListDeliveries))
	m.HandleFunc("POST /api/v1/deliveries/{deliveryID}", h.withAPIKey(h.handleReplayDelivery))
	m.HandleFunc("GET /api/v1/agent/schedules", h.withAPIKey(h.handleListSchedules))
	m.HandleFunc("POST /api/v1/agent/schedules", h.withAPIKey(h.handleCreateSchedule))
	m.HandleFunc("GET /api/v1/agent/schedules/{id}", h.withAPIKey(h.handleGetSchedule))
	m.HandleFunc("PATCH /api/v1/agent/schedules/{id}", h.withAPIKey(h.handleUpdateSchedule))
	m.HandleFunc("DELETE /api/v1/agent/schedules/{id}", h.withAPIKey(h.handleDeleteSchedule))
	m.HandleFunc("POST /api/v1/agent/schedules/{id}/pause", h.withAPIKey(h.handlePauseSchedule))
	m.HandleFunc("POST /api/v1/agent/schedules/{id}/resume", h.withAPIKey(h.handleResumeSchedule))
	m.HandleFunc("GET /api/v1/agent/run-by-external-reference", h.withAPIKey(h.handleRunByExternalReference))
	m.HandleFunc("POST /api/v1/work-items/{id}/input", h.withAPIKey(h.handleAnswerInput))

	// No API key. The provider verifies the signature over the raw body.
	m.HandleFunc("POST /api/v1/ingress/github", h.handleProviderIngress(ingress.ProviderGitHub))
	m.HandleFunc("POST /api/v1/ingress/slack", h.handleProviderIngress(ingress.ProviderSlack))
	m.HandleFunc("POST /api/v1/ingress/linear", h.handleProviderIngress(ingress.ProviderLinear))
	m.HandleFunc("POST /api/v1/webhooks/u/{urlToken}", h.handleWebhook)
	m.HandleFunc("POST /api/v1/webhooks/{webhookUID}", h.handleWebhook)

	h.registerConsole(m)
	h.registerMeasure(m)

	var schemas http.Handler
	if cat, ok := h.backend.(SchemaCatalog); ok {
		schemas = cat.SchemaHandler()
	}
	RegisterSchemaRoutes(m, schemas)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ServeMux writes plain-text 404 and 405. Answer those ourselves so every
	// error body is problem+json. Path values are filled only by mux.ServeHTTP.
	if _, pattern := h.mux.Handler(r); pattern == "" {
		if methods := allowedMethods(h.mux, r); len(methods) > 0 {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeProblem(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (h *handler) authorized(r *http.Request) bool {
	tok, ok := bearerToken(r)
	if !ok || h.apiKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(h.apiKey)) == 1
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) < len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(v[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

func (h *handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{Status: "ok"})
}

func (h *handler) handleListFactories(w http.ResponseWriter, r *http.Request) {
	factories, err := h.backend.ListFactories(r.Context())
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	if factories == nil {
		factories = []Factory{}
	}
	writeJSON(w, http.StatusOK, factories)
}

func (h *handler) handleGetFactory(w http.ResponseWriter, r *http.Request) {
	factory, err := h.backend.GetFactory(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, factory)
}

func (h *handler) handleValidate(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(w, r.Body, maxBodyBytes)
	if err != nil {
		writeReadErr(w, err)
		return
	}
	diags, err := h.backend.ValidateDefinition(r.Context(), r.PathValue("uid"), bytes.NewReader(body))
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ValidateResponse{Diagnostics: diagnosticsOrEmpty(diags)})
}

func (h *handler) handleApply(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(w, r.Body, maxBodyBytes)
	if err != nil {
		writeReadErr(w, err)
		return
	}
	var ref DefinitionRef
	var diags []Diagnostic
	if len(bytes.TrimSpace(body)) == 0 {
		ref, diags, err = h.backend.ApplyDefinition(r.Context(), r.PathValue("uid"))
	} else {
		applier, ok := h.backend.(DefinitionTreeApplier)
		if !ok {
			writeProblem(w, http.StatusNotImplemented, "definition tree apply is not supported")
			return
		}
		ref, diags, err = applier.ApplyDefinitionTree(r.Context(), r.PathValue("uid"), bytes.NewReader(body))
	}
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ApplyResponse{ID: ref.ID, Diagnostics: diagnosticsOrEmpty(diags)})
}

func (h *handler) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := h.backend.ListAgents(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	if agents == nil {
		agents = []Agent{}
	}
	writeJSON(w, http.StatusOK, agents)
}

func (h *handler) handleListFactoryRuns(w http.ResponseWriter, r *http.Request) {
	h.listRuns(w, r, r.PathValue("uid"))
}

func (h *handler) handleListRuns(w http.ResponseWriter, r *http.Request) {
	h.listRuns(w, r, "")
}

func (h *handler) listRuns(w http.ResponseWriter, r *http.Request, factoryID string) {
	q := r.URL.Query()
	queryFactory := strings.TrimSpace(q.Get("factory_id"))
	if factoryID != "" && queryFactory != "" && queryFactory != factoryID {
		writeProblem(w, http.StatusBadRequest, "factory_id does not match")
		return
	}
	if factoryID == "" {
		factoryID = queryFactory
	}
	filter := RunFilter{
		FactoryID:    factoryID,
		Agent:        q.Get("agent"),
		Status:       q.Get("status"),
		Worker:       q.Get("worker"),
		Model:        q.Get("model"),
		FailureClass: q.Get("failure_class"),
		Source:       q.Get("source"),
		ParentRunID:  q.Get("parent_run_id"),
		WorkItemID:   q.Get("work_item_id"),
	}
	if cb, ok := h.backend.(ConsoleBackend); ok {
		rows, err := cb.ListRunRows(r.Context(), filter)
		if err != nil {
			h.writeConsoleErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, orEmpty(rows))
		return
	}
	runs, err := h.backend.ListRuns(r.Context(), filter)
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	if runs == nil {
		runs = []Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (h *handler) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeReadErr(w, err)
		return
	}
	if strings.TrimSpace(req.Factory) == "" {
		writeProblem(w, http.StatusBadRequest, "factory is required")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeProblem(w, http.StatusBadRequest, "prompt is required")
		return
	}
	run, err := h.backend.StartRun(r.Context(), req)
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (h *handler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	if cb, ok := h.backend.(ConsoleBackend); ok {
		detail, err := cb.RunDetail(r.Context(), r.PathValue("runId"))
		if err != nil {
			h.writeConsoleErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, normalizeRunDetail(detail))
		return
	}
	run, err := h.backend.GetRun(r.Context(), r.PathValue("runId"))
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *handler) handleCancel(w http.ResponseWriter, r *http.Request) {
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	run, err := h.backend.CancelRun(r.Context(), r.PathValue("runId"))
	if err != nil {
		h.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	h.events.ServeHTTP(w, r)
}

func (h *handler) handleIngest(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	idxRaw := strings.TrimSpace(r.Header.Get(EventIndexHeader))
	if idxRaw == "" {
		writeProblem(w, http.StatusBadRequest, "event index is required")
		return
	}
	idx, err := strconv.Atoi(idxRaw)
	if err != nil || idx < 0 {
		writeProblem(w, http.StatusBadRequest, "event index is invalid")
		return
	}
	body, err := readLimited(w, r.Body, maxIngestBytes)
	if err != nil {
		writeReadErr(w, err)
		return
	}
	if err := h.backend.IngestEvents(r.Context(), r.PathValue("runId"), tok, idx, splitNDJSON(body)); err != nil {
		h.writeBackendErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var (
	errBodyTooLarge = errors.New("body too large")
	errEmptyBody    = errors.New("empty body")
	errBadJSON      = errors.New("bad json")
)

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	body, err := readLimited(w, r.Body, maxBodyBytes)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errEmptyBody
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(dst); err != nil {
		return errBadJSON
	}
	return nil
}

func readLimited(w http.ResponseWriter, body io.ReadCloser, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(http.MaxBytesReader(w, body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	return data, nil
}

func writeReadErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeProblem(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeProblem(w, http.StatusBadRequest, "invalid request body")
}

// splitNDJSON returns non-blank lines without their trailing newline.
// Blank lines are not events, so they do not shift X-Factory-Event-Index.
func splitNDJSON(body []byte) [][]byte {
	if len(body) == 0 {
		return nil
	}
	parts := bytes.Split(body, []byte("\n"))
	lines := make([][]byte, 0, len(parts))
	for _, line := range parts {
		line = bytes.TrimRight(line, "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	return lines
}

func diagnosticsOrEmpty(d []Diagnostic) []Diagnostic {
	if d == nil {
		return []Diagnostic{}
	}
	return d
}

func allowedMethods(mux *http.ServeMux, r *http.Request) []string {
	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}
	allowed := make([]string, 0, 1)
	for _, method := range methods {
		clone := r.Clone(r.Context())
		clone.Method = method
		if _, pattern := mux.Handler(clone); pattern != "" {
			allowed = append(allowed, method)
		}
	}
	return allowed
}
