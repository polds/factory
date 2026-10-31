package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/failure"
	ghclient "github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/handoff"
	"github.com/polds/factory/internal/improve"
	"github.com/polds/factory/internal/store"
)

const (
	improveBranchPrefix      = "improve/"
	privilegeEscalationLabel = "privilege-escalation"
	factoryConfigCheck       = "factory/config"
	improveSettleLockKey     = "m6-improve-settle"

	// improveBranchAdvertiseFor is how long a succeeded improver waits
	// for a just-pushed improve/<batch> branch, or for the production
	// base ref, to show up on the remote. The wait is a deadline between
	// sweep ticks. It is not a sleep: the settle lock and the worker
	// read loop return as soon as the clone does.
	improveBranchAdvertiseFor = 45 * time.Second

	// improveCloneAttempts caps one retry counter per batch. A clone
	// error that is not a missing ref, and a pull-request create error
	// other than the handled 422, both increment it. After the cap the
	// batch is failed once.
	improveCloneAttempts = 3
)

func (s *Server) RunImprovementNow(ctx context.Context, factory string) (api.ImprovementRunNow, error) {
	if strings.TrimSpace(s.cfg.DefinitionGitURL) == "" {
		s.log.Info("self-improvement skipped: factory has no definition git url")
		return api.ImprovementRunNow{CheckedAgents: []string{}, Batches: []api.ImprovementBatch{}}, nil
	}
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return api.ImprovementRunNow{}, apiStoreErr(err)
	}
	return s.scheduleImprovements(ctx, fac.ID, true)
}

func (s *Server) improvePass(ctx context.Context) error {
	if strings.TrimSpace(s.cfg.DefinitionGitURL) == "" {
		s.log.Info("self-improvement skipped: factory has no definition git url")
		return nil
	}
	snap := s.snapshot()
	if snap.factory.ID == "" {
		return nil
	}
	if err := s.sweepImprovementBatches(ctx, snap.factory.ID); err != nil {
		s.log.Error("improvement sweep", "err", s.redact(err.Error()))
	}
	if _, err := s.scheduleImprovements(ctx, snap.factory.ID, false); err != nil {
		s.log.Error("improvement schedule", "err", s.redact(err.Error()))
	}
	return nil
}

func (s *Server) sweepImprovementBatches(ctx context.Context, factoryID string) error {
	batches, err := s.store.ImprovementBatches(ctx, factoryID)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch batch.Status {
		case api.BatchQueued:
			if batch.ImprovementRunID == "" {
				if err := s.startImprovementRun(ctx, batch); err != nil {
					s.log.Error("improvement run", "batch", batch.ID, "err", s.redact(err.Error()))
				}
			}
		case api.BatchRunning:
			if batch.PRURL != "" || batch.ImprovementRunID == "" {
				continue
			}
			run, err := s.store.GetRun(ctx, batch.ImprovementRunID)
			if err != nil {
				s.log.Error("improvement run", "batch", batch.ID, "err", s.redact(err.Error()))
				continue
			}
			if !terminalStatus(run.Status) {
				continue
			}
			if err := s.openImprovementPR(ctx, batch, run); err != nil {
				s.log.Error("improvement pr", "batch", batch.ID, "err", s.redact(err.Error()))
			}
		}
	}
	return nil
}

func (s *Server) sweepFactoryImprovements(ctx context.Context) error {
	id := s.snapshot().factory.ID
	if id == "" {
		return nil
	}
	return s.sweepImprovementBatches(ctx, id)
}

func (s *Server) settleImprovementRun(ctx context.Context, run store.Run) error {
	batches, err := s.store.ImprovementBatches(ctx, run.FactoryID)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if batch.ImprovementRunID == run.ID {
			return s.openImprovementPR(ctx, batch, run)
		}
	}
	return nil
}

