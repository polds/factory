// Package api is the factoryd REST surface for factories, agents, and runs.
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

// NewHandler serves the M2 REST API.
//
// Every route requires Authorization: Bearer apiKey except GET /healthz and
// POST /api/v1/agent/runs/{runId}/events:ingest. Ingest forwards its bearer
// token to Backend.IngestEvents and does not compare it to apiKey.
//
// events serves GET /api/v1/agent/runs/{runId}/events after the API key is
// accepted. A nil events handler responds 404 on that route.
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
	m.HandleFunc("GET /api/v1/factory/{uid}/agents", h.withAPIKey(h.handleListAgents))
	m.HandleFunc("GET /api/v1/factory/{uid}/runs", h.withAPIKey(h.handleListFactoryRuns))
	m.HandleFunc("POST /api/v1/agent/run", h.withAPIKey(h.handleStartRun))
	m.HandleFunc("GET /api/v1/agent/runs", h.withAPIKey(h.handleListRuns))
	m.HandleFunc("GET /api/v1/agent/runs/{runId}", h.withAPIKey(h.handleGetRun))
	m.HandleFunc("POST /api/v1/agent/runs/{runId}/cancel", h.withAPIKey(h.handleCancel))
	m.HandleFunc("GET /api/v1/agent/runs/{runId}/events", h.withAPIKey(h.handleEvents))
	m.HandleFunc("POST /api/v1/agent/runs/{runId}/events:ingest", h.handleIngest)
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
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	ref, diags, err := h.backend.ApplyDefinition(r.Context(), r.PathValue("uid"))
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
	runs, err := h.backend.ListRuns(r.Context(), RunFilter{
		FactoryID: factoryID,
		Agent:     q.Get("agent"),
		Status:    q.Get("status"),
		Worker:    q.Get("worker"),
		Model:     q.Get("model"),
	})
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
