package server

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"text/template"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

const (
	defaultPublishPrefix = "factory/draft-"
	defaultPublishBody   = `Head {{.Head}}
Pin {{.Pin}}
Evidence: red {{.Red}} failed; green {{.Green}} passed.
Spend counted cents: {{.Counted}}
Sentinel: {{.Sentinel}}
Issue: {{.IssueKey}}
Repo: {{.Repo}}
Draft by Factory; human review required; Factory will not merge.
`
	maxPublishTitle = 256
	maxPublishBody  = 8192
)

var publishRefPattern = regexp.MustCompile(`^factory/[A-Za-z0-9][A-Za-z0-9._-]{0,48}-$`)

type publishPlan struct {
	enabled   bool
	repo      string
	issueKey  string
	refPrefix string
	title     string
	body      string
	sentinel  string
	mutation  string
	listed    map[string]struct{}
}

type publishBodyData struct {
	Head     string
	Pin      string
	Red      string
	Green    string
	Counted  int64
	Sentinel string
	IssueKey string
	Repo     string
}

func (s *Server) resolvePublish(ctx context.Context, factoryID, invocationID string) (publishPlan, error) {
	contract, err := s.factoryContract(ctx, factoryID)
	if err != nil {
		return publishPlan{}, err
	}
	if contract.Publish == nil || !contract.Publish.Enabled {
		return publishPlan{}, nil
	}
	var frozen store.FrozenRun
	if s.store != nil && invocationID != "" {
		frozen, err = s.store.FrozenRun(ctx, invocationID)
		if err != nil {
			return publishPlan{}, err
		}
	}
	sentinel := definition.DefaultEvidenceSentinel
	if contract.Evidence != nil && strings.TrimSpace(contract.Evidence.Sentinel) != "" {
		sentinel = contract.Evidence.Sentinel
	}
	mutation := ""
	if contract.Evidence != nil {
		mutation = contract.Evidence.Mutation
	}
	if strings.TrimSpace(frozen.Mutation) != "" {
		mutation = frozen.Mutation
	}
	if strings.TrimSpace(frozen.Sentinel) != "" {
		sentinel = frozen.Sentinel
	}
	body := defaultPublishBody
	if contract.Publish.Body != "" {
		body = contract.Publish.Body
	}
	if frozen.Body != "" {
		body = frozen.Body
	}
	title := firstText(frozen.Title, contract.Publish.Title, firstText(frozen.IssueKey, contract.Publish.IssueKey))
	plan := publishPlan{
		enabled:   true,
		repo:      repoSlug(contract.Publish.Repo),
		issueKey:  firstText(frozen.IssueKey, contract.Publish.IssueKey),
		refPrefix: firstText(frozen.RefPrefix, contract.Publish.RefPrefix, defaultPublishPrefix),
		title:     title,
		body:      body,
		sentinel:  sentinel,
		mutation:  mutation,
		listed:    contract.repoNames(),
	}
	return plan, nil
}

func (p publishPlan) closed() error {
	if !p.enabled {
		return nil
	}
	if p.repo == "" || !p.repoListed() {
		return refuse(reasonRepo)
	}
	if strings.TrimSpace(p.issueKey) == "" {
		return refuse(reasonIssue)
	}
	if !publishRefPattern.MatchString(p.refPrefix) {
		return refuse(reasonRef)
	}
	if len(p.title) > maxPublishTitle || len(p.body) > maxPublishBody {
		return refuse(reasonGitHub)
	}
	if _, err := renderPublishBody(p, reviewPass{counted: 0}); err != nil {
		return refuse(reasonGitHub)
	}
	return nil
}

func (p publishPlan) repoListed() bool {
	if p.repo == "" || len(p.listed) == 0 {
		return false
	}
	_, ok := p.listed[p.repo]
	return ok
}

func renderPublishBody(plan publishPlan, pass reviewPass) (string, error) {
	tpl, err := template.New("publish").Option("missingkey=error").Parse(plan.body)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	data := publishBodyData{
		Head: pass.x, Pin: pass.pin, Red: pass.red, Green: pass.green,
		Counted: pass.counted, Sentinel: plan.sentinel, IssueKey: plan.issueKey, Repo: plan.repo,
	}
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func publicationRef(prefix, invocationID string) (string, error) {
	if !publishRefPattern.MatchString(prefix) {
		return "", refuse(reasonRef)
	}
	if len(invocationID) < 8 {
		return "", refuse(reasonReady)
	}
	suffix := invocationID[:8]
	for _, r := range suffix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return "", refuse(reasonReady)
		}
	}
	return prefix + suffix, nil
}
