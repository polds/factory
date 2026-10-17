package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// SetRunClass stores the classifier result on the run.
// Empty class and code clear the columns. A missing run returns ErrNotFound.
func (s *Store) SetRunClass(ctx context.Context, runID, class, code string) error {
	if runID == "" {
		return fmt.Errorf("store: run is required")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs
		SET failure_class = NULLIF($2, ''),
		    failure_code = NULLIF($3, '')
		WHERE id = $1`, runID, class, code)
	if err != nil {
		return fmt.Errorf("store: set run class: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type executionStamp struct {
	ID      string
	Started time.Time
	Ended   *time.Time
}

// RunTimeline builds queued, assigned, running, and terminal phases from
// runs and executions. One attempt produces one set of phases.
// Idle is omitted: no column records the idle window.
// DurationMS is the gap until the next phase, and 0 for the open phase.
func (s *Store) RunTimeline(ctx context.Context, runID string) ([]Phase, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, started_at, ended_at
		FROM executions
		WHERE run_id = $1
		ORDER BY attempt, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: timeline: %w", err)
	}
	defer rows.Close()
	execs := []executionStamp{}
	for rows.Next() {
		var ex executionStamp
		if err := rows.Scan(&ex.ID, &ex.Started, &ex.Ended); err != nil {
			return nil, fmt.Errorf("store: timeline: %w", err)
		}
		execs = append(execs, ex)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: timeline: %w", err)
	}
	return timelinePhases(run, execs), nil
}

func timelinePhases(run Run, execs []executionStamp) []Phase {
	if len(execs) == 0 {
		out := []Phase{{Phase: PhaseQueued, At: run.CreatedAt}}
		if run.AssignedAt != nil && !run.AssignedAt.Before(run.CreatedAt) {
			out = append(out, Phase{Phase: PhaseAssigned, At: *run.AssignedAt})
		}
		return phaseDurations(out)
	}
	out := make([]Phase, 0, len(execs)*4)
	for i, ex := range execs {
		queuedAt := run.CreatedAt
		if i > 0 && execs[i-1].Ended != nil {
			queuedAt = *execs[i-1].Ended
		}
		out = append(out, Phase{Phase: PhaseQueued, At: queuedAt, ExecutionID: ex.ID})
		if i == 0 && run.AssignedAt != nil && !run.AssignedAt.Before(queuedAt) && !run.AssignedAt.After(ex.Started) {
			out = append(out, Phase{Phase: PhaseAssigned, At: *run.AssignedAt, ExecutionID: ex.ID})
		}
		if !ex.Started.IsZero() {
			out = append(out, Phase{Phase: PhaseRunning, At: ex.Started, ExecutionID: ex.ID})
		}
		if ex.Ended != nil {
			out = append(out, Phase{Phase: PhaseTerminal, At: *ex.Ended, ExecutionID: ex.ID})
		}
	}
	return phaseDurations(out)
}

func phaseDurations(in []Phase) []Phase {
	for i := range in {
		if i+1 >= len(in) {
			continue
		}
		gap := in[i+1].At.Sub(in[i].At)
		if gap > 0 {
			in[i].DurationMS = gap.Milliseconds()
		}
	}
	return in
}

// RunUsage sums usage events. Other event types are ignored.
// A missing run returns ErrNotFound.
func (s *Store) RunUsage(ctx context.Context, runID string) (RunUsage, error) {
	if _, err := s.GetRun(ctx, runID); err != nil {
		return RunUsage{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, payload_json
		FROM run_events
		WHERE run_id = $1 AND type = 'usage'
		ORDER BY seq`, runID)
	if err != nil {
		return RunUsage{}, fmt.Errorf("store: usage: %w", err)
	}
	defer rows.Close()
	usage := RunUsage{ByModel: []ModelUsage{}, Calls: []UsageCall{}}
	by := map[string]*ModelUsage{}
	for rows.Next() {
		var seq int64
		var payload []byte
		if err := rows.Scan(&seq, &payload); err != nil {
			return RunUsage{}, fmt.Errorf("store: usage: %w", err)
		}
		call, err := usageCall(seq, payload)
		if err != nil {
			return RunUsage{}, err
		}
		usage.Calls = append(usage.Calls, call)
		usage.Total.InputTokens += call.InputTokens
		usage.Total.OutputTokens += call.OutputTokens
		usage.Total.CostCents += call.CostCents
		row := by[call.ModelID]
		if row == nil {
			row = &ModelUsage{ModelID: call.ModelID}
			by[call.ModelID] = row
		}
		row.InputTokens += call.InputTokens
		row.OutputTokens += call.OutputTokens
		row.CostCents += call.CostCents
		row.Calls++
	}
	if err := rows.Err(); err != nil {
		return RunUsage{}, fmt.Errorf("store: usage: %w", err)
	}
	names := make([]string, 0, len(by))
	for name := range by {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		usage.ByModel = append(usage.ByModel, *by[name])
	}
	return usage, nil
}

