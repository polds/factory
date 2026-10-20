package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const (
	benchmarkRunSuffix = ":run"
	captureTaskSuffix  = ":capture-benchmark-task"
)

func m6Routes() []consoleRoute {
	return []consoleRoute{
		{name: "score", pattern: "POST /api/v1/agent/runs/{runId}/score", method: http.MethodPost, sample: "/api/v1/agent/runs/run_1/score", body: `{"scorer":"correctness"}`, golden: "m6-score-accepted.json", status: http.StatusAccepted},
		{name: "scorer-results", pattern: "GET /api/v1/factory/{uid}/scorers/{id}/results", method: http.MethodGet, sample: "/api/v1/factory/fac_1/scorers/correctness/results?from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z&passed=false", golden: "m6-scorer-results.json", status: http.StatusOK},
		{name: "benchmark-run", pattern: "POST /api/v1/factory/{uid}/benchmarks/{suite}", method: http.MethodPost, sample: "/api/v1/factory/fac_1/benchmarks/pr-review:run", body: `{"repetitions":5}`, golden: "m6-benchmark-accepted.json", status: http.StatusAccepted},
		{name: "benchmark-trials", pattern: "GET /api/v1/factory/{uid}/benchmarks/{suite}/trials", method: http.MethodGet, sample: "/api/v1/factory/fac_1/benchmarks/pr-review/trials?benchmark_run_id=br_1", golden: "m6-benchmark-trials.json", status: http.StatusOK},
		{name: "improvement-batches", pattern: "GET /api/v1/factory/{uid}/improvement-batches", method: http.MethodGet, sample: "/api/v1/factory/fac_1/improvement-batches", golden: "m6-improvement-batches.json", status: http.StatusOK},
		{name: "improvement-run-now", pattern: "POST /api/v1/factory/{uid}/improvement:run-now", method: http.MethodPost, sample: "/api/v1/factory/fac_1/improvement:run-now", golden: "m6-improvement-run-now.json", status: http.StatusAccepted},
		{name: "scorers", pattern: "GET /api/v1/factory/{uid}/scorers", method: http.MethodGet, sample: "/api/v1/factory/fac_1/scorers", golden: "m6-scorers.json", status: http.StatusOK},
		{name: "run-scores", pattern: "GET /api/v1/agent/runs/{runId}/scores", method: http.MethodGet, sample: "/api/v1/agent/runs/run_1/scores", golden: "m6-run-scores.json", status: http.StatusOK},
		{name: "benchmarks", pattern: "GET /api/v1/factory/{uid}/benchmarks", method: http.MethodGet, sample: "/api/v1/factory/fac_1/benchmarks", golden: "m6-benchmarks.json", status: http.StatusOK},
		{name: "capture-benchmark-task", pattern: "POST /api/v1/agent/runs/{runId}", method: http.MethodPost, sample: "/api/v1/agent/runs/run_1:capture-benchmark-task", body: `{"suite":"pr-review","name":"broken-doc-link"}`, golden: "m6-capture-benchmark-task.json", status: http.StatusOK},
		{name: "metrics", pattern: "GET /api/v1/factory/{uid}/metrics", method: http.MethodGet, sample: "/api/v1/factory/fac_1/metrics?from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z", golden: "m6-metrics.json", status: http.StatusOK},
	}
}

func (h *handler) registerMeasure(m *http.ServeMux) {
	handlers := map[string]http.HandlerFunc{
		"score":                  h.handleScore,
		"scorer-results":         h.handleScorerResults,
		"benchmark-run":          h.handleBenchmarkRun,
		"benchmark-trials":       h.handleBenchmarkTrials,
		"improvement-batches":    h.handleImprovementBatches,
		"improvement-run-now":    h.handleImprovementRunNow,
		"scorers":                h.handleScorers,
		"run-scores":             h.handleRunScores,
		"benchmarks":             h.handleBenchmarks,
		"capture-benchmark-task": h.handleCaptureBenchmarkTask,
		"metrics":                h.handleMetrics,
	}
	for _, rt := range m6Routes() {
		fn, ok := handlers[rt.name]
		if !ok {
			panic("m6 route has no handler: " + rt.name)
		}
		m.HandleFunc(rt.pattern, h.withAPIKey(fn))
	}
}

func (h *handler) measure(w http.ResponseWriter) (MeasureBackend, bool) {
	mb, ok := h.backend.(MeasureBackend)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, measureUnwiredDetail)
		return nil, false
	}
	return mb, true
}

func (h *handler) writeMeasureErr(w http.ResponseWriter, err error) {
	var ineligible ErrRunIneligible
	switch {
	case errors.As(err, &ineligible):
		detail := ineligible.Reason
		if detail == "" {
			detail = ineligible.Error()
		}
		writeProblem(w, http.StatusUnprocessableEntity, detail)
	case errors.Is(err, ErrUnknownScorer):
		writeProblem(w, http.StatusUnprocessableEntity, "unknown scorer")
	case errors.Is(err, ErrBatchInFlight):
		writeProblem(w, http.StatusConflict, ErrBatchInFlight.Error())
	default:
		h.writeBackendErr(w, err)
	}
}

func (h *handler) handleScore(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	var req ScoreRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeReadErr(w, err)
		return
	}
	scorer := strings.TrimSpace(req.Scorer)
	if scorer == "" {
		writeProblem(w, http.StatusBadRequest, "scorer is required")
		return
	}
	accepted, err := mb.ScoreRun(r.Context(), r.PathValue("runId"), scorer)
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, accepted)
}

