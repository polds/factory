package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
)

const (
	// Size cuts from docs/08: S/M/L/XL at 100, 500, and 1,000 changed lines.
	sizeMedium = 100
	sizeLarge  = 500
	sizeXL     = 1000

	stageKickoffToPR   = "kickoff_to_pr"
	stagePRToReview    = "pr_to_first_review"
	stageReviewToMerge = "first_review_to_merge"
)

// Metrics is the docs/08 dashboard set for [from, to).
// Runs are counted by created_at, including measurement runs. Measurement
// is the scorer, benchmark, and improvement count inside those buckets.
// A run with no work item counts under an empty source name.
//
// PRs opened and PRs merged are separate counts, so merged can exceed
// opened. Autonomy is the share of merged PRs with no human push. n is
// that merged count. ForgeConnectedAt is left empty: installation rows
// have no connected-at timestamp, and the frozen DDL does not add one.
//
// Cycle time uses PRs merged in the window. Kickoff is the work item's
// created_at. A stage median skips a PR that lacks either end. p50 is
// kickoff to merge.
//
// Cost per PR is the median over PRs opened in the window. A run's usage
// cost includes descendant runs that have no work item of their own, and
// that sum is split by integer division across every PR on the work item,
// including PRs outside the window. Remainder cents are dropped. Most
// expensive PRs are the opened-in-window rows, highest cost first.
// Improvement PRs are batches that have a url, newest first, with no
// date filter. Scorer cards are the ok-result pass rate per UTC day.
func (s *Store) Metrics(ctx context.Context, factory string, from, to time.Time) (api.FactoryMetrics, error) {
	if !to.After(from) {
		return api.FactoryMetrics{}, fmt.Errorf("store: metrics range must be non-empty")
	}
	if err := s.requireFactory(ctx, s.pool, factory); err != nil {
		return api.FactoryMetrics{}, err
	}
	out := emptyMetrics()
	var err error
	out.TotalRuns.ByAgent, err = s.namedCounts(ctx, "store: metrics by agent", `
		SELECT COALESCE(a.name, ''), count(*)::int
		FROM runs AS r
		LEFT JOIN agents AS a ON a.id = r.agent_id
		WHERE r.factory_id = $1 AND r.created_at >= $2 AND r.created_at < $3
		GROUP BY 1
		ORDER BY 1`, factory, from, to)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	out.TotalRuns.ByStatus, err = s.namedCounts(ctx, "store: metrics by status", `
		SELECT status, count(*)::int
		FROM runs
		WHERE factory_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY status
		ORDER BY status`, factory, from, to)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	out.TotalRuns.BySource, err = s.namedCounts(ctx, "store: metrics by source", `
		SELECT COALESCE(w.source, ''), count(*)::int
		FROM runs AS r
		LEFT JOIN work_items AS w ON w.id = r.work_item_id
		WHERE r.factory_id = $1 AND r.created_at >= $2 AND r.created_at < $3
		GROUP BY 1
		ORDER BY 1`, factory, from, to)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	out.TotalRuns.ByModel, err = s.namedCounts(ctx, "store: metrics by model", `
		SELECT COALESCE(model_id, ''), count(*)::int
		FROM runs
		WHERE factory_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY 1
		ORDER BY 1`, factory, from, to)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	err = s.pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM runs
		WHERE factory_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		  AND purpose IN ($4, $5, $6)`,
		factory, from, to, api.PurposeScorer, api.PurposeBenchmark, api.PurposeImprovement,
	).Scan(&out.TotalRuns.Measurement)
	if err != nil {
		return api.FactoryMetrics{}, fmt.Errorf("store: metrics measurement: %w", err)
	}
	facts, err := s.metricFacts(ctx, factory)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	runs, err := s.metricRuns(ctx, factory)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	costs := attributePRCosts(runs, facts)
	fillPRMetrics(&out, facts, costs, from, to)
	days, err := s.passDays(ctx, factory, "", from, to)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	out.ScorerCards = scorerCards(days)
	prs, err := s.improvementPRs(ctx, factory)
	if err != nil {
		return api.FactoryMetrics{}, err
	}
	out.ImprovementPRs = prs
	return out, nil
}

func emptyMetrics() api.FactoryMetrics {
	return api.FactoryMetrics{
		TotalRuns: api.RunTotals{
			ByAgent:  []api.NamedCount{},
			ByStatus: []api.NamedCount{},
			BySource: []api.NamedCount{},
			ByModel:  []api.NamedCount{},
		},
		CycleTime: api.CycleTimeMetric{Stages: []api.CycleTimeStage{
			{Name: stageKickoffToPR},
			{Name: stagePRToReview},
			{Name: stageReviewToMerge},
		}},
		MostExpensivePRs: []api.ExpensivePR{},
		ScorerCards:      []api.ScorerCard{},
		ImprovementPRs:   []api.ImprovementPR{},
	}
}

func scanNamedCount(row pgx.Row) (api.NamedCount, error) {
	var out api.NamedCount
	err := row.Scan(&out.Name, &out.Count)
	if err != nil {
		return api.NamedCount{}, err
	}
	return out, nil
}

func (s *Store) namedCounts(ctx context.Context, op, query, factory string, from, to time.Time) ([]api.NamedCount, error) {
	rows, err := s.pool.Query(ctx, query, factory, from, to)
	return collect(rows, err, op, scanNamedCount)
}

type metricFact struct {
	Repo         string
	Number       int
	WorkItemID   string
	OpenedAt     *time.Time
	MergedAt     *time.Time
	FirstReview  *time.Time
	Kickoff      *time.Time
	HumanPush    bool
	ChangedLines *int
}

func scanMetricFact(row pgx.Row) (metricFact, error) {
	var out metricFact
	err := row.Scan(
		&out.Repo, &out.Number, &out.WorkItemID,
		&out.OpenedAt, &out.MergedAt, &out.FirstReview, &out.Kickoff,
		&out.HumanPush, &out.ChangedLines,
	)
	if err != nil {
		return metricFact{}, err
	}
	return out, nil
}

func (s *Store) metricFacts(ctx context.Context, factory string) ([]metricFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.repo, p.number, COALESCE(p.work_item_id, ''),
		       p.opened_at, p.merged_at, p.first_review_at, w.created_at,
		       p.human_push_seen, p.changed_lines
		FROM pr_facts AS p
		LEFT JOIN work_items AS w ON w.id = p.work_item_id
		WHERE p.factory_id = $1
		ORDER BY p.repo ASC, p.number ASC`, factory)
	return collect(rows, err, "store: metrics prs", scanMetricFact)
}

