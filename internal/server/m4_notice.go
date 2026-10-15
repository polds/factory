package server

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/outbound"
	"github.com/polds/factory/internal/store"
)

var pullURLRe = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/\d+`)

func (s *Server) overlayOf(id string) (runOverlay, bool) {
	if id == "" {
		return runOverlay{}, false
	}
	s.integ.mu.Lock()
	defer s.integ.mu.Unlock()
	ov, ok := s.integ.overlay[id]
	return ov, ok
}

func (s *Server) putOverlay(id string, ov runOverlay) {
	if id == "" {
		return
	}
	s.integ.mu.Lock()
	defer s.integ.mu.Unlock()
	if s.integ.overlay == nil {
		s.integ.overlay = map[string]runOverlay{}
	}
	s.integ.overlay[id] = ov
}

func (s *Server) markStarted(id string) bool {
	if id == "" {
		return false
	}
	s.integ.mu.Lock()
	defer s.integ.mu.Unlock()
	if s.integ.started == nil {
		s.integ.started = map[string]struct{}{}
	}
	if _, ok := s.integ.started[id]; ok {
		return false
	}
	s.integ.started[id] = struct{}{}
	return true
}

func (s *Server) boundForRun(run store.Run, bound boundAgent) boundAgent {
	ov, ok := s.overlayOf(run.ID)
	if !ok || ov.Runner == "" {
		return bound
	}
	snap := s.snapshot()
	if snap.tree == nil {
		return bound
	}
	runner, ok := snap.tree.Runners[ov.Runner]
	if !ok {
		return bound
	}
	bound.runner = runner
	return bound
}

func (s *Server) recordOutcomes(ctx context.Context, res automation.Result) {
	for _, outcome := range res.Outcomes {
		if outcome.RunID != "" && (outcome.Runner != "" || outcome.Secrets != nil || len(outcome.RequiredRepos) > 0) {
			s.putOverlay(outcome.RunID, runOverlay{
				Runner:  outcome.Runner,
				Secrets: append([]string(nil), outcome.Secrets...),
				Repos:   append([]definition.Repository(nil), outcome.RequiredRepos...),
			})
		}
		if outcome.RunID == "" {
			continue
		}
		if outcome.Kind != automation.KindCreated && outcome.Kind != automation.KindFollowUp {
			continue
		}
		item, err := s.store.GetWorkItem(ctx, outcome.WorkItemID)
		if err != nil {
			continue
		}
		if !s.markStarted(outcome.RunID) {
			continue
		}
		s.notify(ctx, outbound.KindRunStarted, item, outcome.RunID, "", "")
	}
}

func (s *Server) noteAfterTerminal(ctx context.Context, runID string, before store.WorkItem) {
	if s.integ.notifier == nil || before.ID == "" {
		return
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	after, err := s.store.GetWorkItem(ctx, before.ID)
	if err != nil {
		return
	}
	if after.Stage != before.Stage || after.Status != before.Status {
		s.notify(ctx, outbound.KindStageChanged, after, run.ID, "", "")
	}
	if after.Status == store.WorkItemAwaitingApproval {
		s.notify(ctx, outbound.KindApprovalPending, after, run.ID, "", "")
	}
	if after.Status == store.WorkItemComplete && after.Stage == store.StageHumanHandoff {
		s.notify(ctx, outbound.KindHandoff, after, run.ID, "", "")
	}
	if run.Status == store.StatusFailed {
		s.notify(ctx, outbound.KindFailed, after, run.ID, run.FailureReason, "")
	}
	s.notifyQueued(ctx, after)
}

func (s *Server) notifyQueued(ctx context.Context, item store.WorkItem) {
	if item.ID == "" || s.db == nil {
		return
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM runs WHERE work_item_id = $1 AND status = $2`, item.ID, store.StatusQueued)
	if err != nil {
		return
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return
	}
	for _, id := range ids {
		if !s.markStarted(id) {
			continue
		}
		s.notify(ctx, outbound.KindRunStarted, item, id, "", "")
	}
}

func (s *Server) noteQuestion(ctx context.Context, runID string, lines [][]byte) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run.WorkItemID == "" {
		return
	}
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if err != nil {
		return
	}
	s.notify(ctx, outbound.KindQuestionAsked, item, run.ID, questionText(lines), "")
}

func (s *Server) noteIngest(ctx context.Context, runID string, lines [][]byte) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run.WorkItemID == "" {
		return
	}
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if err != nil {
		return
	}
	for _, line := range lines {
		var ev struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == "findings" || ev.Type == "review.findings" {
			s.notify(ctx, outbound.KindFindings, item, run.ID, ev.Text, "")
		}
		if pr := pullURLRe.Find(line); pr != nil {
			s.recordPullURL(ctx, item.ID, string(pr))
		}
	}
}

func questionText(lines [][]byte) string {
	for _, line := range lines {
		var ev struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Question string `json:"question"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Type != "question" {
			continue
		}
		if ev.Question != "" {
			return ev.Question
		}
		return ev.Text
	}
	return ""
}

func (s *Server) notify(ctx context.Context, kind outbound.Kind, item store.WorkItem, runID, text, prURL string) {
	if s.integ.notifier == nil || item.ID == "" {
		return
	}
	notice := outbound.Notice{
		Kind:           kind,
		WorkItemID:     item.ID,
		RunID:          runID,
		Stage:          item.Stage,
		Title:          item.Title,
		Text:           text,
		PRURL:          prURL,
		ConversationID: item.ConversationID,
		SlackTeam:      s.cfg.SlackTeamID,
		Channel:        slackChannel(s.snapshot().tree),
	}
	if repo, number, ok := parseIssueRef(item.SourceRef); ok {
		notice.Repo = repo
		notice.Number = number
	}
	if err := s.integ.notifier.Notify(ctx, notice); err != nil && s.log != nil {
		s.log.Debug("outbound", "kind", string(kind), "work_item_id", item.ID, "err", err)
	}
}

func slackChannel(tree *definition.Tree) string {
	if tree == nil {
		return ""
	}
	for _, integ := range tree.Factory.Integrations {
		if integ.Slack != nil && strings.TrimSpace(integ.Slack.Channel) != "" {
			return integ.Slack.Channel
		}
	}
	return ""
}

func parseIssueRef(ref string) (repo string, number int, ok bool) {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "github:")
	ownerRepo, rawNum, found := strings.Cut(ref, "#")
	if !found {
		return "", 0, false
	}
	owner, name, found := strings.Cut(ownerRepo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", 0, false
	}
	n, err := strconv.Atoi(rawNum)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return owner + "/" + name, n, true
}
