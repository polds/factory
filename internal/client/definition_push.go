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

	"github.com/polds/factory/internal/api"
)

// ErrGitBackedFactory is returned when PUT definition answers 409.
// A factory is either git-backed or API-backed.
var ErrGitBackedFactory = errors.New("factory is git-backed")

type (
	// DefinitionPushRequest is the definition-push body.
	DefinitionPushRequest = api.DefinitionPushRequest
	// DefinitionPushResponse is the validation result.
	DefinitionPushResponse = api.DefinitionPushResponse
)

// PushDefinition calls PUT /api/v1/factory/{name}/definition.
// A 409 from a git-backed factory is ErrGitBackedFactory. The request
// body is not included in the error.
func (c *Client) PushDefinition(ctx context.Context, factory string, req DefinitionPushRequest) (DefinitionPushResponse, error) {
	var zero DefinitionPushResponse
	if err := c.requireKey(); err != nil {
		return zero, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return zero, err
	}
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/definition"
	endpoint, err := c.endpoint(path, nil)
	if err != nil {
		return zero, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(httpReq)
	if err != nil {
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return zero, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(raw, &zero); err != nil {
			return DefinitionPushResponse{}, fmt.Errorf("decode response: %w", err)
		}
		return zero, nil
	case http.StatusConflict:
		return DefinitionPushResponse{}, ErrGitBackedFactory
	default:
		return DefinitionPushResponse{}, problemError(resp.StatusCode, raw)
	}
}