func (s *Server) scheduleImprovements(ctx context.Context, factoryID string, runNow bool) (api.ImprovementRunNow, error) {
	snap := s.snapshot()
	failures, err := s.store.UnreviewedFailures(ctx, factoryID, "")
	if err != nil {
		return api.ImprovementRunNow{}, err
	}
	allowed := map[string]bool{}
	if snap.tree != nil {
		for name, sc := range snap.tree.Scorers {
			if sc.SelfImprovement {
				allowed[name] = true
			}
		}
	}
	var pending []improve.Failure
	for _, row := range failures {
		if !allowed[row.Scorer] {
			continue
		}
		run, err := s.store.GetRun(ctx, row.RunID)
		if err != nil {
			return api.ImprovementRunNow{}, err
		}
		// Measurement runs feed benchmarks and judges, not this loop.
		if purposeOrWork(run.Purpose) != api.PurposeWork {
			continue
		}
		pending = append(pending, improve.Failure{
			ResultID:  row.ResultID,
			RunID:     row.RunID,
			Agent:     row.Agent,
			Scorer:    row.Scorer,
			Label:     row.Label,
			Reasoning: row.Reasoning,
			At:        row.At,
		})
	}
	cfg := definition.SelfImprovement{}
	if snap.tree != nil && snap.tree.Factory.SelfImprovement != nil {
		cfg = *snap.tree.Factory.SelfImprovement
	}
	inFlight := map[string]bool{}
	if runNow {
		cfg.FailedRunThreshold = 1
	} else if batches, err := s.store.ImprovementBatches(ctx, factoryID); err == nil {
		for _, batch := range batches {
			switch batch.Status {
			case api.BatchQueued, api.BatchRunning, api.BatchPROpen:
				inFlight[batch.Agent] = true
			}
		}
	}
	due := improve.Due(s.now(), pending, cfg, inFlight)
	out := api.ImprovementRunNow{CheckedAgents: append([]string{}, due...), Batches: []api.ImprovementBatch{}}
	byAgent := map[string][]improve.Failure{}
	for _, row := range pending {
		byAgent[row.Agent] = append(byAgent[row.Agent], row)
	}
	for _, agent := range due {
		ids := failureIDs(byAgent[agent])
		batch, err := s.store.CreateBatch(ctx, store.NewImprovementBatch{
			FactoryID:  factoryID,
			Agent:      agent,
			FailureIDs: ids,
		})
		if errors.Is(err, api.ErrBatchInFlight) {
			if runNow {
				return api.ImprovementRunNow{}, err
			}
			s.log.Info("improvement batch in flight", "agent", agent)
			continue
		}
		if err != nil {
			return api.ImprovementRunNow{}, err
		}
		if err := s.startImprovementRun(ctx, batch); err != nil {
			return api.ImprovementRunNow{}, err
		}
		reloaded, err := s.reloadBatch(ctx, factoryID, batch.ID)
		if err != nil {
			return api.ImprovementRunNow{}, err
		}
		out.Batches = append(out.Batches, wireImprovementBatch(reloaded))
	}
	return out, nil
}

func failureIDs(rows []improve.Failure) []string {
	seen := map[string]bool{}
	var ids []string
	for _, row := range rows {
		if row.ResultID == "" || seen[row.ResultID] {
			continue
		}
		seen[row.ResultID] = true
		ids = append(ids, row.ResultID)
	}
	return ids
}

func (s *Server) reloadBatch(ctx context.Context, factoryID, id string) (store.ImprovementBatch, error) {
	batches, err := s.store.ImprovementBatches(ctx, factoryID)
	if err != nil {
		return store.ImprovementBatch{}, err
	}
	for _, batch := range batches {
		if batch.ID == id {
			return batch, nil
		}
	}
	return store.ImprovementBatch{}, store.ErrNotFound
}

func (s *Server) startImprovementRun(ctx context.Context, batch store.ImprovementBatch) error {
	return s.withImproveLock(ctx, func() error {
		current, err := s.reloadBatch(ctx, batch.FactoryID, batch.ID)
		if err != nil {
			return err
		}
		if current.ImprovementRunID != "" {
			return nil
		}
		snap := s.snapshot()
		pool := s.cfg.WorkerPool
		agentID := ""
		model := ""
		agent := definition.Agent{}
		includeAgent := false
		if id, ok := snap.byName[current.Agent]; ok {
			agentID = id
			if bound, ok := snap.byID[id]; ok {
				includeAgent = true
				agent = bound.agent
				if bound.agent.Pool != "" {
					pool = bound.agent.Pool
				}
				model = bound.agent.Model
			}
		}
		prompt := s.improvementPrompt(ctx, current, agent, includeAgent)
		run, err := s.store.CreateRun(ctx, store.NewRun{
			FactoryID: current.FactoryID,
			AgentID:   agentID,
			Pool:      pool,
			Priority:  store.PriorityBatch,
			Prompt:    prompt,
			Title:     "improve:" + current.Agent,
			ModelID:   model,
			Purpose:   api.PurposeImprovement,
		})
		if err != nil {
			return err
		}
		if err := s.store.SetBatchRun(ctx, current.ID, run.ID); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		s.nudge()
		return nil
	})
}

