package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/scorer"
	"github.com/polds/factory/internal/store"
)

const (
	judgeTitlePrefix = "score:"
	judgeContextCap  = 16 * 1024
)

func judgeTitle(name string) string { return judgeTitlePrefix + name }

func (s *Server) ScoreRun(ctx context.Context, runID, name string) (api.ScoreAccepted, error) {
	sc, ok := s.scorerByName(name)
	if !ok {
		return api.ScoreAccepted{}, api.ErrUnknownScorer
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return api.ScoreAccepted{}, apiStoreErr(err)
	}
	facts := s.workFacts(ctx, run)
	if ok, why := scorer.Eligible(sc, facts); !ok {
		return api.ScoreAccepted{}, api.ErrRunIneligible{Reason: why}
	}
	result, judge, err := s.ensureJudge(ctx, run, sc, judgeReplace, "")
	if err != nil {
		return api.ScoreAccepted{}, err
	}
	return api.ScoreAccepted{ResultID: result.ID, JudgeRunID: judge.ID}, nil
}

type judgeMode int

const (
	judgeSample judgeMode = iota
	judgeForce
	judgeReplace
)

func (s *Server) scorerByName(name string) (definition.Scorer, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return definition.Scorer{}, false
	}
	tree := s.snapshot().tree
	if name == scorer.Correctness(nil).Name {
		return scorer.Correctness(tree), true
	}
	if tree == nil {
		return definition.Scorer{}, false
	}
	sc, ok := tree.Scorers[name]
	return sc, ok
}

func (s *Server) workFacts(ctx context.Context, run store.Run) scorer.RunFacts {
	return scorer.RunFacts{
		RunID:        run.ID,
		Agent:        s.agentName(ctx, run.AgentID),
		Status:       run.Status,
		FailureClass: run.FailureClass,
		Purpose:      purposeOrWork(run.Purpose),
		Terminal:     terminalStatus(run.Status),
		ParentAgents: s.parentAgentNames(ctx, run),
	}
}

func purposeOrWork(purpose string) string {
	if purpose == "" {
		return api.PurposeWork
	}
	return purpose
}

func terminalStatus(status string) bool {
	switch status {
	case failure.StatusSucceeded, failure.StatusFailed, failure.StatusCancelled:
		return true
	default:
		return false
	}
}

func (s *Server) agentName(ctx context.Context, agentID string) string {
	if agentID == "" {
		return ""
	}
	if bound, ok := s.snapshot().byID[agentID]; ok {
		return bound.name
	}
	if s.db == nil {
		return ""
	}
	var name string
	if err := s.db.QueryRow(ctx, `SELECT name FROM agents WHERE id = $1`, agentID).Scan(&name); err != nil {
		return ""
	}
	return name
}

func (s *Server) parentAgentNames(ctx context.Context, run store.Run) []string {
	var names []string
	seen := map[string]bool{}
	id := run.ParentRunID
	for id != "" && len(names) < 8 {
		parent, err := s.store.GetRun(ctx, id)
		if err != nil {
			break
		}
		if name := s.agentName(ctx, parent.AgentID); name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		id = parent.ParentRunID
	}
	return names
}

// samplePass is the leader sampler. It judges terminal work runs and repairs
// benchmark trials whose runs are already terminal. Upsert replaces a row, so
// a scorer that already has an ok or scorer_invalid result is left alone.
func (s *Server) samplePass(ctx context.Context) error {
	if err := s.sampleWork(ctx); err != nil {
		s.log.Error("sampler", "err", err)
	}
	if err := s.repairBenchmarkTrials(ctx); err != nil {
		s.log.Error("benchmark repair", "err", err)
	}
	return nil
}

func (s *Server) sampleWork(ctx context.Context) error {
	snap := s.snapshot()
	if snap.tree == nil || snap.factory.ID == "" || s.db == nil {
		return nil
	}
	runs, err := s.terminalWorkRuns(ctx, snap.factory.ID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, sc := range s.samplerScorers(snap.tree) {
			if _, _, err := s.ensureJudge(ctx, run, sc, judgeSample, ""); err != nil {
				s.log.Error("sample", "run_id", run.ID, "scorer", sc.Name, "err", err)
			}
		}
	}
	return nil
}

