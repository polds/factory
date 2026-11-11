package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

const (
	publishEnvRepo   = "FACTORY_PUBLISH_DRAFT_REPO"
	publishRepo      = "northstar-ai-labs/dreaming"
	publishIssueKey  = "dreaming#454"
	publishRefPrefix = "factory/dreaming-454-"
	publishTitle     = "Factory: DB-backed tenant-isolation tests (#454)"
	publishSentinel  = "factoryEvidencePass"

	reasonRootCancelled = "root cancelled"
	reasonRootPaused    = "root paused"
	reasonRootOpen      = "root not completed"
	reasonRepo          = "repo"
	reasonSpend         = "spend"
	reasonEvidence      = "evidence missing"
	reasonRed           = "red unnamed"
	reasonGreen         = "green"
	reasonTruncated     = "truncated"
	reasonPorcelain     = "porcelain"
	reasonHead          = "head"
	reasonReady         = "ready sha"
	reasonSentinel      = "sentinel"
	reasonIssue         = "issue"
	reasonRef           = "existing ref"
	reasonPR            = "existing pr"
	reasonNotDraft      = "not draft"
	reasonHeadMismatch  = "head mismatch"
	reasonGitHub        = "github"
)

// publishWorktreeHook runs after the publisher checks out X and before it
// reads porcelain and HEAD. Production leaves it nil.
var publishWorktreeHook func(dir string)

type publishError struct {
	Reason string
	Detail string
}

func (e *publishError) Error() string {
	if e == nil {
		return "publish"
	}
	if e.Detail == "" {
		return "publish: " + e.Reason
	}
	return "publish: " + e.Reason + ": " + e.Detail
}

func refuse(reason string) error {
	return &publishError{Reason: reason}
}

type reviewPass struct {
	impl    store.ProducedHead
	pin     string
	red     string
	green   string
	x       string
	counted int64
}

func (s *Server) finishDraftPublication(ctx context.Context, factoryUID, taskID string, failed bool) {
	if failed {
		s.releaseProducedIfTerminal(ctx, factoryUID, taskID)
		return
	}
	if err := s.publishDraft(ctx, factoryUID, taskID); err != nil && s.log != nil {
		s.log.Info("draft publication", "err", scrubPublishText(err.Error(), ""))
	}
	s.releaseProducedIfTerminal(ctx, factoryUID, taskID)
}

