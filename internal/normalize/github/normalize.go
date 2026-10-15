package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const codeForgeGitHub = "GITHUB"

// Normalizer maps GitHub deliveries. AppSlug derives *_mentioned events
// from @mentions in a new body. The zero value emits no mention events.
type Normalizer struct {
	AppSlug string
}

type mapped struct {
	ev          Event
	ignore      bool
	mentionText string
	mentionName string
}

// Normalize maps d with an empty app slug, so it never emits *_mentioned.
func Normalize(d Delivery) ([]Event, error) {
	return Normalizer{}.Normalize(d)
}

// Normalize maps one verified delivery. Unknown events return nil, nil.
func (n Normalizer) Normalize(d Delivery) ([]Event, error) {
	ghEvent := header(d.Headers, "X-GitHub-Event")
	if ghEvent == "" {
		return nil, errors.New("github: missing X-GitHub-Event")
	}
	if len(bytes.TrimSpace(d.Body)) == 0 {
		return nil, errors.New("github: empty body")
	}
	raw, err := compactRaw(d.Body)
	if err != nil {
		return nil, err
	}
	var body payload
	if err := json.Unmarshal(d.Body, &body); err != nil {
		return nil, fmt.Errorf("github: parse body: %w", err)
	}
	got, err := mapPayload(ghEvent, &body)
	if err != nil || got.ignore {
		return nil, err
	}
	got.ev.Provider = "github"
	got.ev.DeliveryID = d.DeliveryID
	got.ev.Raw = raw
	if got.ev.OccurredAt.IsZero() && !d.ReceivedAt.IsZero() {
		got.ev.OccurredAt = d.ReceivedAt.UTC()
	}
	out := []Event{got.ev}
	if got.mentionName != "" && n.mentions(got.mentionText) {
		extra := got.ev
		extra.Event = got.mentionName
		out = append(out, extra)
	}
	return out, nil
}

func mapPayload(ghEvent string, p *payload) (mapped, error) {
	switch ghEvent {
	case "issues":
		return mapIssues(p)
	case "issue_comment":
		return mapIssueComment(p)
	case "pull_request":
		return mapPullRequest(p)
	case "pull_request_review":
		return mapReview(p)
	case "push":
		return mapPush(p)
	case "check_run":
		return mapCheckRun(p)
	case "check_suite":
		return mapCheckSuite(p)
	case "workflow_run":
		return mapWorkflowRun(p)
	default:
		if strings.HasPrefix(ghEvent, "installation") {
			return mapInstallation(p)
		}
		return mapped{ignore: true}, nil
	}
}

func mapIssues(p *payload) (mapped, error) {
	var name string
	switch p.Action {
	case "opened":
		name = "issue_created"
	case "assigned":
		name = "issue_assigned"
	case "labeled":
		name = "issue_labeled"
	default:
		return mapped{ignore: true}, nil
	}
	if p.Issue == nil {
		return mapped{}, errors.New("github: issues delivery has no issue")
	}
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	ev := issueEvent(name, p.Issue, repo, p.Sender)
	out := mapped{ev: ev}
	if p.Action == "opened" {
		out.mentionText = p.Issue.Body
		out.mentionName = "issue_mentioned"
	}
	return out, nil
}

func mapIssueComment(p *payload) (mapped, error) {
	if p.Action != "created" {
		return mapped{ignore: true}, nil
	}
	if p.Issue == nil || p.Comment == nil {
		return mapped{}, errors.New("github: issue_comment delivery has no issue or comment")
	}
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	name := "issue_commented"
	mention := "issue_mentioned"
	if p.Issue.PullRequest != nil {
		name = "pull_request_commented"
		mention = "pull_request_mentioned"
	}
	ev := issueEvent(name, p.Issue, repo, p.Sender)
	ev.Subject.Kind = "issue_comment"
	ev.Subject.ID = idStr(p.Comment.ID)
	ev.Subject.URL = p.Comment.HTMLURL
	ev.Subject.Text = p.Comment.Body
	ev.OccurredAt = parseTime(p.Comment.CreatedAt)
	return mapped{ev: ev, mentionText: p.Comment.Body, mentionName: mention}, nil
}

