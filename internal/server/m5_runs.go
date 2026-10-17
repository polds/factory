package server

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/store"
)

// runListLimit is the page size after filters. Filters are SQL predicates,
// so a match past the unfiltered 200th row is still returned.
const runListLimit = 200

const listedRunColumns = `
    r.id, r.factory_id, COALESCE(r.agent_id, ''), r.status, r.pool, r.priority, r.prompt, r.title, r.repo,
    COALESCE(r.worker_id, ''), COALESCE(r.failure_reason, ''), r.exit_code, r.cancel_requested,
    r.created_at, r.started_at, r.ended_at,
    COALESCE(r.work_item_id, ''), COALESCE(r.parent_run_id, ''), COALESCE(r.conversation_id, ''),
    COALESCE(r.stage, ''), COALESCE(r.model_id, ''),
    COALESCE(r.failure_class, ''), COALESCE(r.failure_code, ''), r.assigned_at,
    COALESCE(w.source, '')`

func scanListedRun(row pgx.Row) (store.Run, error) {
	var run store.Run
	var priority int
	err := row.Scan(
		&run.ID, &run.FactoryID, &run.AgentID, &run.Status, &run.Pool, &priority,
		&run.Prompt, &run.Title, &run.Repo, &run.WorkerID, &run.FailureReason,
		&run.ExitCode, &run.CancelRequested, &run.CreatedAt, &run.StartedAt, &run.EndedAt,
		&run.WorkItemID, &run.ParentRunID, &run.ConversationID, &run.Stage, &run.ModelID,
		&run.FailureClass, &run.FailureCode, &run.AssignedAt, &run.Source,
	)
	if err != nil {
		return store.Run{}, err
	}
	run.Priority = store.Priority(priority)
	return run, nil
}

// selectRuns applies every api.RunFilter field in SQL, before the limit.
// The model predicate matches runs.model_id or the agent snapshot model.
// StartRun does not set model_id, and the previous in-memory filter used
// the snapshot. The agent predicate matches agent_id or agents.name.
func (s *Server) selectRuns(ctx context.Context, f api.RunFilter) ([]store.Run, error) {
	factoryID := ""
	if f.FactoryID != "" {
		fac, err := s.factory(ctx, f.FactoryID)
		if err != nil {
			return nil, err
		}
		factoryID = fac.ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+listedRunColumns+`
		FROM runs r
		LEFT JOIN agents a ON a.id = r.agent_id
		LEFT JOIN work_items w ON w.id = r.work_item_id
		WHERE ($1 = '' OR r.factory_id = $1)
		  AND ($2 = '' OR r.agent_id = $2 OR a.name = $2)
		  AND ($3 = '' OR r.status = $3)
		  AND ($4 = '' OR COALESCE(r.worker_id, '') = $4)
		  AND (
		    $5 = ''
		    OR r.model_id = $5
		    OR COALESCE(a.spec_json->>'model', '') = $5
		  )
		  AND ($6 = '' OR COALESCE(r.failure_class, '') = $6)
		  AND ($7 = '' OR w.source = $7)
		  AND ($8 = '' OR r.parent_run_id = $8)
		  AND ($9 = '' OR r.work_item_id = $9)
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $10`,
		factoryID, f.Agent, f.Status, f.Worker, f.Model, f.FailureClass, f.Source, f.ParentRunID, f.WorkItemID, runListLimit)
	if err != nil {
		return nil, fmt.Errorf("server: list runs: %w", err)
	}
	defer rows.Close()
	out := []store.Run{}
	for rows.Next() {
		run, err := scanListedRun(rows)
		if err != nil {
			return nil, fmt.Errorf("server: list runs: %w", err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server: list runs: %w", err)
	}
	return out, nil
}

func queueWaitMS(run store.Run, now time.Time) int64 {
	end := now
	switch {
	case run.AssignedAt != nil:
		end = *run.AssignedAt
	case run.Status == store.StatusQueued:
		end = now
	default:
		return 0
	}
	ms := end.Sub(run.CreatedAt).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

func execMS(run store.Run, now time.Time) int64 {
	if run.StartedAt == nil {
		return 0
	}
	end := now
	if run.EndedAt != nil {
		end = *run.EndedAt
	}
	ms := end.Sub(*run.StartedAt).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

func (s *Server) costByRun(ctx context.Context, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT run_id, COALESCE(SUM((payload_json->>'cost_cents')::bigint), 0)
		FROM run_events
		WHERE type = 'usage' AND run_id = ANY($1)
		GROUP BY run_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var cents int64
		if err := rows.Scan(&id, &cents); err != nil {
			return nil, err
		}
		out[id] = cents
	}
	return out, rows.Err()
}

func (s *Server) classByRun(ctx context.Context, runs []store.Run) map[string]failure.Result {
	out := make(map[string]failure.Result, len(runs))
	for _, run := range runs {
		result, err := s.failureInput(ctx, run.ID)
		if err != nil {
			continue
		}
		out[run.ID] = result
	}
	return out
}

func toRunRow(run store.Run, snap loaded, result failure.Result, cost *int64, now time.Time) api.RunRow {
	row := api.RunRow{
		Run:         toAPIRun(run, snap),
		QueueWaitMS: queueWaitMS(run, now),
		ExecMS:      execMS(run, now),
		CostCents:   cost,
	}
	if result.Class != "" {
		row.FailureClass = string(result.Class)
		row.FailureSummary = result.Summary
	}
	if result.Code != "" {
		row.FailureCode = result.Code
	}
	row.ExitCode = run.ExitCode
	return row
}

// withSource fills Source from the work item when the run is linked and
// the caller did not already join it. A missing work item leaves Source empty.
func (s *Server) withSource(ctx context.Context, run store.Run) store.Run {
	if run.WorkItemID == "" || run.Source != "" || s.db == nil {
		return run
	}
	var source string
	err := s.db.QueryRow(ctx, `SELECT COALESCE(source, '') FROM work_items WHERE id = $1`, run.WorkItemID).Scan(&source)
	if err != nil {
		return run
	}
	run.Source = source
	return run
}

func toFailure(result failure.Result) api.Failure {
	evidence := result.Evidence
	if evidence == nil {
		evidence = []string{}
	}
	return api.Failure{
		Class:     string(result.Class),
		Code:      result.Code,
		Summary:   result.Summary,
		Retryable: result.Retryable,
		Evidence:  evidence,
	}
}
