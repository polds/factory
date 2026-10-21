package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/scorer"
	"github.com/polds/factory/internal/store"
)

// apiFacade implements MeasureBackend. Inbox is declared here so the
// improvement rows merge into the existing inbox without editing m5_backend.go.
var _ api.MeasureBackend = apiFacade{}

func (f apiFacade) Inbox(ctx context.Context, factory string) ([]api.InboxItem, error) {
	items, err := f.Server.Inbox(ctx, factory)
	if err != nil {
		return nil, err
	}
	fac, err := f.factory(ctx, factory)
	if err != nil {
		return nil, err
	}
	batches, err := f.store.ImprovementBatches(ctx, fac.ID)
	if err != nil {
		return nil, err
	}
	for _, batch := range batches {
		if batch.Status != api.BatchPROpen {
			continue
		}
		items = append(items, api.InboxItem{
			Kind:      api.InboxKindImprovementPR,
			ID:        batch.ID,
			RunID:     batch.ImprovementRunID,
			CreatedAt: batch.CreatedAt,
			Title:     "improvement for " + batch.Agent,
			Detail:    batch.PRURL,
			Action:    api.InboxActionOpen,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (s *Server) ScorerResults(ctx context.Context, factory, name string, q api.ScorerResultQuery) ([]api.ScorerResult, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	if _, ok := s.scorerByName(name); !ok {
		return nil, api.ErrUnknownScorer
	}
	rows, err := s.store.ScorerResults(ctx, fac.ID, name, q.From, q.To, q.Passed)
	if err != nil {
		return nil, err
	}
	out := make([]api.ScorerResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, wireScorerResult(row))
	}
	return out, nil
}

func wireScorerResult(row store.ScorerResult) api.ScorerResult {
	return api.ScorerResult{
		ID:         row.ID,
		RunID:      row.RunID,
		Label:      row.Label,
		Score:      row.Score,
		Passed:     row.Passed,
		Reasoning:  row.Reasoning,
		Status:     row.Status,
		CreatedAt:  row.CreatedAt,
		ReviewedAt: row.ReviewedAt,
	}
}

func (s *Server) RunScores(ctx context.Context, runID string) ([]api.RunScore, error) {
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return nil, apiStoreErr(err)
	}
	rows, err := s.store.RunScores(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]api.RunScore, 0, len(rows))
	for _, row := range rows {
		out = append(out, wireRunScore(row))
	}
	return out, nil
}

// wireRunScore is the only store-to-wire mapping for a run score.
// scored_at is not on the wire yet. Add it here when the column exists.
func wireRunScore(row store.ScorerResult) api.RunScore {
	return api.RunScore{
		Scorer:    row.ScorerName,
		Label:     row.Label,
		Score:     row.Score,
		Passed:    row.Passed,
		Reasoning: row.Reasoning,
		Status:    row.Status,
	}
}

func (s *Server) Scorers(ctx context.Context, factory string) ([]api.ScorerSummary, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	snap := s.snapshot()
	if snap.tree == nil || snap.factory.ID != fac.ID {
		return nil, api.ErrNotFound
	}
	names := map[string]definition.Scorer{}
	for name, sc := range snap.tree.Scorers {
		names[name] = sc
	}
	if _, ok := names[scorer.Correctness(nil).Name]; !ok {
		names[scorer.Correctness(snap.tree).Name] = scorer.Correctness(snap.tree)
	}
	order := make([]string, 0, len(names))
	for name := range names {
		order = append(order, name)
	}
	sort.Strings(order)
	out := make([]api.ScorerSummary, 0, len(order))
	for _, name := range order {
		sc := names[name]
		days, err := s.store.ScorerPassRateByDay(ctx, fac.ID, name, time.Time{}, time.Time{})
		if err != nil {
			return nil, err
		}
		out = append(out, wireScorerSummary(sc, days))
	}
	return out, nil
}

func wireScorerSummary(sc definition.Scorer, days []api.DayPassRate) api.ScorerSummary {
	agents := make([]string, 0, len(sc.Agents))
	for _, agent := range sc.Agents {
		if agent.Name != "" {
			agents = append(agents, agent.Name)
		}
	}
	labels := make([]string, 0, len(sc.Labels))
	for _, label := range sc.Labels {
		labels = append(labels, label.Value)
	}
	return api.ScorerSummary{
		Name:            sc.Name,
		Agents:          agents,
		Labels:          labels,
		PassingScore:    sc.PassingScore,
		SamplingRate:    sc.SamplingRate,
		Model:           sc.Model,
		SelfImprovement: sc.SelfImprovement,
		PassRateByDay:   days,
	}
}

func (s *Server) Benchmarks(ctx context.Context, factory string) ([]api.BenchmarkSummary, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	snap := s.snapshot()
	if snap.tree == nil || snap.factory.ID != fac.ID {
		return nil, api.ErrNotFound
	}
	names := make([]string, 0, len(snap.tree.Benchmarks))
	for name := range snap.tree.Benchmarks {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]api.BenchmarkSummary, 0, len(names))
	for _, name := range names {
		suite := snap.tree.Benchmarks[name]
		item := api.BenchmarkSummary{
			Suite:          name,
			Agent:          suite.Agent,
			Configurations: configurationNames(suite),
			Tasks:          append([]string{}, suite.Tasks...),
		}
		last, err := s.store.LatestBenchmarkRun(ctx, fac.ID, name)
		if err == nil {
			item.LastRun = &api.BenchmarkLastRun{ID: last.ID, CreatedAt: last.CreatedAt, Repetitions: last.Repetitions}
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func configurationNames(suite definition.BenchmarkSuite) []string {
	out := make([]string, 0, len(suite.Configurations))
	for _, cfg := range suite.Configurations {
		out = append(out, cfg.Name)
	}
	return out
}

func (s *Server) ImprovementBatches(ctx context.Context, factory string) ([]api.ImprovementBatch, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	rows, err := s.store.ImprovementBatches(ctx, fac.ID)
	if err != nil {
		return nil, err
	}
	out := make([]api.ImprovementBatch, 0, len(rows))
	for _, row := range rows {
		out = append(out, wireImprovementBatch(row))
	}
	return out, nil
}

func wireImprovementBatch(row store.ImprovementBatch) api.ImprovementBatch {
	failures := make([]api.ImprovementFailure, 0, len(row.Failures))
	for _, failure := range row.Failures {
		failures = append(failures, api.ImprovementFailure{
			RunID:    failure.RunID,
			ResultID: failure.ResultID,
			Scorer:   failure.Scorer,
			Label:    failure.Label,
		})
	}
	return api.ImprovementBatch{
		ID:        row.ID,
		Agent:     row.Agent,
		Status:    row.Status,
		Failures:  failures,
		PRURL:     row.PRURL,
		CreatedAt: row.CreatedAt,
	}
}

func (s *Server) Metrics(ctx context.Context, factory string, from, to time.Time) (api.FactoryMetrics, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return api.FactoryMetrics{}, apiStoreErr(err)
	}
	metrics, err := s.store.Metrics(ctx, fac.ID, from, to)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	return metrics, nil
}

func (s *Server) CaptureBenchmarkTask(ctx context.Context, runID string, req api.CaptureBenchmarkRequest) (api.CapturedBenchmarkTask, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return api.CapturedBenchmarkTask{}, apiStoreErr(err)
	}
	if purposeOrWork(run.Purpose) != api.PurposeWork {
		return api.CapturedBenchmarkTask{}, api.ErrRunIneligible{Reason: fmt.Sprintf("purpose %q is not %s", purposeOrWork(run.Purpose), api.PurposeWork)}
	}
	if !terminalStatus(run.Status) {
		return api.CapturedBenchmarkTask{}, api.ErrRunIneligible{Reason: "run is not terminal"}
	}
	heads := frozenHeads(run.Repo)
	if len(heads) == 0 {
		return api.CapturedBenchmarkTask{}, api.ErrRunIneligible{Reason: "run has no frozen 40-hex head"}
	}
	content := captureYAML(run, heads)
	content = s.redact(content)
	refs := make([]api.HeadRef, 0, len(heads))
	for _, head := range heads {
		refs = append(refs, headRef(head))
	}
	return api.CapturedBenchmarkTask{
		Path:     "benchmarks/" + req.Suite + "/tasks/" + req.Name + ".yaml",
		Content:  content,
		HeadRefs: refs,
	}, nil
}

func frozenHeads(repo string) []definition.RepoRef {
	var out []definition.RepoRef
	for _, line := range strings.Fields(repo) {
		ref, err := definition.ParseRepoRef(line)
		if err != nil {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func captureYAML(run store.Run, heads []definition.RepoRef) string {
	title := strings.TrimSpace(run.Title)
	if title == "" {
		title = "Captured task"
	}
	prompt := strings.TrimSpace(run.Prompt)
	criteria := "The change matches the run outcome."
	var b strings.Builder
	fmt.Fprintf(&b, "title: %s\n", yamlScalar(title))
	fmt.Fprintf(&b, "prompt: %s\n", yamlScalar(prompt))
	fmt.Fprintf(&b, "successCriteria: %s\n", yamlScalar(criteria))
	fmt.Fprintf(&b, "sourceRunId: %s\n", yamlScalar(run.ID))
	b.WriteString("startingRepoRefs:\n")
	for _, head := range heads {
		fmt.Fprintf(&b, "  - %s\n", head.String())
	}
	return b.String()
}

func yamlScalar(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, ":#\n\"'") || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
	}
	return value
}