func mapPullRequest(p *payload) (mapped, error) {
	name, ok := pullEventName(p.Action, p.PullRequest)
	if !ok {
		return mapped{ignore: true}, nil
	}
	if p.PullRequest == nil {
		return mapped{}, errors.New("github: pull_request delivery has no pull_request")
	}
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	pr := p.PullRequest
	ev := Event{
		Event:      name,
		OccurredAt: parseTime(pr.UpdatedAt),
		Actor:      actorFrom(p.Sender),
		Subject: Subject{
			Kind:       "pull_request",
			ID:         idStr(pr.ID),
			Number:     pr.Number,
			Title:      pr.Title,
			URL:        pr.HTMLURL,
			Repo:       repo,
			Labels:     labelNames(pr.Labels),
			Branch:     strPtr(pr.Head.Ref),
			BaseBranch: strPtr(pr.Base.Ref),
			Text:       pr.Body,
		},
		Thread: issueThread(repo, pr.Number),
	}
	out := mapped{ev: ev}
	if p.Action == "opened" {
		out.mentionText = pr.Body
		out.mentionName = "pull_request_mentioned"
	}
	return out, nil
}

func pullEventName(action string, pr *ghPullRequest) (string, bool) {
	switch action {
	case "opened":
		return "pull_request_opened", true
	case "closed":
		if pr != nil && pr.Merged {
			return "pull_request_merged", true
		}
		return "pull_request_closed", true
	case "reopened":
		return "pull_request_reopened", true
	case "ready_for_review":
		return "pull_request_ready", true
	case "labeled":
		return "pull_request_labeled", true
	case "assigned":
		return "pull_request_assigned", true
	case "synchronize":
		return "pull_request_synchronized", true
	case "review_requested":
		return "pull_request_review_requested", true
	case "enqueued":
		return "pull_request_enqueued", true
	case "dequeued":
		return "pull_request_dequeued", true
	default:
		return "", false
	}
}

func mapReview(p *payload) (mapped, error) {
	if p.Action != "submitted" {
		return mapped{ignore: true}, nil
	}
	if p.Review == nil || p.PullRequest == nil {
		return mapped{}, errors.New("github: pull_request_review delivery is missing review or pull_request")
	}
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	pr := p.PullRequest
	ev := Event{
		Event:      "pull_request_review_submitted",
		OccurredAt: parseTime(p.Review.SubmittedAt),
		Actor:      actorFrom(p.Sender),
		Subject: Subject{
			Kind:       "pull_request_review",
			ID:         idStr(p.Review.ID),
			Number:     pr.Number,
			Title:      pr.Title,
			URL:        p.Review.HTMLURL,
			Repo:       repo,
			Labels:     labelNames(pr.Labels),
			Branch:     strPtr(pr.Head.Ref),
			BaseBranch: strPtr(pr.Base.Ref),
			Text:       p.Review.Body,
		},
		Thread: issueThread(repo, pr.Number),
	}
	return mapped{ev: ev, mentionText: p.Review.Body, mentionName: "pull_request_mentioned"}, nil
}

func mapPush(p *payload) (mapped, error) {
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	occurred := time.Time{}
	text := ""
	url := ""
	if p.HeadCommit != nil {
		occurred = parseTime(p.HeadCommit.Timestamp)
		text = p.HeadCommit.Message
		url = p.HeadCommit.URL
	}
	ev := Event{
		Event:      "push",
		OccurredAt: occurred,
		Actor:      actorFrom(p.Sender),
		Subject: Subject{
			Kind:   "push",
			ID:     p.After,
			URL:    url,
			Repo:   repo,
			Branch: branchFromRef(p.Ref),
			Paths:  pushPaths(p.Commits),
			Text:   text,
		},
		Thread: Thread{
			Kind:        "github_push",
			ExternalRef: repo.Owner + "/" + repo.Name + "@" + p.Ref,
		},
	}
	return mapped{ev: ev}, nil
}