func batchFailures(batch store.ImprovementBatch) []improve.Failure {
	out := make([]improve.Failure, 0, len(batch.Failures))
	for _, row := range batch.Failures {
		out = append(out, improve.Failure{
			ResultID:  row.ResultID,
			RunID:     row.RunID,
			Agent:     batch.Agent,
			Scorer:    row.Scorer,
			Label:     row.Label,
			Reasoning: row.Reasoning,
			At:        row.At,
		})
	}
	return out
}

// improvementPrompt packages the bound agent's instructions the same way a
// stage run does. The definition URL is a secret, so the workspace section
// names DEFINITION_GIT_URL and does not include the value.
func (s *Server) improvementPrompt(ctx context.Context, batch store.ImprovementBatch, agent definition.Agent, includeAgent bool) string {
	task := improve.RunPrompt(batchFailures(batch), s.improvementTreeText(ctx, batch.ID), s.priorRejections(ctx, batch), s.secretMaterial())
	text := s.improvementWorkspace(batch, agent) + "\n" + task
	if !includeAgent {
		return text
	}
	return handoff.BuildPrompt(agent, handoff.Context{
		Title: "improve:" + batch.Agent,
		Body:  text,
		Env:   s.redactEnv(),
	})
}

func (s *Server) improvementWorkspace(batch store.ImprovementBatch, agent definition.Agent) string {
	branch := improveBranchPrefix + batch.ID
	var b strings.Builder
	b.WriteString("## Workspace\n")
	b.WriteString("/workspace starts empty.\n")
	if s.definitionURLAllowed(agent) {
		b.WriteString("The definition repository URL is in the environment variable DEFINITION_GIT_URL.\n")
	}
	fmt.Fprintf(&b, "Base ref: %s\n", s.productionRef())
	fmt.Fprintf(&b, "Push the branch %s.\n", branch)
	fmt.Fprintf(&b, "branch: %s\n", branch)
	b.WriteString("GH_TOKEN is available for HTTPS push.\n")
	return b.String()
}

func (s *Server) definitionURLAllowed(agent definition.Agent) bool {
	if strings.TrimSpace(s.cfg.Secrets["DEFINITION_GIT_URL"]) != "" {
		return true
	}
	if namedSecret(agent.Secrets, "DEFINITION_GIT_URL") {
		return true
	}
	tree := s.snapshot().tree
	return tree != nil && namedSecret(tree.Factory.Secrets, "DEFINITION_GIT_URL")
}