func usageCall(seq int64, payload []byte) (UsageCall, error) {
	var body struct {
		Turn         int    `json:"turn"`
		ModelID      string `json:"model_id"`
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
		CostCents    int64  `json:"cost_cents"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return UsageCall{}, fmt.Errorf("store: usage seq %d: %w", seq, err)
	}
	return UsageCall{
		Seq:          seq,
		Turn:         body.Turn,
		ModelID:      body.ModelID,
		InputTokens:  body.InputTokens,
		OutputTokens: body.OutputTokens,
		CostCents:    body.CostCents,
	}, nil
}

// FactoryEventsFrom returns events for factoryID with gid greater than cursor,
// oldest first. The cursor is exclusive. limit must be positive.
//
// The live tail is Listen(ctx, "run_events"). AppendEvents notifies that
// channel with the run id after commit. Wake on the notification, then read
// again from the last gid applied. Read once before Listen and once after
// it is registered so a commit between the two is not missed.
func (s *Store) FactoryEventsFrom(ctx context.Context, factoryID string, cursor int64, limit int) ([]FactoryEvent, error) {
	if err := s.requireFactory(ctx, s.pool, factoryID); err != nil {
		return nil, err
	}
	if cursor < 0 {
		return nil, fmt.Errorf("store: event cursor must be >= 0")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("store: event limit must be positive")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT
		    run_events.gid,
		    run_events.run_id,
		    run_events.seq,
		    run_events.execution_id,
		    run_events.idx,
		    run_events.ts,
		    run_events.type,
		    run_events.payload_json
		FROM run_events
		JOIN runs ON runs.id = run_events.run_id
		WHERE runs.factory_id = $1 AND run_events.gid > $2
		ORDER BY run_events.gid
		LIMIT $3`, factoryID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("store: factory events: %w", err)
	}
	defer rows.Close()
	out := []FactoryEvent{}
	for rows.Next() {
		var ev FactoryEvent
		var payload []byte
		if err := rows.Scan(
			&ev.GID, &ev.RunID, &ev.Seq, &ev.ExecutionID, &ev.Idx, &ev.TS, &ev.Type, &payload,
		); err != nil {
			return nil, fmt.Errorf("store: factory events: %w", err)
		}
		ev.Payload = json.RawMessage(payload)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: factory events: %w", err)
	}
	return out, nil
}

