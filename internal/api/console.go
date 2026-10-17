package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/polds/factory/internal/events"
)

// maxSteerWait is the long-poll cap for GET …/steer.
const maxSteerWait = 25 * time.Second

// steerWait is the wait passed to NextSteers. Tests shorten it.
// Values above maxSteerWait are capped.
var steerWait = maxSteerWait

// consoleRoute is one M5 route. Registration and the handler tests share
// this list so a new route is covered by the 401, 404, and 501 walks.
type consoleRoute struct {
	name     string
	pattern  string
	method   string
	sample   string
	body     string
	golden   string
	runToken bool
	status   int
}

func consoleRoutes() []consoleRoute {
	return []consoleRoute{
		{name: "definition", pattern: "GET /api/v1/factory/{uid}/definition", method: http.MethodGet, sample: "/api/v1/factory/fac_1/definition", golden: "definition.json", status: http.StatusOK},
		{name: "timeline", pattern: "GET /api/v1/agent/runs/{runId}/timeline", method: http.MethodGet, sample: "/api/v1/agent/runs/run_1/timeline", golden: "timeline.json", status: http.StatusOK},
		{name: "usage", pattern: "GET /api/v1/agent/runs/{runId}/harness-usage", method: http.MethodGet, sample: "/api/v1/agent/runs/run_1/harness-usage", golden: "harness-usage.json", status: http.StatusOK},
		{name: "interrupt", pattern: "POST /api/v1/agent/runs/{runId}/interrupt", method: http.MethodPost, sample: "/api/v1/agent/runs/run_1/interrupt", golden: "interrupt.json", status: http.StatusAccepted},
		{name: "followup", pattern: "POST /api/v1/agent/runs/{runId}/followups", method: http.MethodPost, sample: "/api/v1/agent/runs/run_1/followups", body: `{"text":"try the other file"}`, golden: "followup.json", status: http.StatusAccepted},
		{name: "steer", pattern: "GET /api/v1/agent/runs/{runId}/steer", method: http.MethodGet, sample: "/api/v1/agent/runs/run_1/steer?after=steer_00", golden: "steer.json", runToken: true, status: http.StatusOK},
		{name: "events", pattern: "GET /api/v1/factory/{uid}/events", method: http.MethodGet, sample: "/api/v1/factory/fac_1/events", status: http.StatusOK},
		{name: "inbox", pattern: "GET /api/v1/factory/{uid}/inbox", method: http.MethodGet, sample: "/api/v1/factory/fac_1/inbox", golden: "inbox.json", status: http.StatusOK},
		{name: "automations", pattern: "GET /api/v1/factory/{uid}/automations", method: http.MethodGet, sample: "/api/v1/factory/fac_1/automations", golden: "automations.json", status: http.StatusOK},
		{name: "pause", pattern: "POST /api/v1/factory/{uid}/automations/{name}/pause", method: http.MethodPost, sample: "/api/v1/factory/fac_1/automations/on-label/pause", status: http.StatusNoContent},
		{name: "resume", pattern: "POST /api/v1/factory/{uid}/automations/{name}/resume", method: http.MethodPost, sample: "/api/v1/factory/fac_1/automations/on-label/resume", status: http.StatusNoContent},
		{name: "webhooks", pattern: "GET /api/v1/factory/{uid}/webhooks", method: http.MethodGet, sample: "/api/v1/factory/fac_1/webhooks", golden: "webhooks.json", status: http.StatusOK},
		{name: "workers", pattern: "GET /api/v1/agent/connected-self-hosted-workers", method: http.MethodGet, sample: "/api/v1/agent/connected-self-hosted-workers", golden: "workers.json", status: http.StatusOK},
		{name: "secrets", pattern: "GET /api/v1/factory/{uid}/secrets", method: http.MethodGet, sample: "/api/v1/factory/fac_1/secrets", golden: "secrets.json", status: http.StatusOK},
		{name: "identities", pattern: "GET /api/v1/agent/identities", method: http.MethodGet, sample: "/api/v1/agent/identities", golden: "identities.json", status: http.StatusOK},
		{name: "summary", pattern: "GET /api/v1/factory/{uid}/summary", method: http.MethodGet, sample: "/api/v1/factory/fac_1/summary?from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z", golden: "summary.json", status: http.StatusOK},
	}
}

func (h *handler) registerConsole(m *http.ServeMux) {
	handlers := map[string]http.HandlerFunc{
		"definition":  h.handleDefinition,
		"timeline":    h.handleTimeline,
		"usage":       h.handleUsage,
		"interrupt":   h.handleInterrupt,
		"followup":    h.handleFollowup,
		"steer":       h.handleSteer,
		"events":      h.handleFactoryEvents,
		"inbox":       h.handleInbox,
		"automations": h.handleAutomations,
		"pause":       h.handlePauseAutomation,
		"resume":      h.handleResumeAutomation,
		"webhooks":    h.handleWebhooks,
		"workers":     h.handleWorkers,
		"secrets":     h.handleSecrets,
		"identities":  h.handleIdentities,
		"summary":     h.handleSummary,
	}
	for _, rt := range consoleRoutes() {
		fn, ok := handlers[rt.name]
		if !ok {
			panic("console route has no handler: " + rt.name)
		}
		if rt.runToken {
			m.HandleFunc(rt.pattern, fn)
			continue
		}
		m.HandleFunc(rt.pattern, h.withAPIKey(fn))
	}
}

