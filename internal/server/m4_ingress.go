package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/ingress"
	ghnorm "github.com/polds/factory/internal/normalize/github"
	linnorm "github.com/polds/factory/internal/normalize/linear"
	slacknorm "github.com/polds/factory/internal/normalize/slack"
	"github.com/polds/factory/internal/outbound"
	"github.com/polds/factory/internal/store"
	"github.com/polds/factory/internal/webhooks"
)

var issueMentionRe = regexp.MustCompile(`(?:^|[\s(])#(\d+)\b`)

// ingressStore adapts *store.Store to ingress.Store.
// ClaimReceived returns ingress deliveries; the store row carries factory
// and retention columns the dispatcher does not read.
type ingressStore struct{ s *Server }

func (a ingressStore) ClaimReceived(ctx context.Context, limit int) ([]ingress.Delivery, error) {
	rows, err := a.s.store.ClaimReceived(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ingress.Delivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, ingress.Delivery{
			ID:         row.ID,
			Provider:   row.Provider,
			WebhookUID: row.WebhookUID,
			DeliveryID: row.DeliveryID,
			ReceivedAt: row.ReceivedAt,
			Headers:    row.Headers,
			Body:       row.Body,
			Truncated:  row.Truncated,
		})
	}
	return out, nil
}

func (a ingressStore) MarkDelivery(ctx context.Context, id, status string) error {
	return a.s.store.MarkDelivery(ctx, id, status)
}

// ingressSink stamps the current factory. ingress.Sink has no factory id.
type ingressSink struct{ s *Server }

func (k ingressSink) Enqueue(ctx context.Context, d ingress.Delivery) (bool, error) {
	fac := k.s.snapshot().factory
	if fac.ID == "" {
		return false, errors.New("server: factory is not applied")
	}
	return enqueueDelivery(ctx, k.s, fac.ID, d)
}

// webhookSink uses the registration's factory id.
type webhookSink struct{ s *Server }

func (k webhookSink) Enqueue(ctx context.Context, factoryID string, d ingress.Delivery) (bool, error) {
	if factoryID == "" {
		return false, errors.New("server: factory is not applied")
	}
	return enqueueDelivery(ctx, k.s, factoryID, d)
}

func enqueueDelivery(ctx context.Context, s *Server, factoryID string, d ingress.Delivery) (bool, error) {
	received := d.ReceivedAt
	if received.IsZero() {
		received = s.now()
	}
	row := store.Delivery{
		ID:         d.ID,
		FactoryID:  factoryID,
		Provider:   d.Provider,
		WebhookUID: d.WebhookUID,
		DeliveryID: d.DeliveryID,
		ReceivedAt: received,
		Headers:    d.Headers,
		Body:       d.Body,
		Truncated:  d.Truncated,
		ExpiresAt:  received.Add(s.cfg.DeliveryRetention),
	}
	_, dup, err := s.store.InsertDelivery(ctx, row)
	if err != nil {
		return false, err
	}
	if !dup {
		s.pokeDispatch()
	}
	return dup, nil
}

var (
	_ ingress.Sink  = ingressSink{}
	_ webhooks.Sink = webhookSink{}
)

func (s *Server) handleDelivery(ctx context.Context, d ingress.Delivery) (bool, error) {
	evs, err := normalizeDelivery(d)
	if err != nil {
		return false, err
	}
	raw, err := normalizedEventJSON(evs)
	if err != nil {
		return false, err
	}
	if err := s.store.SetDeliveryNormalized(ctx, d.ID, raw); err != nil {
		return false, err
	}
	if len(evs) == 0 {
		return false, ingress.ErrIgnored
	}
	matched := false
	seen := map[string]struct{}{}
	var autos []string
	for _, ev := range evs {
		s.noteDefinitionPush(ev)
		if s.integ.engine == nil {
			return false, errors.New("server: automation engine is not configured")
		}
		s.integ.mu.Lock()
		res, handleErr := s.integ.engine.Handle(ctx, ev)
		s.integ.mu.Unlock()
		if handleErr != nil {
			return false, handleErr
		}
		if res.Matched {
			matched = true
		}
		s.recordOutcomes(ctx, res)
		s.notePaused(ctx, res)
		s.linkPullRequest(ctx, ev)
		s.notePRFact(ctx, ev)
		for _, outcome := range res.Outcomes {
			if outcome.Automation == "" {
				continue
			}
			if _, ok := seen[outcome.Automation]; ok {
				continue
			}
			seen[outcome.Automation] = struct{}{}
			autos = append(autos, outcome.Automation)
		}
	}
	if len(autos) > 0 {
		if _, err := s.store.SetMatchedAutomations(ctx, d.ID, autos); err != nil {
			s.log.Error("matched automations", "delivery_id", d.ID, "err", err)
		}
	}
	return matched, nil
}

