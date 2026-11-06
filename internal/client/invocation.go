package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// InvocationRequest is the public invoke body.
type InvocationRequest struct {
	DefinitionRevision  string         `json:"definitionRevision,omitempty"`
	Profile             *ProfileName   `json:"profile,omitempty"`
	Target              *InvokeTarget  `json:"target,omitempty"`
	Inputs              map[string]any `json:"inputs,omitempty"`
	SourceOccurrenceKey string         `json:"sourceOccurrenceKey"`
	Intent              string         `json:"intent"`
	Priority            string         `json:"priority"`
}

type ProfileName struct {
	Name string `json:"name"`
}

type InvokeTarget struct {
	Repository  string `json:"repository,omitempty"`
	Ref         string `json:"ref,omitempty"`
	PullRequest int    `json:"pullRequest,omitempty"`
}

// Invoke posts a defined-factory invocation.
func (c *Client) Invoke(ctx context.Context, factoryUID string, req InvocationRequest) (json.RawMessage, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	endpoint, err := c.endpoint("/api/v1/factory/"+factoryUID+"/invocations", nil)
	if err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}