func mapCheckRun(p *payload) (mapped, error) {
	if p.Action != "rerequested" {
		return mapped{ignore: true}, nil
	}
	if p.CheckRun == nil {
		return mapped{}, errors.New("github: check_run delivery has no check_run")
	}
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	cr := p.CheckRun
	branch := ""
	base := ""
	if cr.CheckSuite != nil {
		branch = cr.CheckSuite.HeadBranch
	}
	if branch == "" && len(cr.PullRequests) == 1 {
		branch = cr.PullRequests[0].Head.Ref
	}
	if len(cr.PullRequests) == 1 {
		base = cr.PullRequests[0].Base.Ref
	}
	ev := Event{
		Event:      "check_run_rerequested",
		OccurredAt: parseTime(cr.StartedAt, cr.CompletedAt),
		Actor:      actorFrom(p.Sender),
		Subject: Subject{
			Kind:       "check_run",
			ID:         idStr(cr.ID),
			Title:      cr.Name,
			URL:        cr.HTMLURL,
			Repo:       repo,
			Branch:     strPtr(branch),
			BaseBranch: strPtr(base),
		},
		Thread: codeThread("github_check", repo, cr.PullRequests, cr.HeadSHA),
	}
	return mapped{ev: ev}, nil
}

func mapCheckSuite(p *payload) (mapped, error) {
	var name string
	switch p.Action {
	case "completed":
		name = "check_suite_completed"
	case "rerequested":
		name = "check_suite_rerequested"
	default:
		return mapped{ignore: true}, nil
	}
	if p.CheckSuite == nil {
		return mapped{}, errors.New("github: check_suite delivery has no check_suite")
	}
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	cs := p.CheckSuite
	ev := Event{
		Event:      name,
		OccurredAt: parseTime(cs.UpdatedAt),
		Actor:      actorFrom(p.Sender),
		Subject: Subject{
			Kind:   "check_suite",
			ID:     idStr(cs.ID),
			Repo:   repo,
			Branch: strPtr(cs.HeadBranch),
		},
		Thread: codeThread("github_check", repo, cs.PullRequests, cs.HeadSHA),
	}
	return mapped{ev: ev}, nil
}

func mapWorkflowRun(p *payload) (mapped, error) {
	if p.Action != "completed" {
		return mapped{ignore: true}, nil
	}
	if p.WorkflowRun == nil {
		return mapped{}, errors.New("github: workflow_run delivery has no workflow_run")
	}
	repo, err := repoOf(p.Repository)
	if err != nil {
		return mapped{}, err
	}
	run := p.WorkflowRun
	ev := Event{
		Event:      "workflow_run_completed",
		OccurredAt: parseTime(run.UpdatedAt),
		Actor:      actorFrom(p.Sender),
		Subject: Subject{
			Kind:   "workflow_run",
			ID:     idStr(run.ID),
			Title:  run.Name,
			URL:    run.HTMLURL,
			Repo:   repo,
			Branch: strPtr(run.HeadBranch),
		},
		Thread: codeThread("github_workflow", repo, run.PullRequests, run.HeadSHA),
	}
	return mapped{ev: ev}, nil
}

func mapInstallation(p *payload) (mapped, error) {
	if p.Installation == nil {
		return mapped{}, errors.New("github: installation delivery has no installation")
	}
	inst := p.Installation
	repo := soleRepo(p.Repositories, p.ReposAdded)
	ev := Event{
		Event:      "installation_changed",
		OccurredAt: parseTime(inst.UpdatedAt, inst.CreatedAt),
		Actor:      actorFrom(p.Sender),
		Subject: Subject{
			Kind:  "installation",
			ID:    idStr(inst.ID),
			Title: inst.Account.Login,
			Repo:  repo,
		},
		Thread: Thread{
			Kind:        "github_installation",
			ExternalRef: "installation:" + idStr(inst.ID),
		},
	}
	return mapped{ev: ev}, nil
}

