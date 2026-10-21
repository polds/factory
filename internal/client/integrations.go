package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/api"
)

// DeliveryFilter selects deliveries. Unmatched sets status=unmatched.
// IncludeBody asks the API for the stored payload. The default list omits it.
type DeliveryFilter struct {
	Status      string
	Unmatched   bool
	IncludeBody bool
	Limit       int
}

// Delivery is one persisted ingress payload.
// Body is present only when IncludeBody was set.
type Delivery = api.Delivery

// Schedule is one cron schedule.
type Schedule = api.Schedule

// ExternalRun is the binding returned by run-by-external-reference.
type ExternalRun struct {
	Provider       string `json:"provider"`
	ExternalRef    string `json:"external_ref"`
	WorkItemID     string `json:"work_item_id"`
	ConversationID string `json:"conversation_id"`
	Run            *Run   `json:"run,omitempty"`
}

// InputAnswer is the recorded answer for a work item.
type InputAnswer = api.InputAnswer

type answerBody struct {
	Text string `json:"text"`
}

// ListDeliveries lists deliveries for a factory. The API key is a bearer token.
func (c *Client) ListDeliveries(ctx context.Context, factory string, f DeliveryFilter) ([]Delivery, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	q := url.Values{}
	switch {
	case f.Unmatched:
		q.Set("status", "unmatched")
	case f.Status != "":
		q.Set("status", f.Status)
	}
	if f.IncludeBody {
		q.Set("include", "body")
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	var rows []Delivery
	err := c.doJSONQuery(ctx, http.MethodGet, factoryPath(factory, "deliveries"), q, nil, &rows, http.StatusOK)
	return rows, err
}

// ListWebhookDeliveries lists one webhook's deliveries, newest first.
func (c *Client) ListWebhookDeliveries(ctx context.Context, factory, webhookUID string, f DeliveryFilter) ([]Delivery, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	if err := requireID(webhookUID, "webhook id"); err != nil {
		return nil, err
	}
	q := url.Values{}
	switch {
	case f.Unmatched:
		q.Set("status", "unmatched")
	case f.Status != "":
		q.Set("status", f.Status)
	}
	if f.IncludeBody {
		q.Set("include", "body")
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	var rows []Delivery
	err := c.doJSONQuery(ctx, http.MethodGet, factoryPath(factory, "webhooks", webhookUID, "deliveries"), q, nil, &rows, http.StatusOK)
	return rows, err
}

// ReplayDelivery requeues a stored delivery. The request does not carry a
// signature and has no parameter that skips ingress verification.
func (c *Client) ReplayDelivery(ctx context.Context, id string) (Delivery, error) {
	if err := requireID(id, "delivery id"); err != nil {
		return Delivery{}, err
	}
	var row Delivery
	path := "/api/v1/deliveries/" + url.PathEscape(id) + ":replay"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &row, http.StatusOK)
	return row, err
}

// ListSchedules lists schedules. An empty factory lists every schedule the key can see.
func (c *Client) ListSchedules(ctx context.Context, factory string) ([]Schedule, error) {
	q := url.Values{}
	if strings.TrimSpace(factory) != "" {
		q.Set("factory", factory)
	}
	var rows []Schedule
	err := c.doJSONQuery(ctx, http.MethodGet, "/api/v1/agent/schedules", q, nil, &rows, http.StatusOK)
	return rows, err
}

// PauseSchedule pauses one schedule.
func (c *Client) PauseSchedule(ctx context.Context, id string) (Schedule, error) {
	return c.setSchedulePaused(ctx, id, true)
}

// ResumeSchedule resumes one schedule.
func (c *Client) ResumeSchedule(ctx context.Context, id string) (Schedule, error) {
	return c.setSchedulePaused(ctx, id, false)
}

func (c *Client) setSchedulePaused(ctx context.Context, id string, pause bool) (Schedule, error) {
	if err := requireID(id, "schedule id"); err != nil {
		return Schedule{}, err
	}
	action := "resume"
	if pause {
		action = "pause"
	}
	var row Schedule
	path := "/api/v1/agent/schedules/" + url.PathEscape(id) + "/" + action
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &row, http.StatusOK)
	return row, err
}

// RunByExternalReference resolves a provider thread to its work item and run.
func (c *Client) RunByExternalReference(ctx context.Context, provider, ref string) (ExternalRun, error) {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(ref) == "" {
		return ExternalRun{}, errors.New("provider and ref are required")
	}
	q := url.Values{}
	q.Set("provider", provider)
	q.Set("ref", ref)
	var row ExternalRun
	err := c.doJSONQuery(ctx, http.MethodGet, "/api/v1/agent/run-by-external-reference", q, nil, &row, http.StatusOK)
	return row, err
}

// AnswerInput posts the text that answers a work item's pending question.
func (c *Client) AnswerInput(ctx context.Context, workItemID, text string) (InputAnswer, error) {
	if err := requireID(workItemID, "task id"); err != nil {
		return InputAnswer{}, err
	}
	if strings.TrimSpace(text) == "" {
		return InputAnswer{}, errors.New("text is required")
	}
	var row InputAnswer
	path := "/api/v1/work-items/" + url.PathEscape(workItemID) + "/input"
	err := c.doJSON(ctx, http.MethodPost, path, answerBody{Text: text}, &row, http.StatusOK)
	return row, err
}