type metricRun struct {
	ID         string
	ParentID   string
	WorkItemID string
	Cost       int64
}

func scanMetricRun(row pgx.Row) (metricRun, error) {
	var out metricRun
	err := row.Scan(&out.ID, &out.ParentID, &out.WorkItemID, &out.Cost)
	if err != nil {
		return metricRun{}, err
	}
	return out, nil
}

func (s *Store) metricRuns(ctx context.Context, factory string) ([]metricRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, COALESCE(r.parent_run_id, ''), COALESCE(r.work_item_id, ''),
		       COALESCE(SUM(
		           CASE
		               WHEN jsonb_typeof(e.payload_json -> 'cost_cents') = 'number'
		               THEN (e.payload_json ->> 'cost_cents')::bigint
		               ELSE 0
		           END
		       ), 0)::bigint
		FROM runs AS r
		LEFT JOIN run_events AS e ON e.run_id = r.id AND e.type = 'usage'
		WHERE r.factory_id = $1
		GROUP BY r.id, r.parent_run_id, r.work_item_id`, factory)
	return collect(rows, err, "store: metrics run costs", scanMetricRun)
}

type pricedPR struct {
	metricFact
	Cost int64
}

func attributePRCosts(runs []metricRun, facts []metricFact) []pricedPR {
	byID := make(map[string]metricRun, len(runs))
	for _, run := range runs {
		byID[run.ID] = run
	}
	shares := map[string]int64{}
	for _, run := range runs {
		workItem := workItemOf(byID, run.ID)
		if workItem == "" {
			continue
		}
		shares[workItem] += run.Cost
	}
	counts := map[string]int{}
	for _, fact := range facts {
		if fact.WorkItemID != "" {
			counts[fact.WorkItemID]++
		}
	}
	out := make([]pricedPR, len(facts))
	for i, fact := range facts {
		var cost int64
		if n := counts[fact.WorkItemID]; fact.WorkItemID != "" && n > 0 {
			cost = shares[fact.WorkItemID] / int64(n)
		}
		out[i] = pricedPR{metricFact: fact, Cost: cost}
	}
	return out
}

func workItemOf(byID map[string]metricRun, id string) string {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		run, ok := byID[id]
		if !ok {
			return ""
		}
		if run.WorkItemID != "" {
			return run.WorkItemID
		}
		id = run.ParentID
	}
	return ""
}

func fillPRMetrics(out *api.FactoryMetrics, facts []metricFact, costs []pricedPR, from, to time.Time) {
	var opened []pricedPR
	var autonomous int
	var kickoff, review, merge, full []int64
	for i, fact := range facts {
		openedIn := inWindow(fact.OpenedAt, from, to)
		mergedIn := inWindow(fact.MergedAt, from, to)
		if openedIn {
			out.PRsOpened++
			opened = append(opened, costs[i])
		}
		if mergedIn {
			out.PRsMerged++
			if !fact.HumanPush {
				autonomous++
			}
			if ms, ok := between(fact.Kickoff, fact.OpenedAt); ok {
				kickoff = append(kickoff, ms)
			}
			if ms, ok := between(fact.OpenedAt, fact.FirstReview); ok {
				review = append(review, ms)
			}
			if ms, ok := between(fact.FirstReview, fact.MergedAt); ok {
				merge = append(merge, ms)
			}
			if ms, ok := between(fact.Kickoff, fact.MergedAt); ok {
				full = append(full, ms)
			}
		}
	}
	out.Autonomy.N = out.PRsMerged
	if out.PRsMerged > 0 {
		out.Autonomy.Share = float64(autonomous) / float64(out.PRsMerged)
	}
	out.CycleTime.P50MS = medianInt64(full)
	out.CycleTime.Stages = []api.CycleTimeStage{
		{Name: stageKickoffToPR, P50MS: medianInt64(kickoff)},
		{Name: stagePRToReview, P50MS: medianInt64(review)},
		{Name: stageReviewToMerge, P50MS: medianInt64(merge)},
	}
	openedCosts := make([]int64, len(opened))
	bySize := map[string][]int64{}
	for i, pr := range opened {
		openedCosts[i] = pr.Cost
		if pr.ChangedLines != nil {
			bySize[sizeBucket(*pr.ChangedLines)] = append(bySize[sizeBucket(*pr.ChangedLines)], pr.Cost)
		}
	}
	out.CostPerPR.MedianCents = medianInt64(openedCosts)
	out.CostPerPR.BySize = api.PRSizeCost{
		S:  medianInt64(bySize["S"]),
		M:  medianInt64(bySize["M"]),
		L:  medianInt64(bySize["L"]),
		XL: medianInt64(bySize["XL"]),
	}
	sort.Slice(opened, func(i, j int) bool {
		if opened[i].Cost != opened[j].Cost {
			return opened[i].Cost > opened[j].Cost
		}
		if opened[i].Repo != opened[j].Repo {
			return opened[i].Repo < opened[j].Repo
		}
		return opened[i].Number < opened[j].Number
	})
	out.MostExpensivePRs = make([]api.ExpensivePR, len(opened))
	for i, pr := range opened {
		lines := 0
		if pr.ChangedLines != nil {
			lines = *pr.ChangedLines
		}
		out.MostExpensivePRs[i] = api.ExpensivePR{
			Repo:         pr.Repo,
			Number:       pr.Number,
			CostCents:    pr.Cost,
			ChangedLines: lines,
			URL:          "https://github.com/" + pr.Repo + "/pull/" + strconv.Itoa(pr.Number),
		}
	}
}

func inWindow(at *time.Time, from, to time.Time) bool {
	return at != nil && !at.Before(from) && at.Before(to)
}

func between(start, end *time.Time) (int64, bool) {
	if start == nil || end == nil || end.Before(*start) {
		return 0, false
	}
	return end.Sub(*start).Milliseconds(), true
}

func sizeBucket(lines int) string {
	switch {
	case lines < sizeMedium:
		return "S"
	case lines < sizeLarge:
		return "M"
	case lines < sizeXL:
		return "L"
	default:
		return "XL"
	}
}

func medianInt64(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]int64(nil), vals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func scorerCards(days []passDay) []api.ScorerCard {
	var cards []api.ScorerCard
	index := map[string]int{}
	for _, day := range days {
		if day.N == 0 {
			continue
		}
		rate := api.DayPassRate{Day: day.Day, PassRate: float64(day.Pass) / float64(day.N), N: day.N}
		at, ok := index[day.Scorer]
		if !ok {
			index[day.Scorer] = len(cards)
			cards = append(cards, api.ScorerCard{
				Name:          day.Scorer,
				PassRateByDay: []api.DayPassRate{rate},
			})
			continue
		}
		cards[at].PassRateByDay = append(cards[at].PassRateByDay, rate)
	}
	if cards == nil {
		return []api.ScorerCard{}
	}
	return cards
}

func scanImprovementPR(row pgx.Row) (api.ImprovementPR, error) {
	var out api.ImprovementPR
	err := row.Scan(&out.BatchID, &out.Agent, &out.URL, &out.CreatedAt)
	if err != nil {
		return api.ImprovementPR{}, err
	}
	return out, nil
}

func (s *Store) improvementPRs(ctx context.Context, factory string) ([]api.ImprovementPR, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, agent_name, pr_url, created_at
		FROM improvement_batches
		WHERE factory_id = $1 AND pr_url <> ''
		ORDER BY created_at DESC, id DESC`, factory)
	return collect(rows, err, "store: metrics improvement prs", scanImprovementPR)
}