func namedSecret(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func (s *Server) improvementTreeText(ctx context.Context, batchID string) string {
	dir := s.snapshot().dir
	if strings.TrimSpace(s.cfg.DefinitionGitURL) != "" {
		if _, cloned, err := s.cloneRef(ctx, s.productionRef()); err == nil && cloned != "" {
			dir = cloned
		}
	}
	return "branch: " + improveBranchPrefix + batchID + "\n" + listDefinitionFiles(dir)
}

func (s *Server) priorRejections(ctx context.Context, batch store.ImprovementBatch) []string {
	batches, err := s.store.ImprovementBatches(ctx, batch.FactoryID)
	if err != nil {
		return nil
	}
	var notes []string
	for _, row := range batches {
		if row.Agent == batch.Agent && row.Status == api.BatchClosed && strings.TrimSpace(row.RejectionNote) != "" {
			notes = append(notes, row.RejectionNote)
		}
	}
	return notes
}

func (s *Server) openImprovementPR(ctx context.Context, batch store.ImprovementBatch, run store.Run) error {
	return s.withImproveLock(ctx, func() error {
		current, err := s.reloadBatch(ctx, batch.FactoryID, batch.ID)
		if err != nil {
			return err
		}
		if current.PRURL != "" || current.Status == api.BatchPROpen || current.Status == api.BatchFailed ||
			current.Status == api.BatchMerged || current.Status == api.BatchClosed {
			return nil
		}
		if !terminalStatus(run.Status) {
			return nil
		}
		// A failed improver used to leave the batch running. The in-flight
		// row then blocked every later batch for this agent. Mark it
		// failed once and do not clone.
		if run.Status != failure.StatusSucceeded {
			return s.failImprovementBatch(ctx, current.ID, improvementRunNote(run))
		}
		baseRef := s.productionRef()
		baseSHA, baseDir, err := s.cloneRef(ctx, baseRef)
		if err != nil {
			if missingRemoteRef(err) {
				return s.deferMissingRef(ctx, current.ID, "improvement base missing: "+baseRef)
			}
			return s.cappedImproveFailure(ctx, current.ID, "improvement clone: "+err.Error())
		}
		branch := improveBranchPrefix + current.ID
		headSHA, headDir, err := s.cloneRef(ctx, branch)
		if err != nil {
			if missingRemoteRef(err) {
				return s.deferMissingRef(ctx, current.ID, "improvement branch missing: "+branch)
			}
			return s.cappedImproveFailure(ctx, current.ID, "improvement clone: "+err.Error())
		}
		_ = baseSHA
		problems, warnings := guardFindings(baseDir, headDir)
		if len(problems) > 0 {
			return s.failImprovementBatch(ctx, current.ID, strings.Join(problems, "\n"))
		}
		client, ok := s.integ.githubApp.(*ghclient.Client)
		if !ok || client == nil {
			return errors.New("server: github app is not configured")
		}
		tok, repo, err := s.mintPRToken(ctx)
		if err != nil {
			return err
		}
		body := s.improvementPRBody(current, warnings, baseDir, headDir)
		number, prURL, err := client.CreatePullRequest(ctx, tok, repo, branch, s.productionRef(), "Improve "+current.Agent, body)
		if err != nil {
			var status *ghclient.StatusError
			if errors.As(err, &status) && status.StatusCode == http.StatusUnprocessableEntity {
				number, prURL, err = s.lookupOpenPR(ctx, tok, repo, branch)
				if err == nil {
					// The improver may have opened the request first. Replace
					// its body so the regressions section is still on the PR.
					// A rejected patch is logged and does not retry the batch.
					if patchErr := s.patchPullRequestBody(ctx, tok, repo, number, body); patchErr != nil {
						s.log.Error("improvement pr body", "batch", current.ID, "err", s.redact(patchErr.Error()))
					}
				}
			}
		}
		if err != nil {
			return s.cappedImproveFailure(ctx, current.ID, "improvement pr: "+err.Error())
		}
		s.clearImproveRetry(current.ID)
		if s.m6SkipSetBatchPR != nil && s.m6SkipSetBatchPR() {
			return nil
		}
		if err := s.store.SetBatchPR(ctx, current.ID, prURL, number, repo); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		s.requestImprovementReview(ctx, client, tok, repo, number, headSHA, current, warnings, headDir, baseDir)
		return nil
	})
}

func guardFindings(baseDir, headDir string) ([]string, []definition.Diagnostic) {
	baseTree, baseDiags := definition.LoadDir(baseDir)
	headTree, headDiags := definition.LoadDir(headDir)
	var problems []string
	for _, d := range append(append([]definition.Diagnostic{}, baseDiags...), headDiags...) {
		if d.Severity == definition.SeverityError {
			problems = append(problems, fmt.Sprintf("%s:%d: %s", d.File, d.Line, d.Message))
		}
	}
	var warnings []definition.Diagnostic
	for _, d := range definition.GuardRails(baseTree, headTree) {
		switch d.Severity {
		case definition.SeverityError:
			problems = append(problems, fmt.Sprintf("%s:%d: %s", d.File, d.Line, d.Message))
		case definition.SeverityWarning:
			warnings = append(warnings, d)
		}
	}
	return problems, warnings
}

func (s *Server) improvementPRBody(batch store.ImprovementBatch, warnings []definition.Diagnostic, baseDir, headDir string) string {
	body := improve.PRBody(improve.Batch{
		Agent:   batch.Agent,
		Layer:   improvementLayer(baseDir, headDir),
		Secrets: s.secretMaterial(),
	}, batchFailures(batch), s.improvementLinks())
	if len(warnings) > 0 {
		body += "\nPrivilege escalation: a scorer sets selfImprovement true.\n"
	}
	return body
}

func (s *Server) improvementLinks() improve.Links {
	base := strings.TrimRight(s.cfg.ExternalURL, "/")
	return improve.Links{
		Run: func(id string) string {
			if base == "" || id == "" {
				return ""
			}
			return base + "/api/v1/agent/runs/" + id
		},
		Result: func(id string) string {
			if base == "" || id == "" {
				return ""
			}
			return base + "/api/v1/scorer-results/" + id
		},
	}
}

func (s *Server) requestImprovementReview(ctx context.Context, client *ghclient.Client, tok ghclient.Token, repo string, number int, headSHA string, batch store.ImprovementBatch, warnings []definition.Diagnostic, headDir, baseDir string) {
	changed, err := changedDefinitionFiles(baseDir, headDir)
	if err != nil {
		s.log.Error("blast radius", "batch", batch.ID, "err", s.redact(err.Error()))
		changed = 0
	}
	cfg := definition.SelfImprovement{}
	if tree := s.snapshot().tree; tree != nil && tree.Factory.SelfImprovement != nil {
		cfg = *tree.Factory.SelfImprovement
	}
	rng := rand.New(rand.NewSource(s.now().UnixNano()))
	routing := improve.Route(cfg, improve.Pools{
		Admins:       append([]string{}, s.cfg.ReviewerAdmins...),
		Team:         append([]string{}, s.cfg.ReviewerTeam...),
		LoginByEmail: s.cfg.ReviewerLogins,
	}, changed, improve.DefaultBlastLimit, rng)
	labels := append([]string{}, routing.Labels...)
	if len(warnings) > 0 {
		labels = append(labels, privilegeEscalationLabel)
	}
	if routing.Reviewer != "" {
		logins := []string{routing.Reviewer}
		if len(logins) > 3 {
			logins = logins[:3]
		}
		if err := client.RequestReviewers(ctx, tok, repo, number, logins); err != nil {
			s.log.Error("reviewers", "batch", batch.ID, "err", s.redact(err.Error()))
		}
	}
	if len(labels) > 0 {
		if err := client.AddLabels(ctx, tok, repo, number, labels); err != nil {
			s.log.Error("labels", "batch", batch.ID, "err", s.redact(err.Error()))
		}
	}
	if headSHA == "" {
		s.log.Error("check run", "batch", batch.ID, "err", errors.New("head sha is empty"))
		return
	}
	if _, err := client.CreateCheckRun(ctx, tok, repo, headSHA, ghclient.CheckRun{
		Name:       factoryConfigCheck,
		Status:     ghclient.CheckCompleted,
		Conclusion: "success",
		Summary:    "factory/config passed",
	}); err != nil {
		s.log.Error("check run", "batch", batch.ID, "err", s.redact(err.Error()))
	}
}

func improvementRunNote(run store.Run) string {
	note := "improvement run " + run.Status
	reason := strings.TrimSpace(run.FailureReason)
	if reason == "" {
		reason = strings.TrimSpace(run.FailureCode)
	}
	if reason != "" {
		note += ": " + reason
	}
	return note
}

func (s *Server) failImprovementBatch(ctx context.Context, batchID, note string) error {
	note = strings.TrimSpace(s.redact(note))
	if len(note) > 4000 {
		note = note[:4000]
	}
	if err := s.store.SetBatchStatus(ctx, batchID, api.BatchFailed, note); err != nil {
		return err
	}
	s.clearImproveRetry(batchID)
	return nil
}

func missingRemoteRef(err error) bool {
	return err != nil && strings.Contains(err.Error(), "couldn't find remote ref")
}

func (s *Server) branchAdvertiseFor() time.Duration {
	if s.m6BranchAdvertiseFor > 0 {
		return s.m6BranchAdvertiseFor
	}
	return improveBranchAdvertiseFor
}

func (s *Server) cloneAttemptLimit() int {
	if s.m6CloneAttempts > 0 {
		return s.m6CloneAttempts
	}
	return improveCloneAttempts
}

// improveRetryState is per batch and process-local. A restart drops it.
// The next missing ref starts a new advertise window, which is enough:
// the branch is either visible by then or the new window fails the batch.
type improveRetryState struct {
	deadline time.Time
	attempts int
}

// deferMissingRef records the advertise deadline on the first miss and
// returns without sleeping. A later sweep fails the batch once after the
// deadline. Context cancel does not record a deadline or fail the batch.
func (s *Server) deferMissingRef(ctx context.Context, batchID, note string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.missingRefExpired(batchID) {
		return s.failImprovementBatch(ctx, batchID, note)
	}
	return nil
}

// cappedImproveFailure counts a non-missing clone error or a pull-request
// create error against improveCloneAttempts. Under the cap it returns nil
// so the sweep tries again. At the cap it fails the batch once.
func (s *Server) cappedImproveFailure(ctx context.Context, batchID, note string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.improveAttemptCapped(batchID) {
		return s.failImprovementBatch(ctx, batchID, note)
	}
	return nil
}

func (s *Server) missingRefExpired(batchID string) bool {
	now := s.now()
	window := s.branchAdvertiseFor()
	s.m6ImproveMu.Lock()
	defer s.m6ImproveMu.Unlock()
	if s.m6ImproveRetry == nil {
		s.m6ImproveRetry = map[string]improveRetryState{}
	}
	st := s.m6ImproveRetry[batchID]
	if st.deadline.IsZero() {
		st.deadline = now.Add(window)
		s.m6ImproveRetry[batchID] = st
		return false
	}
	return !now.Before(st.deadline)
}

func (s *Server) improveAttemptCapped(batchID string) bool {
	s.m6ImproveMu.Lock()
	defer s.m6ImproveMu.Unlock()
	if s.m6ImproveRetry == nil {
		s.m6ImproveRetry = map[string]improveRetryState{}
	}
	st := s.m6ImproveRetry[batchID]
	st.attempts++
	s.m6ImproveRetry[batchID] = st
	return st.attempts >= s.cloneAttemptLimit()
}

func (s *Server) improveRefPending() bool {
	s.m6ImproveMu.Lock()
	defer s.m6ImproveMu.Unlock()
	for _, st := range s.m6ImproveRetry {
		if !st.deadline.IsZero() {
			return true
		}
	}
	return false
}

func (s *Server) clearImproveRetry(batchID string) {
	s.m6ImproveMu.Lock()
	defer s.m6ImproveMu.Unlock()
	delete(s.m6ImproveRetry, batchID)
}

func (s *Server) githubAPI() string {
	base := strings.TrimRight(s.integ.githubBase, "/")
	if base == "" {
		return "https://api.github.com"
	}
	return base
}

func (s *Server) patchPullRequestBody(ctx context.Context, tok ghclient.Token, repo string, number int, body string) error {
	owner, name, ok := splitOwnerName(repo)
	if !ok {
		return errors.New("server: pull request body: repository is invalid")
	}
	if number <= 0 {
		return errors.New("server: pull request body: number is required")
	}
	raw, err := json.Marshal(struct {
		Body string `json:"body"`
	}{Body: body})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.githubAPI(), url.PathEscape(owner), url.PathEscape(name), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	client := s.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("server: pull request body: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("server: pull request body: HTTP %d", res.StatusCode)
	}
	return nil
}

func (s *Server) lookupOpenPR(ctx context.Context, tok ghclient.Token, repo, branch string) (int, string, error) {
	owner, name, ok := splitOwnerName(repo)
	if !ok {
		return 0, "", errors.New("server: pull request lookup: repository is invalid")
	}
	endpoint := s.githubAPI() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls?state=open&head=" + url.QueryEscape(owner+":"+branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	client := s.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("server: pull request lookup: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, "", fmt.Errorf("server: pull request lookup: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("server: pull request lookup: HTTP %d", res.StatusCode)
	}
	var rows []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return 0, "", fmt.Errorf("server: pull request lookup: %w", err)
	}
	if len(rows) == 0 || rows[0].Number <= 0 || rows[0].HTMLURL == "" {
		return 0, "", errors.New("server: pull request lookup: no open pull request")
	}
	return rows[0].Number, rows[0].HTMLURL, nil
}

func splitOwnerName(repo string) (string, string, bool) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

func (s *Server) withImproveLock(ctx context.Context, fn func() error) error {
	if s.db == nil {
		return fn()
	}
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("server: improve lock: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1)::bigint)`, improveSettleLockKey); err != nil {
		return fmt.Errorf("server: improve lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1)::bigint)`, improveSettleLockKey)
	}()
	return fn()
}
