package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func (s *Server) Definition(ctx context.Context, factory string) (api.DefinitionView, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return api.DefinitionView{}, err
	}
	snap := s.snapshot()
	view := api.DefinitionView{
		Source:   api.DefinitionLocal,
		Editable: true,
		Files:    []api.DefinitionFile{},
	}
	if s.apiBacked() {
		view.Source = api.DefinitionSourceAPI
		view.Editable = false
		view.Files = s.apiDefinitionFiles()
		return view, nil
	}
	if strings.TrimSpace(s.cfg.DefinitionGitURL) != "" {
		view.Source = api.DefinitionGit
		view.Editable = false
		view.ProductionRef = s.cfg.DefinitionGitProductionRef
		view.GitSHA = s.currentGitSHA(ctx, fac.ID)
		view.ForgeURL = forgeTreeURL(s.cfg.DefinitionGitURL, view.GitSHA, s.cfg.DefinitionGitSubdir)
	}
	dir := snap.dir
	if dir == "" {
		dir = s.cfg.DefinitionPath
	}
	files, err := definitionFiles(dir)
	if err != nil {
		return api.DefinitionView{}, err
	}
	view.Files = files
	return view, nil
}

func (s *Server) currentGitSHA(ctx context.Context, factoryID string) string {
	s.mu.Lock()
	sha := s.gitSHA
	s.mu.Unlock()
	if sha != "" {
		return sha
	}
	var stored string
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(d.git_sha, '')
		FROM factories f
		JOIN definitions d ON d.id = f.current_definition_id
		WHERE f.id = $1`, factoryID).Scan(&stored)
	if err != nil {
		return ""
	}
	return stored
}

func definitionDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func definitionFiles(dir string) ([]api.DefinitionFile, error) {
	if strings.TrimSpace(dir) == "" {
		return []api.DefinitionFile{}, nil
	}
	if !definitionDir(dir) {
		return []api.DefinitionFile{}, nil
	}
	var files []api.DefinitionFile
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, api.DefinitionFile{Path: rel, Content: string(body)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []api.DefinitionFile{}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (s *Server) RunDetail(ctx context.Context, id string) (api.RunDetail, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.RunDetail{}, api.ErrNotFound
		}
		return api.RunDetail{}, err
	}
	result, err := s.failureInput(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return api.RunDetail{}, err
	}
	now := s.now()
	run = s.withSource(ctx, run)
	detail := api.RunDetail{
		Run:         toAPIRun(run, s.snapshot()),
		Failure:     toFailure(result),
		QueueWaitMS: queueWaitMS(run, now),
		ExecMS:      execMS(run, now),
		Steer:       s.steerState(ctx, run),
		PRURL:       s.pullRequestURL(ctx, run.WorkItemID),
		ExitCode:    run.ExitCode,
	}
	costs, err := s.costByRun(ctx, []string{run.ID})
	if err != nil {
		return api.RunDetail{}, err
	}
	if cents, ok := costs[run.ID]; ok {
		detail.CostCents = &cents
	}
	snap, err := s.kubernetesSnapshot(ctx, run.ID)
	if err != nil {
		return api.RunDetail{}, err
	}
	detail.KubernetesSnapshot = snap
	return detail, nil
}

func (s *Server) pullRequestURL(ctx context.Context, workItemID string) string {
	if workItemID == "" {
		return ""
	}
	var url string
	if err := s.db.QueryRow(ctx, `
		SELECT COALESCE(open_pr_url, '') FROM work_items WHERE id = $1`, workItemID).Scan(&url); err != nil {
		return ""
	}
	return url
}

func (s *Server) ListRunRows(ctx context.Context, filter api.RunFilter) ([]api.RunRow, error) {
	runs, err := s.selectRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	costs, err := s.costByRun(ctx, ids)
	if err != nil {
		return nil, err
	}
	classes := s.classByRun(ctx, runs)
	snap := s.snapshot()
	now := s.now()
	out := make([]api.RunRow, 0, len(runs))
	for _, run := range runs {
		var cost *int64
		if cents, ok := costs[run.ID]; ok {
			cost = &cents
		}
		out = append(out, toRunRow(run, snap, classes[run.ID], cost, now))
	}
	return out, nil
}

func (s *Server) RunTimeline(ctx context.Context, id string) ([]api.Phase, error) {
	if _, err := s.store.GetRun(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, api.ErrNotFound
		}
		return nil, err
	}
	phases, err := s.store.RunTimeline(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, api.ErrNotFound
		}
		return nil, err
	}
	var harnessAt time.Time
	err = s.db.QueryRow(ctx, `
		SELECT ts FROM run_events
		WHERE run_id = $1 AND type = 'terminal'
		ORDER BY seq DESC
		LIMIT 1`, id).Scan(&harnessAt)
	if err != nil {
		harnessAt = time.Time{}
	}
	return withIdlePhase(phases, harnessAt), nil
}

// withIdlePhase inserts an idle phase from the harness terminal event to
// ended_at when the run stayed up after the harness finished. Store timeline
// has no idle column. Durations are recomputed across the inserted phase.
func withIdlePhase(phases []store.Phase, harnessAt time.Time) []api.Phase {
	out := make([]api.Phase, 0, len(phases)+1)
	inserted := harnessAt.IsZero()
	for _, phase := range phases {
		if !inserted && phase.Phase == store.PhaseTerminal && harnessAt.Before(phase.At) {
			out = append(out, api.Phase{
				Phase:       api.PhaseIdle,
				At:          harnessAt,
				ExecutionID: phase.ExecutionID,
			})
			inserted = true
		}
		out = append(out, api.Phase{
			Phase:       phase.Phase,
			At:          phase.At,
			ExecutionID: phase.ExecutionID,
		})
	}
	for i := range out {
		if i+1 >= len(out) {
			out[i].DurationMS = 0
			continue
		}
		gap := out[i+1].At.Sub(out[i].At)
		if gap > 0 {
			out[i].DurationMS = gap.Milliseconds()
		}
	}
	return out
}

func (s *Server) RunUsage(ctx context.Context, id string) (api.Usage, error) {
	usage, err := s.store.RunUsage(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.Usage{}, api.ErrNotFound
		}
		return api.Usage{}, err
	}
	out := api.Usage{
		Total: api.UsageTotal{
			InputTokens:  usage.Total.InputTokens,
			OutputTokens: usage.Total.OutputTokens,
			CostCents:    usage.Total.CostCents,
		},
		ByModel: make([]api.ModelUsage, 0, len(usage.ByModel)),
		Calls:   make([]api.UsageCall, 0, len(usage.Calls)),
	}
	for _, row := range usage.ByModel {
		out.ByModel = append(out.ByModel, api.ModelUsage{
			ModelID:      row.ModelID,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			CostCents:    row.CostCents,
		})
	}
	for _, call := range usage.Calls {
		out.Calls = append(out.Calls, api.UsageCall{
			Seq:          call.Seq,
			Turn:         call.Turn,
			ModelID:      call.ModelID,
			InputTokens:  call.InputTokens,
			OutputTokens: call.OutputTokens,
			CostCents:    call.CostCents,
		})
	}
	return out, nil
}

func (s *Server) Inbox(ctx context.Context, factory string) ([]api.InboxItem, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.Inbox(ctx, fac.ID)
	if err != nil {
		return nil, err
	}
	out := make([]api.InboxItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.InboxItem{
			Kind:       row.Kind,
			ID:         row.ID,
			WorkItemID: row.WorkItemID,
			RunID:      row.RunID,
			CreatedAt:  row.CreatedAt,
			Title:      row.Title,
			Detail:     row.Detail,
			Action:     row.Action,
		})
	}
	return out, nil
}

func (s *Server) Automations(ctx context.Context, factory string) ([]api.Automation, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.AutomationStats(ctx, fac.ID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Automation, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.Automation{
			Name:          row.Name,
			Enabled:       row.Enabled,
			RuntimePaused: row.RuntimePaused,
			Triggers:      triggerLabels(row.Triggers),
			LastFiredAt:   row.LastFiredAt,
			MatchCount:    row.MatchCount,
			RunsStarted:   row.RunsStarted,
		})
	}
	return out, nil
}

func triggerLabels(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}
	}
	var triggers []definition.Trigger
	if err := json.Unmarshal(raw, &triggers); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(triggers))
	for _, tr := range triggers {
		switch {
		case tr.Schedule != nil && tr.Schedule.Cron != "":
			if tr.Schedule.Name != "" {
				out = append(out, "schedule:"+tr.Schedule.Name)
				continue
			}
			out = append(out, "schedule:"+tr.Schedule.Cron)
		case tr.Provider != "" || tr.Event != "":
			out = append(out, tr.Provider+":"+tr.Event)
		default:
			encoded, err := json.Marshal(tr)
			if err != nil {
				continue
			}
			out = append(out, string(encoded))
		}
	}
	return out
}

func (s *Server) PauseAutomation(ctx context.Context, factory, name string) error {
	return s.setAutomationPaused(ctx, factory, name, true)
}

func (s *Server) ResumeAutomation(ctx context.Context, factory, name string) error {
	return s.setAutomationPaused(ctx, factory, name, false)
}

func (s *Server) setAutomationPaused(ctx context.Context, factory, name string, paused bool) error {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return err
	}
	if err := s.store.SetAutomationPaused(ctx, fac.ID, name, paused); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.ErrNotFound
		}
		return err
	}
	return nil
}

func (s *Server) Webhooks(ctx context.Context, factory string) ([]api.Webhook, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListWebhookRegistrations(ctx, fac.ID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Webhook, 0, len(rows))
	for _, row := range rows {
		// SecretRef and URLTokenHash are not copied. url_token ingress uses
		// a placeholder path so the token itself never appears.
		path := "/api/v1/webhooks/" + url.PathEscape(row.UID)
		if row.AuthMode == store.WebhookAuthURLToken {
			path = "/api/v1/webhooks/u/{url_token}"
		}
		hook := api.Webhook{
			UID:             row.UID,
			Name:            row.Name,
			IngressPath:     path,
			AuthMode:        row.AuthMode,
			SignatureScheme: row.SignatureScheme,
			Enabled:         row.Enabled,
		}
		var last *time.Time
		err := s.db.QueryRow(ctx, `
			SELECT max(received_at) FROM deliveries WHERE webhook_uid = $1`, row.UID).Scan(&last)
		if err == nil {
			hook.LastDeliveryAt = last
		}
		out = append(out, hook)
	}
	return out, nil
}

func (s *Server) Workers(ctx context.Context) (api.WorkersView, error) {
	now := s.now()
	workers, err := s.store.ListWorkers(ctx, now)
	if err != nil {
		return api.WorkersView{}, err
	}
	pools, err := s.store.PoolStats(ctx, now)
	if err != nil {
		return api.WorkersView{}, err
	}
	view := api.WorkersView{
		Workers: make([]api.Worker, 0, len(workers)),
		Pools:   make([]api.PoolStat, 0, len(pools)),
	}
	for _, row := range workers {
		view.Workers = append(view.Workers, api.Worker{
			ID:             row.ID,
			Pool:           row.Pool,
			Platform:       row.Platform,
			Backend:        row.Backend,
			Version:        row.Version,
			InFlight:       row.InFlight,
			Capacity:       row.MaxConcurrent,
			HeartbeatAgeMS: row.HeartbeatAgeMS,
			Reconnects:     row.Reconnects,
			Rejected1h:     row.Rejected1h,
		})
	}
	for _, row := range pools {
		// A nil p50 is 0 on the wire. The console renders "—" when both
		// queue depth and this value are 0, which is the empty-pool case.
		p50 := int64(0)
		if row.P50TimeToAssignMS != nil {
			p50 = *row.P50TimeToAssignMS
		}
		view.Pools = append(view.Pools, api.PoolStat{
			Pool:            row.Pool,
			QueueDepth:      row.QueueDepth,
			P50TimeToAssign: p50,
			Rejected1h:      row.Rejected1h,
		})
	}
	return view, nil
}

func (s *Server) Secrets(ctx context.Context, factory string) ([]api.Secret, error) {
	if _, err := s.factory(ctx, factory); err != nil {
		return nil, err
	}
	snap := s.snapshot()
	if snap.tree == nil {
		return []api.Secret{}, nil
	}
	type use struct {
		kind string
		name string
	}
	used := map[string][]use{}
	add := func(name, kind, owner string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		used[name] = append(used[name], use{kind: kind, name: owner})
	}
	for _, name := range snap.tree.Factory.Secrets {
		add(name, "factory", snap.tree.Factory.Name)
	}
	agentNames := make([]string, 0, len(snap.tree.Agents))
	for name := range snap.tree.Agents {
		agentNames = append(agentNames, name)
	}
	sort.Strings(agentNames)
	for _, name := range agentNames {
		agent := snap.tree.Agents[name]
		for _, secret := range agent.Secrets {
			add(secret, "agent", name)
		}
	}
	autoNames := make([]string, 0, len(snap.tree.Automations))
	for name := range snap.tree.Automations {
		autoNames = append(autoNames, name)
	}
	sort.Strings(autoNames)
	for _, name := range autoNames {
		auto := snap.tree.Automations[name]
		for _, secret := range auto.Secrets {
			add(secret, "automation", name)
		}
	}
	hookNames := make([]string, 0, len(snap.tree.Webhooks))
	for name := range snap.tree.Webhooks {
		hookNames = append(hookNames, name)
	}
	sort.Strings(hookNames)
	for _, name := range hookNames {
		add(snap.tree.Webhooks[name].SecretName, "webhook", name)
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]api.Secret, 0, len(names))
	for _, name := range names {
		_, present := s.cfg.Secrets[name]
		uses := used[name]
		sec := api.Secret{
			Name:     name,
			Provider: "",
			Present:  present,
			UsedBy:   make([]api.SecretUse, 0, len(uses)),
		}
		for _, u := range uses {
			sec.UsedBy = append(sec.UsedBy, api.SecretUse{Kind: u.kind, Name: u.name})
		}
		out = append(out, sec)
	}
	return out, nil
}

func (s *Server) Identities(context.Context) ([]api.Identity, error) {
	out := []api.Identity{}
	if s.cfg.GitHubAppID > 0 {
		out = append(out, api.Identity{
			Subject: "github-app",
			Kind:    "github_app",
			Scope:   strconv.FormatInt(s.cfg.GitHubAppID, 10),
		})
	}
	if s.cfg.SlackBotToken != "" {
		out = append(out, api.Identity{
			Subject: "slack",
			Kind:    "slack_bot",
			Scope:   s.cfg.SlackTeamID,
		})
	}
	if s.cfg.LinearAPIKey != "" {
		out = append(out, api.Identity{
			Subject: "linear",
			Kind:    "linear",
		})
	}
	if strings.TrimSpace(s.cfg.InferenceBaseURL) != "" {
		out = append(out, api.Identity{
			Subject: "inference",
			Kind:    "inference",
			Scope:   s.cfg.InferenceBaseURL,
		})
	}
	return out, nil
}

func (s *Server) Summary(ctx context.Context, factory string, from, to time.Time) (api.Summary, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return api.Summary{}, err
	}
	row, err := s.store.FactorySummary(ctx, fac.ID, from, to)
	if err != nil {
		return api.Summary{}, err
	}
	// A nil p50 is 0 on the wire. The console treats 0 with an empty queue
	// as unknown and renders "—".
	p50 := int64(0)
	if row.QueueWaitP50MS != nil {
		p50 = *row.QueueWaitP50MS
	}
	byStatus := row.RunsByStatus
	if byStatus == nil {
		byStatus = map[string]int{}
	}
	byClass := row.RunsByFailureClass
	if byClass == nil {
		byClass = map[string]int{}
	}
	return api.Summary{
		RunsByStatus:        byStatus,
		RunsByFailureClass:  byClass,
		QueueWaitP50MS:      p50,
		ActiveRuns:          row.ActiveRuns,
		WaitingOnHuman:      row.WaitingOnHuman,
		PRsOpened:           row.PRsOpened,
		DeliveriesUnmatched: row.DeliveriesUnmatched,
	}, nil
}
