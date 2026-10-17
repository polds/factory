// Package client is the factoryctl REST and SSE client for the control plane.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "http://localhost:8080"

// Client calls factoryd's /api/v1 routes.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// Backoff is the delay before a watch reconnect. Nil uses a short exponential delay.
	Backoff func(attempt int) time.Duration
	// PollInterval is the delay between work-item polls in WatchTask.
	// Zero uses a short default. Run watch reconnects keep using Backoff.
	PollInterval time.Duration
}

// RunRequest is the POST /agent/run body.
type RunRequest struct {
	Factory  string `json:"factory,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Prompt   string `json:"prompt"`
	Title    string `json:"title,omitempty"`
	Priority string `json:"priority,omitempty"`
	Repo     string `json:"repo,omitempty"`
}

// Run is the control-plane run object returned by start and cancel.
// WorkItemID is set on factory run rows once the API emits it (T10).
// ParentRunID, ConversationID, Stage, and ModelID are the stage-run
// columns. Empty for a bare run.
type Run struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	WorkItemID     string `json:"work_item_id,omitempty"`
	ParentRunID    string `json:"parent_run_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Stage          string `json:"stage,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
}

// StartRun creates a run. The API key is sent as a bearer token and is never copied into the body.
func (c *Client) StartRun(ctx context.Context, req RunRequest) (Run, error) {
	var run Run
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/run", req, &run, http.StatusCreated)
	return run, err
}

// CancelRun requests cancellation and returns the run.
func (c *Client) CancelRun(ctx context.Context, runID string) (Run, error) {
	var run Run
	path := "/api/v1/agent/runs/" + url.PathEscape(runID) + "/cancel"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &run, http.StatusOK)
	return run, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, in any, out any, okStatus int) error {
	return c.doJSONQuery(ctx, method, path, nil, in, out, okStatus)
}

func (c *Client) doJSONQuery(ctx context.Context, method, path string, q url.Values, in any, out any, okStatus int) error {
	if err := c.requireKey(); err != nil {
		return err
	}
	return c.doBearer(ctx, c.APIKey, method, path, q, in, out, okStatus)
}

// doBearer is doJSONQuery with an explicit bearer. Steer uses the run token
// here instead of the factory API key.
func (c *Client) doBearer(ctx context.Context, token, method, path string, q url.Values, in any, out any, okStatus int) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	endpoint, err := c.endpoint(path, q)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != okStatus {
		return problemError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) requireKey() error {
	if c.APIKey == "" {
		return errors.New("FACTORY_API_KEY is required")
	}
	return nil
}

func (c *Client) endpoint(path string, q url.Values) (string, error) {
	base, err := url.Parse(c.base())
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	u := base.ResolveReference(ref)
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func (c *Client) base() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return defaultBaseURL
	}
	return base
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) sleep(ctx context.Context, attempt int) error {
	d := c.delay(attempt)
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) delay(attempt int) time.Duration {
	if c.Backoff != nil {
		return c.Backoff(attempt)
	}
	d := 100 * time.Millisecond
	for i := 1; i < attempt && d < 2*time.Second; i++ {
		d *= 2
	}
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

type problemBody struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func problemError(status int, raw []byte) error {
	var p problemBody
	if err := json.Unmarshal(raw, &p); err == nil {
		if p.Detail != "" {
			return fmt.Errorf("api %d: %s", status, p.Detail)
		}
		if p.Title != "" {
			return fmt.Errorf("api %d: %s", status, p.Title)
		}
	}
	return fmt.Errorf("api %d", status)
}