func normalizeDelivery(d ingress.Delivery) ([]event.Event, error) {
	switch d.Provider {
	case ingress.ProviderGitHub:
		evs, err := ghnorm.Normalize(ghnorm.Delivery{
			ID: d.ID, Provider: d.Provider, WebhookUID: d.WebhookUID,
			DeliveryID: d.DeliveryID, ReceivedAt: d.ReceivedAt,
			Headers: d.Headers, Body: d.Body, Truncated: d.Truncated,
		})
		if err != nil || len(evs) == 0 {
			return nil, err
		}
		return convertEvents(evs)
	case ingress.ProviderSlack:
		evs, err := slacknorm.New("", "").Normalize(slacknorm.Delivery{
			ID: d.ID, Provider: d.Provider, WebhookUID: d.WebhookUID,
			DeliveryID: d.DeliveryID, ReceivedAt: d.ReceivedAt,
			Headers: d.Headers, Body: d.Body, Truncated: d.Truncated,
		})
		if err != nil || len(evs) == 0 {
			return nil, err
		}
		return convertEvents(evs)
	case ingress.ProviderLinear:
		evs, err := linnorm.Normalize(linnorm.Delivery{
			ID: d.ID, Provider: d.Provider, WebhookUID: d.WebhookUID,
			DeliveryID: d.DeliveryID, ReceivedAt: d.ReceivedAt,
			Headers: d.Headers, Body: d.Body, Truncated: d.Truncated,
		})
		if err != nil || len(evs) == 0 {
			return nil, err
		}
		return convertEvents(evs)
	case ingress.ProviderWebhook:
		return webhooks.Normalize(d)
	default:
		return nil, fmt.Errorf("server: unknown provider %q", d.Provider)
	}
}

// normalizedEventJSON is the event list with the raw key removed.
// An empty input is [] so a delivery that produced no events is distinct
// from NULL (never dispatched).
func normalizedEventJSON(evs []event.Event) (json.RawMessage, error) {
	if evs == nil {
		evs = []event.Event{}
	}
	raw, err := json.Marshal(evs)
	if err != nil {
		return nil, err
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []map[string]json.RawMessage{}
	}
	for i := range rows {
		delete(rows[i], "raw")
	}
	out, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func convertEvents(src any) ([]event.Event, error) {
	raw, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	var out []event.Event
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) noteDefinitionPush(ev event.Event) {
	if ev.Provider != event.ProviderGitHub || ev.Event != "push" {
		return
	}
	if strings.TrimSpace(s.cfg.DefinitionGitURL) == "" {
		return
	}
	wantOwner, wantName, ok := gitRepo(s.cfg.DefinitionGitURL)
	if !ok || ev.Subject.Repo.Owner == "" {
		return
	}
	if !strings.EqualFold(ev.Subject.Repo.Owner, wantOwner) || !strings.EqualFold(ev.Subject.Repo.Name, wantName) {
		return
	}
	branch := ""
	if ev.Subject.Branch != nil {
		branch = *ev.Subject.Branch
	}
	ref := s.cfg.DefinitionGitRef
	if ref == "" {
		ref = "main"
	}
	if branch != ref && (s.cfg.DefinitionGitProductionRef == "" || branch != s.cfg.DefinitionGitProductionRef) {
		return
	}
	s.pokeDef()
}

func gitRepo(raw string) (owner, name string, ok bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	if raw == "" {
		return "", "", false
	}
	path := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", false
		}
		path = u.Path
	} else if i := strings.LastIndex(raw, ":"); i >= 0 {
		path = raw[i+1:]
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-1] == "" || parts[len(parts)-2] == "" {
		return "", "", false
	}
	return parts[len(parts)-2], parts[len(parts)-1], true
}

