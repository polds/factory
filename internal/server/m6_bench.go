package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/benchmark"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/scorer"
	"github.com/polds/factory/internal/store"
)

func (s *Server) StartBenchmark(ctx context.Context, factory, suiteName string, repetitions *int) (api.BenchmarkAccepted, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return api.BenchmarkAccepted{}, apiStoreErr(err)
	}
	snap := s.snapshot()
	if snap.tree == nil || snap.factory.ID != fac.ID {
		return api.BenchmarkAccepted{}, api.ErrNotFound
	}
	suite, ok := snap.tree.Benchmarks[suiteName]
	if !ok {
		return api.BenchmarkAccepted{}, api.ErrNotFound
	}
	reps := benchmark.DefaultMinRepetitions
	if repetitions != nil {
		reps = *repetitions
	}
	if reps < 1 {
		return api.BenchmarkAccepted{}, errors.New("server: repetitions must be >= 1")
	}
	tasks := snap.tree.BenchmarkTasks[suiteName]
	expanded, err := benchmark.Expand(suite, tasks, reps, snap.tree.Factory.BenchmarkSubs)
	if err != nil {
		return api.BenchmarkAccepted{}, err
	}
	shaByRef := map[string]string{}
	for _, trial := range expanded {
		ref := strings.TrimSpace(trial.DefinitionRef)
		if _, seen := shaByRef[ref]; seen {
			continue
		}
		sha, err := s.resolveDefinitionSHA(ctx, ref)
		if err != nil {
			return api.BenchmarkAccepted{}, err
		}
		shaByRef[ref] = sha
	}
	benchRun, err := s.store.CreateBenchmarkRun(ctx, fac.ID, suiteName, "api", reps)
	if err != nil {
		return api.BenchmarkAccepted{}, err
	}
	agentID := snap.byName[suite.Agent]
	pool := s.cfg.WorkerPool
	if bound, ok := snap.byID[agentID]; ok && bound.agent.Pool != "" {
		pool = bound.agent.Pool
	}
	for _, trial := range expanded {
		task := tasks[trial.Task]
		model := ""
		if binding, ok := trial.Bindings[suite.Agent]; ok {
			model = binding.Model
		}
		run, err := s.store.CreateRun(ctx, store.NewRun{
			FactoryID: fac.ID,
			AgentID:   agentID,
			Pool:      pool,
			Priority:  store.PriorityBatch,
			Prompt:    task.Prompt,
			Title:     taskTitle(task, trial.Task),
			ModelID:   model,
			Purpose:   api.PurposeBenchmark,
		})
		if err != nil {
			return api.BenchmarkAccepted{}, err
		}
		if _, err := s.store.RecordTrial(ctx, store.BenchmarkTrial{
			BenchmarkRunID: benchRun.ID,
			Configuration:  trial.Configuration,
			Task:           trial.Task,
			Repetition:     trial.Repetition,
			RunID:          run.ID,
			Outcome:        api.TrialPending,
			DefinitionSHA:  shaByRef[strings.TrimSpace(trial.DefinitionRef)],
		}); err != nil {
			return api.BenchmarkAccepted{}, err
		}
	}
	s.nudge()
	return api.BenchmarkAccepted{BenchmarkRunID: benchRun.ID}, nil
}

func taskTitle(task definition.BenchmarkTask, name string) string {
	if strings.TrimSpace(task.Title) != "" {
		return task.Title
	}
	return name
}

func (s *Server) BenchmarkTrials(ctx context.Context, factory, suiteName, benchmarkRunID string) (api.BenchmarkTrials, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return api.BenchmarkTrials{}, apiStoreErr(err)
	}
	benchRun, err := s.benchmarkRunFor(ctx, fac.ID, suiteName, benchmarkRunID)
	if err != nil {
		return api.BenchmarkTrials{}, err
	}
	cells, err := s.store.TrialCells(ctx, benchRun.ID)
	if err != nil {
		return api.BenchmarkTrials{}, err
	}
	trials, err := s.store.ListBenchmarkTrials(ctx, benchRun.ID)
	if err != nil {
		return api.BenchmarkTrials{}, err
	}
	tokens := s.tokensByCell(ctx, trials)
	view := make([]api.BenchmarkCell, 0, len(cells))
	compareCells := make([]benchmark.Cell, 0, len(cells))
	for _, cell := range cells {
		wire := wireTrialCell(cell)
		wire.TokensByModel = tokens[cell.Configuration+"\n"+cell.Task]
		view = append(view, wire)
		compareCells = append(compareCells, benchmark.Cell{
			Configuration: cell.Configuration,
			Task:          cell.Task,
			Pass:          cell.Pass,
			N:             cell.N,
			Errored:       cell.Errored,
		})
	}
	return api.BenchmarkTrials{
		Cells:  view,
		Report: benchmark.Compare(compareCells, benchmark.DefaultMinRepetitions),
	}, nil
}