func (h *handler) handleScorerResults(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
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
	passed, err := parsePassed(q.Get("passed"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "passed is invalid")
		return
	}
	rows, err := mb.ScorerResults(r.Context(), r.PathValue("uid"), r.PathValue("id"), ScorerResultQuery{
		From:   from,
		To:     to,
		Passed: passed,
	})
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(rows))
}

func (h *handler) handleBenchmarkRun(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	suite, ok := cutSuffix(r.PathValue("suite"), benchmarkRunSuffix)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	req, err := decodeBenchmarkRun(w, r)
	if err != nil {
		writeReadErr(w, err)
		return
	}
	if req.Repetitions != nil && *req.Repetitions < 1 {
		writeProblem(w, http.StatusUnprocessableEntity, "repetitions must be at least 1")
		return
	}
	accepted, err := mb.StartBenchmark(r.Context(), r.PathValue("uid"), suite, req.Repetitions)
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, accepted)
}

func (h *handler) handleBenchmarkTrials(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	view, err := mb.BenchmarkTrials(r.Context(), r.PathValue("uid"), r.PathValue("suite"), r.URL.Query().Get("benchmark_run_id"))
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	view.Cells = orEmpty(view.Cells)
	for i := range view.Cells {
		view.Cells[i].TokensByModel = orEmpty(view.Cells[i].TokensByModel)
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *handler) handleImprovementBatches(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	rows, err := mb.ImprovementBatches(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	rows = orEmpty(rows)
	for i := range rows {
		rows[i].Failures = orEmpty(rows[i].Failures)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handler) handleImprovementRunNow(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	if _, err := readLimited(w, r.Body, maxBodyBytes); err != nil {
		writeReadErr(w, err)
		return
	}
	out, err := mb.RunImprovementNow(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	out.CheckedAgents = orEmpty(out.CheckedAgents)
	out.Batches = orEmpty(out.Batches)
	for i := range out.Batches {
		out.Batches[i].Failures = orEmpty(out.Batches[i].Failures)
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (h *handler) handleScorers(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	rows, err := mb.Scorers(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	rows = orEmpty(rows)
	for i := range rows {
		rows[i].Agents = orEmpty(rows[i].Agents)
		rows[i].Labels = orEmpty(rows[i].Labels)
		rows[i].PassRateByDay = orEmpty(rows[i].PassRateByDay)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handler) handleRunScores(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	rows, err := mb.RunScores(r.Context(), r.PathValue("runId"))
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(rows))
}

func (h *handler) handleBenchmarks(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	rows, err := mb.Benchmarks(r.Context(), r.PathValue("uid"))
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	rows = orEmpty(rows)
	for i := range rows {
		rows[i].Configurations = orEmpty(rows[i].Configurations)
		rows[i].Tasks = orEmpty(rows[i].Tasks)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handler) handleCaptureBenchmarkTask(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
	if !ok {
		return
	}
	runID, ok := cutSuffix(r.PathValue("runId"), captureTaskSuffix)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	var req CaptureBenchmarkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeReadErr(w, err)
		return
	}
	req.Suite = strings.TrimSpace(req.Suite)
	req.Name = strings.TrimSpace(req.Name)
	if req.Suite == "" {
		writeProblem(w, http.StatusBadRequest, "suite is required")
		return
	}
	if req.Name == "" {
		writeProblem(w, http.StatusBadRequest, "name is required")
		return
	}
	task, err := mb.CaptureBenchmarkTask(r.Context(), runID, req)
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	task.HeadRefs = orEmpty(task.HeadRefs)
	writeJSON(w, http.StatusOK, task)
}

func (h *handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	mb, ok := h.measure(w)
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
	view, err := mb.Metrics(r.Context(), r.PathValue("uid"), from, to)
	if err != nil {
		h.writeMeasureErr(w, err)
		return
	}
	view.TotalRuns.ByAgent = orEmpty(view.TotalRuns.ByAgent)
	view.TotalRuns.ByStatus = orEmpty(view.TotalRuns.ByStatus)
	view.TotalRuns.BySource = orEmpty(view.TotalRuns.BySource)
	view.TotalRuns.ByModel = orEmpty(view.TotalRuns.ByModel)
	view.CycleTime.Stages = orEmpty(view.CycleTime.Stages)
	view.MostExpensivePRs = orEmpty(view.MostExpensivePRs)
	view.ScorerCards = orEmpty(view.ScorerCards)
	for i := range view.ScorerCards {
		view.ScorerCards[i].PassRateByDay = orEmpty(view.ScorerCards[i].PassRateByDay)
	}
	view.ImprovementPRs = orEmpty(view.ImprovementPRs)
	writeJSON(w, http.StatusOK, view)
}

func cutSuffix(value, suffix string) (string, bool) {
	id, ok := strings.CutSuffix(value, suffix)
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

func parsePassed(v string) (*bool, error) {
	switch strings.TrimSpace(v) {
	case "":
		return nil, nil
	case "true":
		ok := true
		return &ok, nil
	case "false":
		ok := false
		return &ok, nil
	default:
		return nil, errors.New("passed is invalid")
	}
}

func decodeBenchmarkRun(w http.ResponseWriter, r *http.Request) (BenchmarkRunRequest, error) {
	body, err := readLimited(w, r.Body, maxBodyBytes)
	if err != nil {
		return BenchmarkRunRequest{}, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return BenchmarkRunRequest{}, nil
	}
	var req BenchmarkRunRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return BenchmarkRunRequest{}, errBadJSON
	}
	return req, nil
}