func (s *Server) linkPullRequest(ctx context.Context, ev event.Event) {
	if ev.Provider != event.ProviderGitHub {
		return
	}
	if ev.Event != "pull_request_opened" && ev.Event != "pull_request_reopened" {
		return
	}
	prURL := strings.TrimSpace(ev.Subject.URL)
	if prURL == "" {
		return
	}
	itemID := s.workItemForPullRequest(ctx, ev)
	if itemID == "" {
		return
	}
	s.recordPullURL(ctx, itemID, prURL, pullHeadSHA(ev.Raw))
}

func (s *Server) workItemForPullRequest(ctx context.Context, ev event.Event) string {
	if ref, err := s.store.GetExternalRef(ctx, ev.Provider, ev.Thread.ExternalRef); err == nil {
		return ref.WorkItemID
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Error("pull request ref", "err", err)
		return ""
	}
	owner, name := ev.Subject.Repo.Owner, ev.Subject.Repo.Name
	if owner == "" || name == "" {
		return ""
	}
	for _, number := range issueMentions(ev.Subject.Text) {
		refName := fmt.Sprintf("%s/%s#%d", owner, name, number)
		ref, err := s.store.GetExternalRef(ctx, store.ProviderGitHub, refName)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			s.log.Error("pull request issue ref", "err", err)
			return ""
		}
		return ref.WorkItemID
	}
	return ""
}

func issueMentions(text string) []int {
	matches := issueMentionRe.FindAllStringSubmatch(text, -1)
	var out []int
	seen := map[int]struct{}{}
	for _, match := range matches {
		n, err := strconv.Atoi(match[1])
		if err != nil || n <= 0 {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func (s *Server) recordPullURL(ctx context.Context, workItemID, prURL, headSHA string) {
	if workItemID == "" || prURL == "" || s.store == nil {
		return
	}
	claimed, stored, err := s.store.ClaimOpenPullRequest(ctx, workItemID, prURL)
	if err != nil {
		s.log.Error("pull request", "work_item_id", workItemID, "err", err)
		return
	}
	if !claimed {
		if stored != prURL {
			s.log.Info("pull request already open", "work_item_id", workItemID, "open_pr_url", stored, "ignored", prURL)
		}
		return
	}
	item, err := s.store.GetWorkItem(ctx, workItemID)
	if err != nil {
		s.log.Error("pull request", "work_item_id", workItemID, "err", err)
		return
	}
	s.notify(ctx, outbound.KindPROpened, item, "", "", prURL, headSHA)
}

// resumingAnswerer adapts Server.AnswerInput to automation.ResumingAnswerer.
// It does not call store.AnswerInputRequest.
type resumingAnswerer struct{ s *Server }

func (a resumingAnswerer) Answer(ctx context.Context, workItemID, answer, answeredBy string) (automation.Input, error) {
	row, err := a.s.AnswerInput(ctx, workItemID, answer, answeredBy)
	if err != nil {
		return automation.Input{}, answerErr(err)
	}
	return automation.Input{
		ID:         row.ID,
		WorkItemID: row.WorkItemID,
		RunID:      row.RunID,
		Question:   row.Question,
		Answer:     row.Answer,
		AnsweredBy: row.AnsweredBy,
	}, nil
}

func answerErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, api.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return automation.ErrNotFound
	case errors.Is(err, api.ErrConflict), errors.Is(err, store.ErrConflict):
		return automation.ErrConflict
	default:
		return err
	}
}