// Inbox returns what is waiting on a person, oldest first.
// There is no seen flag. A failed run stays until a newer run of the same
// work item succeeds, and it leaves the list seven days after it ended.
// failure_class cancelled is not waiting: a superseded run is classified
// cancelled and is omitted. Pending approvals and unanswered questions
// stay until they are decided or answered.
func (s *Store) Inbox(ctx context.Context, factoryID string) ([]InboxItem, error) {
	if err := s.requireFactory(ctx, s.pool, factoryID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT kind, id, work_item_id, run_id, created_at, title, detail, action
		FROM (
		    SELECT
		        'approval'::text AS kind,
		        approvals.id,
		        approvals.work_item_id,
		        ''::text AS run_id,
		        approvals.requested_at AS created_at,
		        work_items.title,
		        approvals.gate AS detail,
		        'approve'::text AS action
		    FROM approvals
		    JOIN work_items ON work_items.id = approvals.work_item_id
		    WHERE work_items.factory_id = $1 AND approvals.decision = ''
		    UNION ALL
		    SELECT
		        'question'::text,
		        input_requests.id,
		        input_requests.work_item_id,
		        input_requests.run_id,
		        input_requests.created_at,
		        work_items.title,
		        input_requests.question,
		        'answer'::text
		    FROM input_requests
		    JOIN work_items ON work_items.id = input_requests.work_item_id
		    WHERE work_items.factory_id = $1 AND input_requests.answered_at IS NULL
		    UNION ALL
		    SELECT
		        'failed_run'::text,
		        runs.id,
		        COALESCE(runs.work_item_id, ''),
		        runs.id,
		        COALESCE(runs.ended_at, runs.created_at),
		        COALESCE(work_items.title, runs.title),
		        COALESCE(NULLIF(runs.failure_code, ''), COALESCE(runs.failure_reason, '')),
		        'open'::text
		    FROM runs
		    LEFT JOIN work_items ON work_items.id = runs.work_item_id
		    WHERE runs.factory_id = $1
		      AND runs.status = 'failed'
		      AND runs.failure_class IS DISTINCT FROM 'cancelled'
		      AND COALESCE(runs.ended_at, runs.created_at) >= clock_timestamp() - interval '7 days'
		      AND NOT EXISTS (
		          SELECT 1
		          FROM runs AS newer
		          WHERE newer.work_item_id = runs.work_item_id
		            AND runs.work_item_id IS NOT NULL
		            AND newer.status = 'succeeded'
		            AND (newer.created_at, newer.id) > (runs.created_at, runs.id)
		      )
		) AS inbox
		ORDER BY created_at ASC, kind ASC, id ASC`, factoryID)
	if err != nil {
		return nil, fmt.Errorf("store: inbox: %w", err)
	}
	defer rows.Close()
	out := []InboxItem{}
	for rows.Next() {
		var item InboxItem
		if err := rows.Scan(
			&item.Kind, &item.ID, &item.WorkItemID, &item.RunID,
			&item.CreatedAt, &item.Title, &item.Detail, &item.Action,
		); err != nil {
			return nil, fmt.Errorf("store: inbox: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: inbox: %w", err)
	}
	return out, nil
}

// AutomationStats lists automations on the factory's current definition.
// runtime_paused comes from automation_overrides, which is keyed by name
// so it survives a later apply. match_count counts deliveries whose
// matched_automations contain the name. runs_started counts intake slots
// for this factory that stored a run id. last_fired_at is the later of
// the latest schedule fire and the latest matching delivery.
//
// ApplyDefinition does not write the automations table. Rows have to be
// inserted by a definition writer before this list is non-empty.
func (s *Store) AutomationStats(ctx context.Context, factoryID string) ([]AutomationStat, error) {
	if err := s.requireFactory(ctx, s.pool, factoryID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT
		    automations.name,
		    automations.enabled,
		    COALESCE(automation_overrides.paused, false),
		    automations.triggers_json,
		    (
		        SELECT max(fires.at)
		        FROM (
		            SELECT max(schedules.last_fired_at) AS at
		            FROM schedules
		            WHERE schedules.factory_id = automations.factory_id
		              AND schedules.automation = automations.name
		            UNION ALL
		            SELECT max(deliveries.received_at)
		            FROM deliveries
		            WHERE deliveries.factory_id = automations.factory_id
		              AND jsonb_exists(deliveries.matched_automations, automations.name)
		        ) AS fires
		    ),
		    (
		        SELECT count(*)::int
		        FROM deliveries
		        WHERE deliveries.factory_id = automations.factory_id
		          AND jsonb_exists(deliveries.matched_automations, automations.name)
		    ),
		    (
		        SELECT count(*)::int
		        FROM intake_slots
		        JOIN work_items ON work_items.id = intake_slots.work_item_id
		        WHERE work_items.factory_id = automations.factory_id
		          AND intake_slots.automation = automations.name
		          AND intake_slots.run_id <> ''
		    )
		FROM automations
		JOIN factories ON factories.current_definition_id = automations.definition_id
		LEFT JOIN automation_overrides
		    ON automation_overrides.factory_id = automations.factory_id
		   AND automation_overrides.name = automations.name
		WHERE factories.id = $1
		ORDER BY automations.name`, factoryID)
	if err != nil {
		return nil, fmt.Errorf("store: automation stats: %w", err)
	}
	defer rows.Close()
	out := []AutomationStat{}
	for rows.Next() {
		var stat AutomationStat
		var triggers []byte
		if err := rows.Scan(
			&stat.Name, &stat.Enabled, &stat.RuntimePaused, &triggers,
			&stat.LastFiredAt, &stat.MatchCount, &stat.RunsStarted,
		); err != nil {
			return nil, fmt.Errorf("store: automation stats: %w", err)
		}
		stat.Triggers = json.RawMessage(triggers)
		out = append(out, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: automation stats: %w", err)
	}
	return out, nil
}

// SetAutomationPaused stores a runtime pause keyed by factory and name.
// The row is not tied to a definition id, so a later apply leaves it.
func (s *Store) SetAutomationPaused(ctx context.Context, factoryID, name string, paused bool) error {
	if name == "" {
		return fmt.Errorf("store: automation name is required")
	}
	if err := s.requireFactory(ctx, s.pool, factoryID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO automation_overrides (factory_id, name, paused, updated_at)
		VALUES ($1, $2, $3, clock_timestamp())
		ON CONFLICT (factory_id, name) DO UPDATE
		SET paused = EXCLUDED.paused,
		    updated_at = clock_timestamp()`, factoryID, name, paused); err != nil {
		return fmt.Errorf("store: pause automation: %w", err)
	}
	return nil
}

// FactorySummary aggregates the operational dashboard for [from, to).
// ActiveRuns and WaitingOnHuman ignore the window: a run created earlier
// can still be in progress, and a gate stays until a person acts.
// PRsOpened counts work items created in the window with a non-empty
// open_pr_url. There is no column for when that URL was set.
func (s *Store) FactorySummary(ctx context.Context, factoryID string, from, to time.Time) (FactorySummary, error) {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return FactorySummary{}, fmt.Errorf("store: from and to must be a non-empty range")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return FactorySummary{}, fmt.Errorf("store: summary: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.requireFactory(ctx, tx, factoryID); err != nil {
		return FactorySummary{}, err
	}
	summary := FactorySummary{
		RunsByStatus:       map[string]int{},
		RunsByFailureClass: map[string]int{},
	}
	if err := fillStatusCounts(ctx, tx, factoryID, from, to, summary.RunsByStatus); err != nil {
		return FactorySummary{}, err
	}
	if err := fillClassCounts(ctx, tx, factoryID, from, to, summary.RunsByFailureClass); err != nil {
		return FactorySummary{}, err
	}
	var p50 *float64
	if err := tx.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (
		    ORDER BY extract(epoch FROM (assigned_at - created_at)) * 1000
		)
		FROM runs
		WHERE factory_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		  AND assigned_at IS NOT NULL`, factoryID, from, to).Scan(&p50); err != nil {
		return FactorySummary{}, fmt.Errorf("store: summary: %w", err)
	}
	if p50 != nil {
		ms := int64(math.Round(*p50))
		summary.QueueWaitP50MS = &ms
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM runs
		WHERE factory_id = $1 AND status IN ('queued', 'assigned', 'running')`,
		factoryID).Scan(&summary.ActiveRuns); err != nil {
		return FactorySummary{}, fmt.Errorf("store: summary: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT (
		    SELECT count(*)::int
		    FROM approvals
		    JOIN work_items ON work_items.id = approvals.work_item_id
		    WHERE work_items.factory_id = $1 AND approvals.decision = ''
		) + (
		    SELECT count(*)::int
		    FROM input_requests
		    JOIN work_items ON work_items.id = input_requests.work_item_id
		    WHERE work_items.factory_id = $1 AND input_requests.answered_at IS NULL
		)`, factoryID).Scan(&summary.WaitingOnHuman); err != nil {
		return FactorySummary{}, fmt.Errorf("store: summary: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM work_items
		WHERE factory_id = $1
		  AND open_pr_url <> ''
		  AND created_at >= $2
		  AND created_at < $3`, factoryID, from, to).Scan(&summary.PRsOpened); err != nil {
		return FactorySummary{}, fmt.Errorf("store: summary: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM deliveries
		WHERE factory_id = $1
		  AND status = 'unmatched'
		  AND received_at >= $2
		  AND received_at < $3`, factoryID, from, to).Scan(&summary.DeliveriesUnmatched); err != nil {
		return FactorySummary{}, fmt.Errorf("store: summary: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FactorySummary{}, fmt.Errorf("store: summary: %w", err)
	}
	return summary, nil
}

func fillStatusCounts(ctx context.Context, q rowQuery, factoryID string, from, to time.Time, dst map[string]int) error {
	rows, err := q.Query(ctx, `
		SELECT status, count(*)::int
		FROM runs
		WHERE factory_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY status`, factoryID, from, to)
	if err != nil {
		return fmt.Errorf("store: summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return fmt.Errorf("store: summary: %w", err)
		}
		dst[status] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: summary: %w", err)
	}
	return nil
}

func fillClassCounts(ctx context.Context, q rowQuery, factoryID string, from, to time.Time, dst map[string]int) error {
	rows, err := q.Query(ctx, `
		SELECT failure_class, count(*)::int
		FROM runs
		WHERE factory_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		  AND failure_class IS NOT NULL
		  AND failure_class <> ''
		GROUP BY failure_class`, factoryID, from, to)
	if err != nil {
		return fmt.Errorf("store: summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var class string
		var n int
		if err := rows.Scan(&class, &n); err != nil {
			return fmt.Errorf("store: summary: %w", err)
		}
		dst[class] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: summary: %w", err)
	}
	return nil
}

func (s *Store) requireFactory(ctx context.Context, q rowQuery, id string) error {
	if id == "" {
		return fmt.Errorf("store: factory is required")
	}
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM factories WHERE id = $1`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: factory: %w", err)
	}
	return nil
}
