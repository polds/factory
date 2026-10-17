package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/cron"
	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/store"
)

// apiFacade gives the REST handler an AnswerInput that returns api.InputAnswer.
// Server.AnswerInput returns a store row and resumes the parked run; the
// engine adapter maps that same method onto automation sentinels.
type apiFacade struct{ *Server }

func (f apiFacade) AnswerInput(ctx context.Context, workItemID, text, answeredBy string) (api.InputAnswer, error) {
	row, err := f.Server.AnswerInput(ctx, workItemID, text, answeredBy)
	if err != nil {
		return api.InputAnswer{}, err
	}
	return api.InputAnswer{
		ID:         row.ID,
		WorkItemID: row.WorkItemID,
		Answer:     row.Answer,
		AnsweredBy: row.AnsweredBy,
	}, nil
}

func (s *Server) MountIngress(provider string) (http.Handler, bool) {
	switch provider {
	case ingress.ProviderGitHub:
		return s.integ.ingressGitHub, s.integ.ingressGitHub != nil
	case ingress.ProviderSlack:
		return s.integ.ingressSlack, s.integ.ingressSlack != nil
	case ingress.ProviderLinear:
		return s.integ.ingressLinear, s.integ.ingressLinear != nil
	default:
		return nil, false
	}
}

func (s *Server) MountWebhook() (http.Handler, bool) {
	return s.integ.webhooks, s.integ.webhooks != nil
}

func (s *Server) ListDeliveries(ctx context.Context, factoryID, status string, limit int) ([]api.Delivery, error) {
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListDeliveries(ctx, fac.ID, status, limit)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	out := make([]api.Delivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIDelivery(row))
	}
	return out, nil
}

func (s *Server) ListWebhookDeliveries(ctx context.Context, factoryID, webhookUID, status string, limit int) ([]api.Delivery, error) {
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return nil, err
	}
	reg, err := s.store.GetWebhookRegistration(ctx, webhookUID)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	if reg.FactoryID != fac.ID {
		return nil, api.ErrNotFound
	}
	rows, err := s.store.ListWebhookDeliveries(ctx, fac.ID, webhookUID, status, limit)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	out := make([]api.Delivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIDelivery(row))
	}
	return out, nil
}

func (s *Server) ReplayDelivery(ctx context.Context, id string) (api.Delivery, error) {
	if _, err := s.store.GetDelivery(ctx, id); err != nil {
		return api.Delivery{}, apiStoreErr(err)
	}
	if err := s.store.MarkDelivery(ctx, id, store.DeliveryReceived); err != nil {
		return api.Delivery{}, apiStoreErr(err)
	}
	s.pokeDispatch()
	row, err := s.store.GetDelivery(ctx, id)
	if err != nil {
		return api.Delivery{}, apiStoreErr(err)
	}
	return toAPIDelivery(row), nil
}