func issueEvent(name string, issue *ghIssue, repo *Repo, sender *ghUser) Event {
	return Event{
		Event:      name,
		OccurredAt: parseTime(issue.UpdatedAt, issue.CreatedAt),
		Actor:      actorFrom(sender),
		Subject: Subject{
			Kind:   "issue",
			ID:     idStr(issue.ID),
			Number: issue.Number,
			Title:  issue.Title,
			URL:    issue.HTMLURL,
			Repo:   repo,
			Labels: labelNames(issue.Labels),
			Text:   issue.Body,
		},
		Thread: issueThread(repo, issue.Number),
	}
}

func issueThread(repo *Repo, number int) Thread {
	return Thread{
		Kind:        "github_issue",
		ExternalRef: fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Name, number),
	}
}

// codeThread binds a check or workflow to its pull request when there is
// exactly one. Several pull requests would make owner/repo#N ambiguous, so
// the ref stays on the head SHA.
func codeThread(kind string, repo *Repo, prs []ghPRLink, sha string) Thread {
	if len(prs) == 1 && prs[0].Number != 0 {
		return issueThread(repo, prs[0].Number)
	}
	return Thread{
		Kind:        kind,
		ExternalRef: repo.Owner + "/" + repo.Name + "@" + sha,
	}
}

func (n Normalizer) mentions(text string) bool {
	slug := strings.TrimSpace(n.AppSlug)
	if slug == "" || text == "" {
		return false
	}
	pat := `(?i)(?:^|[^A-Za-z0-9])@` + regexp.QuoteMeta(slug) + `(?:\[bot\])?(?:[^A-Za-z0-9-]|$)`
	return regexp.MustCompile(pat).MatchString(text)
}

func header(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func compactRaw(body []byte) (json.RawMessage, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return nil, fmt.Errorf("github: body is not valid JSON: %w", err)
	}
	return buf.Bytes(), nil
}

func repoOf(r *ghRepository) (*Repo, error) {
	if r == nil {
		return nil, errors.New("github: delivery has no repository")
	}
	if repo, ok := repoFromFull(r.Owner.Login + "/" + r.Name); ok {
		return repo, nil
	}
	if repo, ok := repoFromFull(r.FullName); ok {
		return repo, nil
	}
	return nil, errors.New("github: repository has no owner/name")
}

// soleRepo returns the repository only when the delivery names exactly one.
// An installation that covers several repositories leaves Subject.Repo empty;
// the full list stays in raw.
func soleRepo(lists ...[]ghRepository) *Repo {
	var found *Repo
	n := 0
	for _, list := range lists {
		for i := range list {
			repo, ok := repoFromFull(list[i].FullName)
			if !ok {
				continue
			}
			n++
			found = repo
		}
	}
	if n != 1 {
		return nil
	}
	return found
}

func repoFromFull(full string) (*Repo, bool) {
	owner, name, ok := strings.Cut(full, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, false
	}
	return &Repo{CodeForge: codeForgeGitHub, Owner: owner, Name: name}, true
}

func actorFrom(u *ghUser) Actor {
	if u == nil {
		return Actor{}
	}
	handle := u.Login
	if strings.EqualFold(u.Type, "Bot") && !strings.Contains(strings.ToLower(handle), "[bot]") {
		handle += "[bot]"
	}
	return Actor{ProviderID: idStr(u.ID), Handle: handle}
}

func labelNames(labels []ghLabel) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if label.Name == "" {
			continue
		}
		out = append(out, label.Name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pushPaths(commits []ghCommit) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(paths []string) {
		for _, path := range paths {
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	for i := range commits {
		add(commits[i].Added)
		add(commits[i].Removed)
		add(commits[i].Modified)
	}
	return out
}

func branchFromRef(ref string) *string {
	if rest, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return strPtr(rest)
	}
	return strPtr(ref)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func idStr(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func parseTime(values ...string) time.Time {
	for _, value := range values {
		if value == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return ts.UTC()
		}
	}
	return time.Time{}
}