func (h *handler) console(w http.ResponseWriter) (ConsoleBackend, bool) {
	cb, ok := h.backend.(ConsoleBackend)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, consoleUnwiredDetail)
		return nil, false
	}
	return cb, true
}

func (h *handler) writeConsoleErr(w http.ResponseWriter, err error) {
	var steer ErrSteerUnavailable
	if errors.As(err, &steer) {
		reason := steer.Reason
		if reason == "" {
			reason = "steer unavailable"
		}
		writeProblemReason(w, http.StatusConflict, reason)
		return
	}
	h.writeBackendErr(w, err)
}

func (h *handler) handleDefinition(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	view, err := cb.Definition(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	view.Files = orEmpty(view.Files)
	writeJSON(w, http.StatusOK, view)
}

func (h *handler) handleTimeline(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	phases, err := cb.RunTimeline(r.Context(), r.PathValue("runId"))
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(phases))
}

func (h *handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	usage, err := cb.RunUsage(r.Context(), r.PathValue("runId"))
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	usage.ByModel = orEmpty(usage.ByModel)
	usage.Calls = orEmpty(usage.Calls)
	writeJSON(w, http.StatusOK, usage)
}

func (h *handler) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	id, err := cb.Interrupt(r.Context(), r.PathValue("runId"), APIKeyPrincipal)
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, SteerAck{SteerID: id})
}

type followupBody struct {
	Text string `json:"text"`
}

func (h *handler) handleFollowup(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	var body followupBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeReadErr(w, err)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeProblem(w, http.StatusBadRequest, "text is required")
		return
	}
	id, err := cb.Followup(r.Context(), r.PathValue("runId"), APIKeyPrincipal, text)
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, SteerAck{SteerID: id})
}

func (h *handler) handleSteer(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	cb, ok := h.console(w)
	if !ok {
		return
	}
	wait := steerWait
	if wait <= 0 || wait > maxSteerWait {
		wait = maxSteerWait
	}
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	steers, err := cb.NextSteers(ctx, tok, r.PathValue("runId"), r.URL.Query().Get("after"), wait)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeJSON(w, http.StatusOK, orEmpty(steers))
			return
		}
		h.writeConsoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(steers))
}

func (h *handler) handleFactoryEvents(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	from, err := events.ResumeGID(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid sequence")
		return
	}
	ch, err := cb.FactoryEvents(r.Context(), r.PathValue("uid"), from)
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	out := make(chan events.FactoryEvent)
	go forwardFactory(r.Context(), ch, out)
	events.WriteFactory(w, r, out)
}

func forwardFactory(ctx context.Context, in <-chan FactoryEvent, out chan<- events.FactoryEvent) {
	defer close(out)
	if in == nil {
		<-ctx.Done()
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-in:
			if !ok {
				return
			}
			msg := events.FactoryEvent{
				GID:   ev.GID,
				RunID: ev.RunID,
				Type:  ev.Type,
				Data:  append(json.RawMessage(nil), ev.Data...),
			}
			select {
			case <-ctx.Done():
				return
			case out <- msg:
			}
		}
	}
}

func (h *handler) handleInbox(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	items, err := cb.Inbox(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	items = orEmpty(items)
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		return items[i].Kind < items[j].Kind
	})
	writeJSON(w, http.StatusOK, items)
}

func (h *handler) handleAutomations(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	rows, err := cb.Automations(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	rows = orEmpty(rows)
	for i := range rows {
		rows[i].Triggers = orEmpty(rows[i].Triggers)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handler) handlePauseAutomation(w http.ResponseWriter, r *http.Request) {
	h.setAutomationPaused(w, r, true)
}

func (h *handler) handleResumeAutomation(w http.ResponseWriter, r *http.Request) {
	h.setAutomationPaused(w, r, false)
}

func (h *handler) setAutomationPaused(w http.ResponseWriter, r *http.Request, pause bool) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	var err error
	if pause {
		err = cb.PauseAutomation(r.Context(), r.PathValue("uid"), r.PathValue("name"))
	} else {
		err = cb.ResumeAutomation(r.Context(), r.PathValue("uid"), r.PathValue("name"))
	}
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	rows, err := cb.Webhooks(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(rows))
}

func (h *handler) handleWorkers(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	view, err := cb.Workers(r.Context())
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	view.Workers = orEmpty(view.Workers)
	view.Pools = orEmpty(view.Pools)
	writeJSON(w, http.StatusOK, view)
}

func (h *handler) handleSecrets(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	rows, err := cb.Secrets(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	rows = orEmpty(rows)
	for i := range rows {
		rows[i].UsedBy = orEmpty(rows[i].UsedBy)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handler) handleIdentities(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	rows, err := cb.Identities(r.Context())
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(rows))
}

func (h *handler) handleSummary(w http.ResponseWriter, r *http.Request) {
	cb, ok := h.console(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, err := parseQueryTime(q.Get("from"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "from is invalid")
		return
	}
	to, err := parseQueryTime(q.Get("to"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "to is invalid")
		return
	}
	sum, err := cb.Summary(r.Context(), r.PathValue("uid"), from, to)
	if err != nil {
		h.writeConsoleErr(w, err)
		return
	}
	if sum.RunsByStatus == nil {
		sum.RunsByStatus = map[string]int{}
	}
	if sum.RunsByFailureClass == nil {
		sum.RunsByFailureClass = map[string]int{}
	}
	writeJSON(w, http.StatusOK, sum)
}

func parseQueryTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

func normalizeRunDetail(d RunDetail) RunDetail {
	d.Failure.Evidence = orEmpty(d.Failure.Evidence)
	return d
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