func (s *Server) benchmarkRunFor(ctx context.Context, factoryID, suiteName, id string) (store.BenchmarkRun, error) {
	if strings.TrimSpace(id) == "" {
		row, err := s.store.LatestBenchmarkRun(ctx, factoryID, suiteName)
		if err != nil {
			return store.BenchmarkRun{}, apiStoreErr(err)
		}
		return row, nil
	}
	row, err := s.store.GetBenchmarkRun(ctx, id)
	if err != nil {
		return store.BenchmarkRun{}, apiStoreErr(err)
	}
	if row.FactoryID != factoryID || row.Suite != suiteName {
		return store.BenchmarkRun{}, api.ErrNotFound
	}
	return row, nil
}

func wireTrialCell(cell store.TrialCell) api.BenchmarkCell {
	out := api.BenchmarkCell{
		Configuration:    cell.Configuration,
		Task:             cell.Task,
		N:                cell.N,
		Pass:             cell.Pass,
		Errored:          cell.Errored,
		MedianCostCents:  cell.MedianCostCents,
		MedianDurationMS: cell.MedianDurationMS,
	}
	if cell.N > 0 {
		out.PassRate = float64(cell.Pass) / float64(cell.N)
		out.CILow, out.CIHigh = benchmark.Wilson(cell.Pass, cell.N)
	}
	return out
}

func (s *Server) tokensByCell(ctx context.Context, trials []store.BenchmarkTrial) map[string][]api.ModelTokens {
	grouped := map[string]map[string]*api.ModelTokens{}
	for _, trial := range trials {
		if trial.Outcome != api.TrialPass && trial.Outcome != api.TrialFail {
			continue
		}
		usage, err := s.store.RunUsage(ctx, trial.RunID)
		if err != nil {
			continue
		}
		cellKey := trial.Configuration + "\n" + trial.Task
		byModel := grouped[cellKey]
		if byModel == nil {
			byModel = map[string]*api.ModelTokens{}
			grouped[cellKey] = byModel
		}
		for _, row := range usage.ByModel {
			item := byModel[row.ModelID]
			if item == nil {
				item = &api.ModelTokens{ModelID: row.ModelID}
				byModel[row.ModelID] = item
			}
			item.InputTokens += row.InputTokens
			item.OutputTokens += row.OutputTokens
		}
	}
	out := map[string][]api.ModelTokens{}
	for cellKey, byModel := range grouped {
		names := make([]string, 0, len(byModel))
		for name := range byModel {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out[cellKey] = append(out[cellKey], *byModel[name])
		}
	}
	return out
}