func (s *Server) publishDraft(ctx context.Context, factoryUID, taskID string) error {
	repo := strings.TrimSpace(os.Getenv(publishEnvRepo))
	if repo == "" || s.store == nil {
		return nil
	}
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return err
	}
	task, err := s.store.GetNativeTask(ctx, fac.ID, taskID)
	if err != nil {
		return err
	}
	if task.ParentTaskID != "" {
		return nil
	}
	existing, err := s.store.GetDraftPublication(ctx, task.InvocationID)
	if err == nil {
		switch existing.State {
		case store.DraftOpened, store.DraftFailed:
			return nil
		case store.DraftPending, store.DraftPushed:
		default:
			return fmt.Errorf("publish: state %s", existing.State)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := s.rootAllows(ctx, task); err != nil {
		if existing.InvocationID != "" {
			return s.failDraft(ctx, existing, err)
		}
		return err
	}
	pass, err := s.reviewAllows(ctx, task.InvocationID, repo)
	if err != nil {
		if existing.InvocationID != "" {
			return s.failDraft(ctx, existing, err)
		}
		return err
	}
	work, cleanup, err := s.checkoutPublished(ctx, pass)
	if err != nil {
		if existing.InvocationID != "" {
			return s.failDraft(ctx, existing, err)
		}
		return err
	}
	defer cleanup()
	ref, err := publicationRef(task.InvocationID)
	if err != nil {
		return err
	}
	row := existing
	if row.InvocationID == "" {
		row, err = s.store.InsertDraftPending(ctx, store.DraftPublication{
			InvocationID: task.InvocationID,
			TargetRepo:   publishRepo,
			IssueKey:     publishIssueKey,
			HeadSHA:      pass.x,
			GitRef:       ref,
		})
		if errors.Is(err, store.ErrConflict) {
			return refuse(reasonIssue)
		}
		if err != nil {
			return err
		}
		if row.State == store.DraftOpened || row.State == store.DraftFailed {
			return nil
		}
	}
	if row.HeadSHA != pass.x || row.GitRef != ref {
		return s.failDraft(ctx, row, refuse(reasonReady))
	}
	return s.publishFromRow(ctx, task, row, pass, work)
}

func (s *Server) publishFromRow(ctx context.Context, task store.NativeTask, row store.DraftPublication, pass reviewPass, work draftGit) error {
	client, token, err := s.publicationClient(ctx)
	if err != nil {
		return s.failDraft(ctx, row, scrubbedPublish(err, github.Token{}))
	}
	remote, err := publishGitRemote(s.cfg.GitHubAPIBase, publishRepo)
	if err != nil {
		return s.failDraft(ctx, row, err)
	}
	git := work.withToken(token.Value(), remote)
	if row.State == store.DraftPending {
		pushed, err := s.pushDraft(ctx, task, row, pass, client, token, git)
		if err != nil {
			return err
		}
		row = pushed
	}
	if row.State != store.DraftPushed {
		return fmt.Errorf("publish: state %s", row.State)
	}
	if err := s.rootAllows(ctx, task); err != nil {
		return s.failDraft(ctx, row, err)
	}
	if err := s.openDraft(ctx, client, token, row, pass, git); err != nil {
		return s.failDraft(ctx, row, err)
	}
	s.deletePublishedObject(ctx, pass.impl)
	return nil
}

func (s *Server) pushDraft(ctx context.Context, task store.NativeTask, row store.DraftPublication, pass reviewPass, client *github.Client, token github.Token, git draftGit) (store.DraftPublication, error) {
	if err := s.rootAllows(ctx, task); err != nil {
		return store.DraftPublication{}, s.failDraft(ctx, row, err)
	}
	heads, err := git.listRemote(publishRefPrefix)
	if err != nil {
		return store.DraftPublication{}, s.failDraft(ctx, row, scrubbedPublish(err, token))
	}
	if len(heads.refs) > 0 {
		return store.DraftPublication{}, s.failDraft(ctx, row, refuse(reasonRef))
	}
	pulls, err := client.ListPullsByHeadPrefix(ctx, token, publishRepo, publishRefPrefix)
	if err != nil {
		return store.DraftPublication{}, s.failDraft(ctx, row, scrubbedPublish(err, token))
	}
	if len(pulls) > 0 {
		return store.DraftPublication{}, s.failDraft(ctx, row, refuse(reasonPR))
	}
	if err := s.rootAllows(ctx, task); err != nil {
		return store.DraftPublication{}, s.failDraft(ctx, row, err)
	}
	if err := git.pushCreate(pass.x, row.GitRef); err != nil {
		return store.DraftPublication{}, s.failDraft(ctx, row, scrubbedPublish(err, token))
	}
	advanced, err := s.store.AdvanceDraft(ctx, row.InvocationID, store.DraftPending, store.DraftPushed, store.DraftAdvance{
		GitRef: row.GitRef,
	})
	if err != nil {
		return store.DraftPublication{}, err
	}
	return advanced, nil
}

func (s *Server) openDraft(ctx context.Context, client *github.Client, token github.Token, row store.DraftPublication, pass reviewPass, git draftGit) error {
	heads, err := git.listRemote(publishRefPrefix)
	if err != nil {
		return scrubbedPublish(err, token)
	}
	pulls, err := client.ListPullsByHeadPrefix(ctx, token, publishRepo, publishRefPrefix)
	if err != nil {
		return scrubbedPublish(err, token)
	}
	var ours *github.DraftPull
	for _, pull := range pulls {
		if pull.HeadRef != row.GitRef {
			return refuse(reasonPR)
		}
		if ours != nil {
			return refuse(reasonPR)
		}
		matched := pull
		ours = &matched
	}
	if ours != nil {
		if !ours.Draft || !strings.EqualFold(ours.HeadSHA, pass.x) {
			return refuse(reasonHeadMismatch)
		}
		_, err := s.store.AdvanceDraft(ctx, row.InvocationID, store.DraftPushed, store.DraftOpened, store.DraftAdvance{
			GitRef: row.GitRef, PRNumber: ours.Number, PRURL: ours.URL, Draft: true,
		})
		return err
	}
	body := publicationBody(pass)
	created, err := client.CreateDraftPullRequest(ctx, token, publishRepo, row.GitRef, heads.base, publishTitle, body)
	if err != nil {
		return scrubbedPublish(err, token)
	}
	if !created.Draft {
		return refuse(reasonNotDraft)
	}
	if !strings.EqualFold(created.HeadSHA, pass.x) || created.HeadRef != row.GitRef {
		return refuse(reasonHeadMismatch)
	}
	confirmed, err := client.GetPullRequest(ctx, token, publishRepo, created.Number)
	if err != nil {
		return scrubbedPublish(err, token)
	}
	if !confirmed.Draft {
		return refuse(reasonNotDraft)
	}
	if !strings.EqualFold(confirmed.HeadSHA, pass.x) || confirmed.HeadRef != row.GitRef {
		return refuse(reasonHeadMismatch)
	}
	_, err = s.store.AdvanceDraft(ctx, row.InvocationID, store.DraftPushed, store.DraftOpened, store.DraftAdvance{
		GitRef: row.GitRef, PRNumber: confirmed.Number, PRURL: confirmed.URL, Draft: true,
	})
	return err
}

func (s *Server) publicationClient(ctx context.Context) (*github.Client, github.Token, error) {
	if s.cfg.GitHubAppID <= 0 || s.cfg.GitHubAppKey == nil {
		return nil, github.Token{}, refuse(reasonGitHub)
	}
	client, err := github.New(github.Config{
		AppID:   s.cfg.GitHubAppID,
		Key:     s.cfg.GitHubAppKey,
		BaseURL: s.cfg.GitHubAPIBase,
		HTTP:    s.cfg.HTTPClient,
		Now:     s.now,
		Log:     s.log,
	})
	if err != nil {
		return nil, github.Token{}, refuse(reasonGitHub)
	}
	installation, err := client.InstallationByRepo(ctx, publishRepo)
	if err != nil {
		return nil, github.Token{}, scrubbedPublish(err, github.Token{})
	}
	token, err := client.InstallationToken(ctx, installation, []string{publishRepo}, map[string]string{
		"contents":      "write",
		"pull_requests": "write",
		"metadata":      "read",
	})
	if err != nil {
		return nil, github.Token{}, scrubbedPublish(err, github.Token{})
	}
	return client, token, nil
}

func (s *Server) failDraft(ctx context.Context, row store.DraftPublication, cause error) error {
	if row.InvocationID == "" || row.State == store.DraftOpened || row.State == store.DraftFailed {
		return cause
	}
	detail := scrubPublishText(cause.Error(), "")
	if strings.Contains(detail, "ghs_") || strings.Contains(strings.ToLower(detail), "bearer ") {
		detail = "publish: redacted"
	}
	if len(detail) > 200 {
		detail = detail[:200]
	}
	from := row.State
	if from != store.DraftPending && from != store.DraftPushed {
		return cause
	}
	_, err := s.store.AdvanceDraft(ctx, row.InvocationID, from, store.DraftFailed, store.DraftAdvance{
		GitRef: row.GitRef, Detail: detail,
	})
	if err != nil && s.log != nil {
		s.log.Info("draft publication failed row", "err", "state")
	}
	if row.HeadSHA != "" {
		if head, err := s.implementerHead(ctx, row.InvocationID); err == nil {
			s.deletePublishedObject(ctx, head)
		}
	}
	return cause
}

func (s *Server) rootAllows(ctx context.Context, task store.NativeTask) error {
	fresh, err := s.store.GetNativeTask(ctx, task.FactoryID, task.ID)
	if err != nil {
		return err
	}
	switch fresh.Status {
	case store.NativeCancelled:
		return refuse(reasonRootCancelled)
	case store.NativePaused:
		return refuse(reasonRootPaused)
	case store.NativeCompleted:
	default:
		return refuse(reasonRootOpen)
	}
	gate, err := s.store.SubtreeGate(ctx, fresh.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch gate {
	case store.NativeCancelled:
		return refuse(reasonRootCancelled)
	case store.NativePaused:
		return refuse(reasonRootPaused)
	default:
		return refuse(reasonRootPaused)
	}
}

func (s *Server) reviewAllows(ctx context.Context, invocationID, repo string) (reviewPass, error) {
	if repo != publishRepo {
		return reviewPass{}, refuse(reasonRepo)
	}
	blocked, err := s.store.PublicationSpendBlocked(ctx, invocationID)
	if err != nil {
		return reviewPass{}, err
	}
	if blocked {
		return reviewPass{}, refuse(reasonSpend)
	}
	policy, err := s.store.GetNativePolicy(ctx, invocationID)
	if err != nil {
		return reviewPass{}, err
	}
	if !commitHex40(policy.SourceCommit) {
		return reviewPass{}, refuse(reasonReady)
	}
	impl, revRun, err := s.readyPair(ctx, invocationID, policy)
	if err != nil {
		return reviewPass{}, err
	}
	red, green, err := evidencePair(ctx, s.store, revRun)
	if err != nil {
		return reviewPass{}, err
	}
	spent, err := s.store.RollupSpent(ctx, invocationID)
	if err != nil {
		return reviewPass{}, err
	}
	var counted int64
	for _, cents := range spent {
		counted += cents
	}
	return reviewPass{
		impl: impl, pin: policy.SourceCommit, red: red, green: green, x: impl.HeadSHA, counted: counted,
	}, nil
}

func (s *Server) readyPair(ctx context.Context, invocationID string, policy store.NativePolicy) (store.ProducedHead, string, error) {
	implName, revName, ok := seatNames(policy)
	if !ok {
		return store.ProducedHead{}, "", refuse(reasonReady)
	}
	impl, err := s.producedReady(ctx, invocationID, implName)
	if err != nil {
		return store.ProducedHead{}, "", err
	}
	revHead, err := s.producedReady(ctx, invocationID, revName)
	if err != nil {
		return store.ProducedHead{}, "", err
	}
	if !strings.EqualFold(impl.HeadSHA, revHead.HeadSHA) || !commitHex40(impl.HeadSHA) {
		return store.ProducedHead{}, "", refuse(reasonReady)
	}
	revTask, err := s.store.TaskByAssignee(ctx, invocationID, revName)
	if err != nil {
		return store.ProducedHead{}, "", refuse(reasonEvidence)
	}
	revRun, err := s.store.TaskRun(ctx, revTask)
	if err != nil {
		return store.ProducedHead{}, "", refuse(reasonEvidence)
	}
	return impl, revRun, nil
}

func (s *Server) producedReady(ctx context.Context, invocationID, assignee string) (store.ProducedHead, error) {
	taskID, err := s.store.TaskByAssignee(ctx, invocationID, assignee)
	if err != nil {
		return store.ProducedHead{}, refuse(reasonReady)
	}
	row, err := s.store.GetProducedHead(ctx, taskID)
	if err != nil || row.State != store.ProducedReady {
		return store.ProducedHead{}, refuse(reasonReady)
	}
	return row, nil
}

func (s *Server) implementerHead(ctx context.Context, invocationID string) (store.ProducedHead, error) {
	policy, err := s.store.GetNativePolicy(ctx, invocationID)
	if err != nil {
		return store.ProducedHead{}, err
	}
	implName, _, ok := seatNames(policy)
	if !ok {
		return store.ProducedHead{}, refuse(reasonReady)
	}
	return s.producedReady(ctx, invocationID, implName)
}

func evidencePair(ctx context.Context, st *store.Store, runID string) (string, string, error) {
	var redText, greenText, greenStatus string
	var sawRed, sawGreen bool
	var seq int64
	for {
		batch, err := st.EventsFrom(ctx, runID, seq, 100)
		if err != nil {
			return "", "", err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			seq = row.Seq
			var payload struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Text   string `json:"text"`
			}
			if json.Unmarshal(row.Payload, &payload) != nil || payload.Type != harness.EventEvidenceTest {
				continue
			}
			if strings.Contains(payload.Text, harness.EvidenceTruncated) {
				return "", "", refuse(reasonTruncated)
			}
			if payload.Status == harness.EvidenceToolError {
				return "", "", refuse(reasonSentinel)
			}
			switch payload.Status {
			case harness.EvidenceTestsFailed:
				if !sawRed {
					redText = payload.Text
					sawRed = true
				}
			case harness.EvidenceTestsPassed, harness.EvidenceSkipOnly:
				greenText = payload.Text
				greenStatus = payload.Status
				sawGreen = true
			default:
				return "", "", refuse(reasonEvidence)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	if !sawRed || !sawGreen {
		return "", "", refuse(reasonEvidence)
	}
	if !namedTestAction(redText, "fail") {
		return "", "", refuse(reasonRed)
	}
	if greenStatus == harness.EvidenceSkipOnly || !namedTestAction(greenText, "pass") {
		return "", "", refuse(reasonGreen)
	}
	return namedTest(redText, "fail"), namedTest(greenText, "pass"), nil
}

func namedTestAction(transcript, action string) bool {
	return namedTest(transcript, action) != ""
}

func namedTest(transcript, action string) string {
	for _, line := range strings.Split(transcript, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Action == action && ev.Test != "" {
			return ev.Test
		}
	}
	return ""
}

func (s *Server) checkoutPublished(ctx context.Context, pass reviewPass) (draftGit, func(), error) {
	client := s.objects()
	if client == nil {
		return draftGit{}, nil, refuse(reasonReady)
	}
	rc, err := client.Get(ctx, headKey(pass.impl.RunID, pass.impl.ExecutionID))
	if err != nil {
		return draftGit{}, nil, refuse(reasonReady)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, bundleCap+1))
	if err != nil || len(body) == 0 || len(body) > bundleCap {
		return draftGit{}, nil, refuse(reasonReady)
	}
	dir, err := os.MkdirTemp("", "factory-publish-")
	if err != nil {
		return draftGit{}, nil, err
	}
	cleanupDir := func() { _ = os.RemoveAll(dir) }
	bundleFile, err := os.CreateTemp("", "factory-publish-bundle-")
	if err != nil {
		cleanupDir()
		return draftGit{}, nil, err
	}
	bundlePath := bundleFile.Name()
	if _, err := bundleFile.Write(body); err != nil {
		_ = bundleFile.Close()
		_ = os.Remove(bundlePath)
		cleanupDir()
		return draftGit{}, nil, err
	}
	if err := bundleFile.Close(); err != nil {
		_ = os.Remove(bundlePath)
		cleanupDir()
		return draftGit{}, nil, err
	}
	cleanupDir = func() {
		_ = os.Remove(bundlePath)
		_ = os.RemoveAll(dir)
	}
	if err := harness.VerifyBundleTip(ctx, bundlePath, pass.x); err != nil {
		cleanupDir()
		return draftGit{}, nil, refuse(reasonReady)
	}
	g, gitCleanup, err := openDraftGit(ctx, dir)
	if err != nil {
		cleanupDir()
		return draftGit{}, nil, err
	}
	cleanup := func() {
		gitCleanup()
		cleanupDir()
	}
	if _, err := g.text("init"); err != nil {
		cleanup()
		return draftGit{}, nil, err
	}
	if _, err := g.stdout("bundle", "unbundle", bundlePath); err != nil {
		cleanup()
		return draftGit{}, nil, refuse(reasonReady)
	}
	if _, err := g.text("checkout", "--detach", pass.x); err != nil {
		cleanup()
		return draftGit{}, nil, refuse(reasonHead)
	}
	if publishWorktreeHook != nil {
		publishWorktreeHook(dir)
	}
	porcelain, err := g.text("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		cleanup()
		return draftGit{}, nil, err
	}
	if strings.TrimSpace(porcelain) != "" {
		cleanup()
		return draftGit{}, nil, refuse(reasonPorcelain)
	}
	head, err := g.text("rev-parse", "--verify", "HEAD")
	if err != nil || !strings.EqualFold(head, pass.x) {
		cleanup()
		return draftGit{}, nil, refuse(reasonHead)
	}
	mutation := strings.TrimSpace(os.Getenv(harness.EnvEvidenceMutation))
	if harness.EvidenceMutationPath(mutation) != nil || mutation == "" {
		cleanup()
		return draftGit{}, nil, refuse(reasonSentinel)
	}
	blob, err := g.stdout("cat-file", "blob", pass.x+":"+mutation)
	if err != nil || !strings.Contains(blob, publishSentinel) {
		cleanup()
		return draftGit{}, nil, refuse(reasonSentinel)
	}
	return g, cleanup, nil
}

func (s *Server) deletePublishedObject(ctx context.Context, head store.ProducedHead) {
	client := s.objects()
	if client == nil || head.RunID == "" || head.ExecutionID == "" {
		return
	}
	if err := client.Delete(ctx, headKey(head.RunID, head.ExecutionID)); err != nil && s.log != nil {
		s.log.Info("draft publication bundle", "err", "delete")
	}
}

func (s *Server) releaseProducedIfTerminal(ctx context.Context, factoryUID, taskID string) {
	if s.store == nil {
		return
	}
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return
	}
	task, err := s.store.GetNativeTask(ctx, fac.ID, taskID)
	if err != nil || task.ParentTaskID != "" {
		return
	}
	retain, err := s.retainProducedBundle(ctx, task.InvocationID)
	if err != nil || retain {
		return
	}
	head, err := s.implementerHead(ctx, task.InvocationID)
	if err != nil {
		return
	}
	s.deletePublishedObject(ctx, head)
}

func (s *Server) retainProducedBundle(ctx context.Context, invocationID string) (bool, error) {
	row, err := s.store.GetDraftPublication(ctx, invocationID)
	if err == nil {
		switch row.State {
		case store.DraftOpened, store.DraftFailed:
			return false, nil
		case store.DraftPending, store.DraftPushed:
			return true, nil
		default:
			return true, nil
		}
	}
	if !errors.Is(err, store.ErrNotFound) {
		return true, err
	}
	status, err := s.store.InvocationRootStatus(ctx, invocationID)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	switch status {
	case store.NativeCompleted, store.NativeFailed, store.NativeCancelled:
		return false, nil
	default:
		return true, nil
	}
}

func publicationRef(invocationID string) (string, error) {
	if len(invocationID) < 8 {
		return "", refuse(reasonReady)
	}
	suffix := invocationID[:8]
	for _, r := range suffix {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return "", refuse(reasonReady)
		}
	}
	return publishRefPrefix + suffix, nil
}

func publicationBody(pass reviewPass) string {
	return fmt.Sprintf(`Head %s
Pin %s
Oskar evidence: red %s failed; green %s passed.
Spend counted cents: %d
The test file carries the factoryEvidencePass sentinel used for mutation proof.
Refs #454
Draft by Factory; human review required; Factory will not merge.
`, pass.x, pass.pin, pass.red, pass.green, pass.counted)
}

func publishGitRemote(apiBase, repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", refuse(reasonRepo)
	}
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" || base == "https://api.github.com" {
		return "https://github.com/" + owner + "/" + name + ".git", nil
	}
	return base + "/" + owner + "/" + name + ".git", nil
}

func scrubbedPublish(err error, token github.Token) error {
	if err == nil {
		return nil
	}
	var pe *publishError
	if errors.As(err, &pe) {
		return err
	}
	text := scrubPublishText(err.Error(), token.Value())
	if strings.Contains(text, "ghs_") || strings.Contains(strings.ToLower(text), "bearer ") {
		text = "redacted"
	}
	if len(text) > 160 {
		text = text[:160]
	}
	return &publishError{Reason: reasonGitHub, Detail: text}
}

func scrubPublishText(text, token string) string {
	if token != "" {
		text = strings.ReplaceAll(text, token, "github-installation-token")
	}
	text = strings.ReplaceAll(text, "Authorization: Bearer ", "Authorization: ")
	return text
}