func (s *Server) samplerScorers(tree *definition.Tree) []definition.Scorer {
	if tree == nil {
		return nil
	}
	names := make([]string, 0, len(tree.Scorers))
	for name := range tree.Scorers {
		names = append(names, name)
	}
	sortStrings(names)
	out := make([]definition.Scorer, 0, len(names))
	for _, name := range names {
		out = append(out, tree.Scorers[name])
	}
	return out
}

// terminalWorkRuns lists recent terminal work runs. ListRuns cannot filter
// by purpose, so a status-only page is crowded by measurement runs. This
// read does not write.
func (s *Server) terminalWorkRuns(ctx context.Context, factoryID string) ([]store.Run, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM runs
		WHERE factory_id = $1
		  AND purpose = $2
		  AND status IN ('succeeded', 'failed', 'cancelled')
		ORDER BY created_at DESC
		LIMIT 200`, factoryID, api.PurposeWork)
	if err != nil {
		return nil, fmt.Errorf("server: list work runs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("server: list work runs: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server: list work runs: %w", err)
	}
	out := make([]store.Run, 0, len(ids))
	for _, id := range ids {
		run, err := s.store.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *Server) ensureJudge(ctx context.Context, parent store.Run, sc definition.Scorer, mode judgeMode, criteria string) (store.ScorerResult, store.Run, error) {
	rows, err := s.store.RunScores(ctx, parent.ID)
	if err != nil {
		return store.ScorerResult{}, store.Run{}, err
	}
	var existing *store.ScorerResult
	for i := range rows {
		if rows[i].ScorerName == sc.Name {
			existing = &rows[i]
			break
		}
	}
	if mode != judgeReplace && existing != nil && (existing.Status == api.ScorerStatusOK || existing.Status == api.ScorerStatusInvalid) {
		return *existing, store.Run{ID: existing.JudgeRunID}, nil
	}
	if mode == judgeSample && existing == nil {
		eligible, _ := scorer.Eligible(sc, s.workFacts(ctx, parent))
		if !eligible || !scorer.Sampled(parent.ID, sc.Name, sc.SamplingRate) {
			return store.ScorerResult{}, store.Run{}, nil
		}
	}
	title := judgeTitle(sc.Name)
	var judge store.Run
	if mode != judgeReplace && existing != nil && existing.JudgeRunID != "" {
		loaded, loadErr := s.store.GetRun(ctx, existing.JudgeRunID)
		if loadErr == nil {
			judge = loaded
		}
	}
	if judge.ID == "" && mode != judgeReplace {
		if found, ok := s.childJudge(ctx, parent.ID, title); ok {
			judge = found
		}
	}
	if judge.ID != "" && !terminalStatus(judge.Status) && mode != judgeReplace {
		if existing == nil || existing.JudgeRunID != judge.ID {
			row, upErr := s.upsertPending(ctx, parent, sc.Name, judge.ID)
			return row, judge, upErr
		}
		return *existing, judge, nil
	}
	if judge.ID != "" && terminalStatus(judge.Status) && mode != judgeReplace {
		if err := s.recordJudgeVerdict(ctx, judge); err != nil {
			return store.ScorerResult{}, judge, err
		}
		rows, err = s.store.RunScores(ctx, parent.ID)
		if err != nil {
			return store.ScorerResult{}, judge, err
		}
		for _, row := range rows {
			if row.ScorerName == sc.Name {
				return row, judge, nil
			}
		}
		return store.ScorerResult{}, judge, nil
	}
	if existing == nil || mode == judgeReplace {
		if _, err := s.upsertPending(ctx, parent, sc.Name, ""); err != nil {
			return store.ScorerResult{}, store.Run{}, err
		}
	}
	created, err := s.createJudgeRun(ctx, parent, sc, criteria)
	if err != nil {
		return store.ScorerResult{}, store.Run{}, err
	}
	row, err := s.upsertPending(ctx, parent, sc.Name, created.ID)
	if err != nil {
		return store.ScorerResult{}, created, err
	}
	s.nudge()
	return row, created, nil
}

func (s *Server) upsertPending(ctx context.Context, parent store.Run, name, judgeID string) (store.ScorerResult, error) {
	return s.store.UpsertScorerResult(ctx, store.ScorerResult{
		FactoryID:  parent.FactoryID,
		RunID:      parent.ID,
		ScorerName: name,
		JudgeRunID: judgeID,
		Status:     api.ScorerStatusPending,
	})
}

func (s *Server) createJudgeRun(ctx context.Context, parent store.Run, sc definition.Scorer, criteria string) (store.Run, error) {
	prompt := scorer.JudgePrompt(sc, s.judgeContext(ctx, parent, criteria))
	prompt = s.redact(prompt)
	model := strings.TrimSpace(sc.Model)
	if model == "" {
		if tree := s.snapshot().tree; tree != nil && tree.Factory.ScorerDefaults != nil {
			model = strings.TrimSpace(tree.Factory.ScorerDefaults.Model)
		}
	}
	pool := s.cfg.WorkerPool
	if pool == "" {
		pool = defaultWorkerPool
	}
	return s.store.CreateRun(ctx, store.NewRun{
		FactoryID:   parent.FactoryID,
		Pool:        pool,
		Priority:    store.PriorityBatch,
		Prompt:      prompt,
		Title:       judgeTitle(sc.Name),
		ParentRunID: parent.ID,
		ModelID:     model,
		Purpose:     api.PurposeScorer,
	})
}

// childJudge finds a judge run by parent and title. No store method lists
// runs by parent and purpose. This read does not write. The newest row is
// the one a crashed attach would have created.
func (s *Server) childJudge(ctx context.Context, parentID, title string) (store.Run, bool) {
	if s.db == nil || parentID == "" {
		return store.Run{}, false
	}
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM runs
		WHERE parent_run_id = $1 AND purpose = $2 AND title = $3
		ORDER BY created_at DESC
		LIMIT 1`, parentID, api.PurposeScorer, title).Scan(&id)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("judge lookup", "parent", parentID, "err", err)
		}
		return store.Run{}, false
	}
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return store.Run{}, false
	}
	return run, true
}