func (s *Server) scoreBenchmarkTrial(ctx context.Context, run store.Run) error {
	trial, err := s.store.BenchmarkTrialByRun(ctx, run.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if trial.Outcome != api.TrialPending {
		return nil
	}
	if benchmarkErrored(run) {
		return s.store.SetTrialOutcome(ctx, run.ID, api.TrialErrored, 0, 0, "")
	}
	criteria := s.trialCriteria(ctx, trial)
	tree := s.snapshot().tree
	if strings.TrimSpace(trial.DefinitionSHA) != "" {
		if loaded, _, loadErr := s.treeForSHA(ctx, trial.DefinitionSHA); loadErr == nil && loaded != nil {
			tree = loaded
		}
	}
	correct := scorer.Correctness(tree)
	if _, _, err := s.ensureJudge(ctx, run, correct, judgeForce, criteria); err != nil {
		return err
	}
	for _, sc := range s.samplerScorers(tree) {
		if sc.Name == correct.Name {
			continue
		}
		facts := s.workFacts(ctx, run)
		facts.Purpose = api.PurposeWork
		facts.Status = failure.StatusSucceeded
		facts.FailureClass = ""
		facts.Terminal = true
		if ok, _ := scorer.Eligible(sc, facts); !ok {
			continue
		}
		if _, _, err := s.ensureJudge(ctx, run, sc, judgeForce, criteria); err != nil {
			s.log.Error("benchmark scorer", "run_id", run.ID, "scorer", sc.Name, "err", err)
		}
	}
	return nil
}

func benchmarkErrored(run store.Run) bool {
	if run.Status == failure.StatusCancelled || run.FailureClass == string(failure.ClassCancelled) {
		return true
	}
	switch run.FailureClass {
	case string(failure.ClassInfrastructure), string(failure.ClassConfiguration):
		return true
	default:
		return false
	}
}

func (s *Server) trialCriteria(ctx context.Context, trial store.BenchmarkTrial) string {
	tree := s.snapshot().tree
	if strings.TrimSpace(trial.DefinitionSHA) != "" {
		if loaded, _, err := s.treeForSHA(ctx, trial.DefinitionSHA); err == nil && loaded != nil {
			tree = loaded
		}
	}
	if tree == nil {
		return ""
	}
	benchRun, err := s.store.GetBenchmarkRun(ctx, trial.BenchmarkRunID)
	if err != nil {
		return ""
	}
	tasks := tree.BenchmarkTasks[benchRun.Suite]
	if task, ok := tasks[trial.Task]; ok {
		return task.SuccessCriteria
	}
	if prod := s.snapshot().tree; prod != nil {
		if task, ok := prod.BenchmarkTasks[benchRun.Suite][trial.Task]; ok {
			return task.SuccessCriteria
		}
	}
	return ""
}

func (s *Server) maybeTrialOutcome(ctx context.Context, parent store.Run, scorerName string, row store.ScorerResult) error {
	if parent.Purpose != api.PurposeBenchmark {
		return nil
	}
	if scorerName != scorer.Correctness(s.snapshot().tree).Name {
		return nil
	}
	trial, err := s.store.BenchmarkTrialByRun(ctx, parent.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if trial.Outcome != api.TrialPending {
		return nil
	}
	outcome := api.TrialErrored
	if row.Status == api.ScorerStatusOK {
		if row.Passed {
			outcome = api.TrialPass
		} else {
			outcome = api.TrialFail
		}
	}
	usage, err := s.store.RunUsage(ctx, parent.ID)
	if err != nil {
		usage = store.RunUsage{}
	}
	return s.store.SetTrialOutcome(ctx, parent.ID, outcome, usage.Total.CostCents, runDurationMS(parent), row.ID)
}

func runDurationMS(run store.Run) int64 {
	if run.StartedAt == nil || run.EndedAt == nil {
		return 0
	}
	d := run.EndedAt.Sub(*run.StartedAt)
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

// repairBenchmarkTrials finishes trials whose runs are terminal and whose
// outcome is still pending. A crash after the terminal commit and before the
// judge launch is repaired on the leader sweep. This read does not write;
// the writes go through store methods.
func (s *Server) repairBenchmarkTrials(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT t.run_id
		FROM benchmark_trials t
		JOIN runs r ON r.id = t.run_id
		WHERE t.outcome = $1
		  AND r.status IN ('succeeded', 'failed', 'cancelled')
		ORDER BY t.id ASC
		LIMIT 200`, api.TrialPending)
	if err != nil {
		return fmt.Errorf("server: repair trials: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("server: repair trials: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("server: repair trials: %w", err)
	}
	for _, id := range ids {
		run, err := s.store.GetRun(ctx, id)
		if err != nil {
			s.log.Error("repair trial", "run_id", id, "err", err)
			continue
		}
		if err := s.scoreBenchmarkTrial(ctx, run); err != nil {
			s.log.Error("repair trial", "run_id", id, "err", err)
		}
		if judge, ok := s.childJudge(ctx, id, judgeTitle(scorer.Correctness(s.snapshot().tree).Name)); ok && terminalStatus(judge.Status) {
			if err := s.recordJudgeVerdict(ctx, judge); err != nil {
				s.log.Error("repair verdict", "run_id", id, "err", err)
			}
		}
	}
	return nil
}