func (s *Server) ListSchedules(ctx context.Context, factoryID string) ([]api.Schedule, error) {
	id, err := s.scheduleFactory(ctx, factoryID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListSchedules(ctx, id)
	if err != nil {
		return nil, apiStoreErr(err)
	}
	return toAPISchedules(rows), nil
}

func (s *Server) GetSchedule(ctx context.Context, id string) (api.Schedule, error) {
	row, err := s.store.GetSchedule(ctx, id)
	if err != nil {
		return api.Schedule{}, apiStoreErr(err)
	}
	return toAPISchedule(row), nil
}

func (s *Server) CreateSchedule(ctx context.Context, req api.ScheduleRequest) (api.Schedule, error) {
	factoryID, err := s.scheduleFactory(ctx, req.FactoryID)
	if err != nil {
		return api.Schedule{}, err
	}
	next, err := s.nextFire(req.Cron)
	if err != nil {
		return api.Schedule{}, err
	}
	row, err := s.store.UpsertSchedule(ctx, store.Schedule{
		FactoryID:  factoryID,
		Automation: req.Automation,
		Name:       req.Name,
		Cron:       req.Cron,
		NextFireAt: next,
		Paused:     req.Paused,
	})
	if err != nil {
		return api.Schedule{}, apiStoreErr(err)
	}
	return toAPISchedule(row), nil
}

func (s *Server) UpdateSchedule(ctx context.Context, id string, patch api.SchedulePatch) (api.Schedule, error) {
	cur, err := s.store.GetSchedule(ctx, id)
	if err != nil {
		return api.Schedule{}, apiStoreErr(err)
	}
	next := cur
	if patch.Automation != nil {
		next.Automation = strings.TrimSpace(*patch.Automation)
	}
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.Cron != nil {
		next.Cron = strings.TrimSpace(*patch.Cron)
	}
	if patch.Paused != nil {
		next.Paused = *patch.Paused
	}
	if next.Cron != cur.Cron || next.NextFireAt.IsZero() {
		fire, err := s.nextFire(next.Cron)
		if err != nil {
			return api.Schedule{}, err
		}
		next.NextFireAt = fire
	}
	if next.Automation != cur.Automation {
		if err := s.store.DeleteSchedule(ctx, cur.FactoryID, cur.Automation, cur.Name); err != nil {
			return api.Schedule{}, apiStoreErr(err)
		}
		next.ID = ""
		saved, err := s.store.UpsertSchedule(ctx, next)
		if err != nil {
			return api.Schedule{}, apiStoreErr(err)
		}
		return toAPISchedule(saved), nil
	}
	if next.Name != cur.Name {
		saved, err := s.store.ReplaceSchedule(ctx, cur.Name, next)
		if err != nil {
			return api.Schedule{}, apiStoreErr(err)
		}
		return toAPISchedule(saved), nil
	}
	saved, err := s.store.UpsertSchedule(ctx, next)
	if err != nil {
		return api.Schedule{}, apiStoreErr(err)
	}
	return toAPISchedule(saved), nil
}

func (s *Server) DeleteSchedule(ctx context.Context, id string) error {
	return apiStoreErr(s.store.DeleteScheduleByID(ctx, id))
}

func (s *Server) SetSchedulePaused(ctx context.Context, id string, paused bool) (api.Schedule, error) {
	if err := s.store.SetSchedulePaused(ctx, id, paused); err != nil {
		return api.Schedule{}, apiStoreErr(err)
	}
	return s.GetSchedule(ctx, id)
}

func (s *Server) RunByExternalReference(ctx context.Context, provider, ref string) (api.ExternalRun, error) {
	row, err := s.store.GetExternalRef(ctx, provider, ref)
	if err != nil {
		return api.ExternalRun{}, apiStoreErr(err)
	}
	out := api.ExternalRun{
		Provider:       row.Provider,
		ExternalRef:    row.ExternalRef,
		WorkItemID:     row.WorkItemID,
		ConversationID: row.ConversationID,
	}
	runID, err := s.latestBoundRunID(ctx, row.WorkItemID, row.ConversationID)
	if err != nil {
		return api.ExternalRun{}, err
	}
	if runID == "" {
		return out, nil
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return api.ExternalRun{}, apiStoreErr(err)
	}
	apiRun := toAPIRun(s.withSource(ctx, run), s.snapshot())
	out.Run = &apiRun
	return out, nil
}

func (s *Server) latestBoundRunID(ctx context.Context, workItemID, conversationID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM runs
		WHERE work_item_id = $1 OR ($2 <> '' AND conversation_id = $2)
		ORDER BY created_at DESC
		LIMIT 1`, workItemID, conversationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) scheduleFactory(ctx context.Context, factoryID string) (string, error) {
	factoryID = strings.TrimSpace(factoryID)
	if factoryID == "" {
		fac := s.snapshot().factory
		if fac.ID == "" {
			return "", api.ErrNotFound
		}
		return fac.ID, nil
	}
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return "", err
	}
	return fac.ID, nil
}

func (s *Server) nextFire(expr string) (time.Time, error) {
	parsed, err := cron.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	next := parsed.Next(s.now())
	if next.IsZero() {
		return time.Time{}, errors.New("server: schedule has no next fire")
	}
	return next, nil
}

func toAPIDelivery(row store.Delivery) api.Delivery {
	matched := row.MatchedAutomations
	if matched == nil {
		matched = []string{}
	}
	body := json.RawMessage(nil)
	if json.Valid(row.Body) {
		body = append(json.RawMessage(nil), row.Body...)
	} else if len(row.Body) > 0 {
		encoded, err := json.Marshal(string(row.Body))
		if err == nil {
			body = encoded
		}
	}
	return api.Delivery{
		ID:                 row.ID,
		FactoryID:          row.FactoryID,
		Provider:           row.Provider,
		WebhookUID:         row.WebhookUID,
		DeliveryID:         row.DeliveryID,
		ReceivedAt:         row.ReceivedAt,
		Headers:            row.Headers,
		Body:               body,
		Truncated:          row.Truncated,
		Status:             row.Status,
		MatchedAutomations: matched,
		NormalizedEvents:   decodeNormalizedEvents(row.Normalized),
	}
}

func decodeNormalizedEvents(raw []byte) []json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var events []json.RawMessage
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil
	}
	return events
}

func toAPISchedules(rows []store.Schedule) []api.Schedule {
	out := make([]api.Schedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPISchedule(row))
	}
	return out
}

func toAPISchedule(row store.Schedule) api.Schedule {
	return api.Schedule{
		ID:          row.ID,
		FactoryID:   row.FactoryID,
		Automation:  row.Automation,
		Name:        row.Name,
		Cron:        row.Cron,
		LastFiredAt: row.LastFiredAt,
		NextFireAt:  row.NextFireAt,
		Paused:      row.Paused,
	}
}

func apiStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return api.ErrNotFound
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrImmutable):
		return api.ErrConflict
	default:
		return err
	}
}

var (
	_ api.Backend                = apiFacade{}
	_ api.WorkBackend            = apiFacade{}
	_ api.SchemaCatalog          = apiFacade{}
	_ api.DeliveryBackend        = apiFacade{}
	_ api.ScheduleBackend        = apiFacade{}
	_ api.ExternalRefBackend     = apiFacade{}
	_ api.InputBackend           = apiFacade{}
	_ api.IngressMounts          = apiFacade{}
	_ api.ConsoleBackend         = apiFacade{}
	_ api.DefinitionTreeApplier  = apiFacade{}
	_ api.SteerHistory           = apiFacade{}
	_ api.WebhookDeliveryBackend = apiFacade{}
)