func (s *Server) judgeContext(ctx context.Context, run store.Run, criteria string) scorer.JudgeContext {
	secrets := s.secretMaterial()
	return scorer.JudgeContext{
		WorkItem:        capText(s.workItemText(ctx, run), judgeContextCap),
		Transcript:      capText(s.assistantTranscript(ctx, run.ID), judgeContextCap),
		Diff:            capText(s.eventSection(ctx, run.ID, true), judgeContextCap),
		ToolCallDigests: capText(s.eventSection(ctx, run.ID, false), judgeContextCap),
		Descendants:     capText(s.descendantText(ctx, run.ID), judgeContextCap),
		SuccessCriteria: capText(criteria, judgeContextCap),
		Secrets:         secrets,
	}
}

func (s *Server) workItemText(ctx context.Context, run store.Run) string {
	if run.WorkItemID != "" {
		item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
		if err == nil {
			return strings.TrimSpace(item.Title + "\n" + item.Body)
		}
	}
	return strings.TrimSpace(run.Title + "\n" + run.Prompt)
}

func (s *Server) assistantTranscript(ctx context.Context, runID string) string {
	events, err := s.store.EventsFrom(ctx, runID, 0, 500)
	if err != nil {
		return ""
	}
	var last string
	for _, ev := range events {
		var row struct {
			Type string `json:"type"`
			Role string `json:"role"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(ev.Payload, &row); err != nil {
			continue
		}
		typ := row.Type
		if typ == "" {
			typ = ev.Type
		}
		if typ != "message.delta" && typ != "message" {
			continue
		}
		if row.Role != "" && row.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(row.Text) != "" {
			last = row.Text
		}
	}
	return last
}

func (s *Server) eventSection(ctx context.Context, runID string, diffs bool) string {
	events, err := s.store.EventsFrom(ctx, runID, 0, 500)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, ev := range events {
		var row struct {
			Type         string `json:"type"`
			Tool         string `json:"tool"`
			InputDigest  string `json:"input_digest"`
			OutputDigest string `json:"output_digest"`
			Path         string `json:"path"`
			Text         string `json:"text"`
			Additions    int    `json:"additions"`
			Deletions    int    `json:"deletions"`
		}
		if err := json.Unmarshal(ev.Payload, &row); err != nil {
			continue
		}
		typ := row.Type
		if typ == "" {
			typ = ev.Type
		}
		if diffs {
			if !strings.Contains(typ, "diff") && row.Path == "" && row.Additions == 0 && row.Deletions == 0 {
				continue
			}
			fmt.Fprintf(&b, "%s %s +%d -%d %s\n", typ, row.Path, row.Additions, row.Deletions, row.Text)
			continue
		}
		if row.Tool == "" && row.InputDigest == "" && row.OutputDigest == "" && !strings.Contains(typ, "tool") {
			continue
		}
		fmt.Fprintf(&b, "%s %s in:%s out:%s\n", typ, row.Tool, row.InputDigest, row.OutputDigest)
	}
	return b.String()
}

// descendantText lists child runs. No store method lists runs by parent.
// This read does not write.
func (s *Server) descendantText(ctx context.Context, runID string) string {
	if s.db == nil || runID == "" {
		return ""
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, COALESCE(title, ''), status, purpose
		FROM runs
		WHERE parent_run_id = $1
		ORDER BY created_at ASC
		LIMIT 50`, runID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, title, status, purpose string
		if err := rows.Scan(&id, &title, &status, &purpose); err != nil {
			return b.String()
		}
		fmt.Fprintf(&b, "%s %s %s %s\n", id, purpose, status, title)
	}
	return b.String()
}

func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (s *Server) recordJudgeVerdict(ctx context.Context, judge store.Run) error {
	name := strings.TrimPrefix(judge.Title, judgeTitlePrefix)
	if name == "" || name == judge.Title || judge.ParentRunID == "" {
		return nil
	}
	parent, err := s.store.GetRun(ctx, judge.ParentRunID)
	if err != nil {
		return err
	}
	sc, ok := s.scorerByName(name)
	if !ok {
		sc = definition.Scorer{Name: name}
	}
	rows, err := s.store.RunScores(ctx, parent.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ScorerName == name && row.JudgeRunID == judge.ID &&
			(row.Status == api.ScorerStatusOK || row.Status == api.ScorerStatusInvalid) {
			return s.maybeTrialOutcome(ctx, parent, name, row)
		}
	}
	text := s.assistantTranscript(ctx, judge.ID)
	verdict, parseErr := scorer.ParseVerdict(sc, text)
	row := store.ScorerResult{
		FactoryID:  parent.FactoryID,
		RunID:      parent.ID,
		ScorerName: name,
		JudgeRunID: judge.ID,
		Status:     api.ScorerStatusInvalid,
	}
	if parseErr == nil {
		row.Status = api.ScorerStatusOK
		row.Label = verdict.Label
		row.Score = verdict.Score
		row.Passed = verdict.Passed
		row.Reasoning = s.redact(verdict.Reasoning)
	}
	saved, err := s.store.UpsertScorerResult(ctx, row)
	if err != nil {
		return err
	}
	return s.maybeTrialOutcome(ctx, parent, name, saved)
}

func (s *Server) noteMeasurement(ctx context.Context, runID string) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		s.log.Error("measurement", "run_id", runID, "err", err)
		return
	}
	switch run.Purpose {
	case api.PurposeScorer:
		if err := s.recordJudgeVerdict(ctx, run); err != nil {
			s.log.Error("judge verdict", "run_id", runID, "err", err)
		}
	case api.PurposeBenchmark:
		if err := s.scoreBenchmarkTrial(ctx, run); err != nil {
			s.log.Error("benchmark trial", "run_id", runID, "err", err)
		}
	case api.PurposeImprovement:
		if err := s.settleImprovementRun(ctx, run); err != nil {
			s.log.Error("improvement settle", "run_id", runID, "err", err)
		}
	}
}
