package linear

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

const (
	defaultEndpoint = "https://api.linear.app/graphql"
	maxResponse     = 1 << 20
)

const queryCommentCreate = `mutation CommentCreate($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    success
    comment { id }
  }
}`

const queryCommentUpdate = `mutation CommentUpdate($id: String!, $input: CommentUpdateInput!) {
  commentUpdate(id: $id, input: $input) {
    success
    comment { id }
  }
}`

// ErrEmptyKey, ErrEmptyIssue, and ErrEmptyComment report a call that was not sent.
var (
	ErrEmptyKey     = errors.New("linear API key is empty")
	ErrEmptyIssue   = errors.New("linear issue id is empty")
	ErrEmptyComment = errors.New("linear comment id is empty")
)

// APIError is a Linear HTTP or GraphQL failure. Message never contains the API key.
type APIError struct {
	Op      string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("linear %s: %s", e.Op, e.Message)
	}
	return fmt.Sprintf("linear %s: HTTP %d: %s", e.Op, e.Status, e.Message)
}

// Client is a GraphQL comment client. HTTP, when set, replaces the default client.
type Client struct {
	endpoint string
	key      string
	HTTP     *http.Client
}

// New returns a client for endpoint. An empty endpoint uses the Linear GraphQL API.
// apiKey is trimmed and stored for the Authorization header.
func New(endpoint, apiKey string) *Client {
	return &Client{
		endpoint: strings.TrimSpace(endpoint),
		key:      strings.TrimSpace(apiKey),
	}
}

// String returns a description that omits the API key.
func (c *Client) String() string {
	if c == nil {
		return "linear.Client"
	}
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return "linear.Client{endpoint:" + c.redact(endpoint) + "}"
}

// CreateComment posts markdown on issueID and returns the created comment id.
func (c *Client) CreateComment(ctx context.Context, issueID, markdown string) (string, error) {
	if err := c.ready(); err != nil {
		return "", err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return "", ErrEmptyIssue
	}
	var out struct {
		CommentCreate *commentPayload `json:"commentCreate"`
	}
	err := c.mutate(ctx, "commentCreate", queryCommentCreate, map[string]any{
		"input": map[string]string{
			"issueId": issueID,
			"body":    markdown,
		},
	}, &out)
	if err != nil {
		return "", err
	}
	return createdID("commentCreate", out.CommentCreate)
}

// UpdateComment replaces the body of an existing comment.
func (c *Client) UpdateComment(ctx context.Context, commentID, markdown string) error {
	if err := c.ready(); err != nil {
		return err
	}
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return ErrEmptyComment
	}
	var out struct {
		CommentUpdate *commentPayload `json:"commentUpdate"`
	}
	err := c.mutate(ctx, "commentUpdate", queryCommentUpdate, map[string]any{
		"id": commentID,
		"input": map[string]string{
			"body": markdown,
		},
	}, &out)
	if err != nil {
		return err
	}
	if out.CommentUpdate == nil || !out.CommentUpdate.Success {
		return &APIError{Op: "commentUpdate", Status: http.StatusOK, Message: "unsuccessful"}
	}
	return nil
}

type commentPayload struct {
	Success bool `json:"success"`
	Comment *struct {
		ID string `json:"id"`
	} `json:"comment"`
}

type graphRequest struct {
	Query     string `json:"query"`
	Variables any    `json:"variables"`
}

func createdID(op string, payload *commentPayload) (string, error) {
	if payload == nil || !payload.Success || payload.Comment == nil || payload.Comment.ID == "" {
		return "", &APIError{Op: op, Status: http.StatusOK, Message: "unsuccessful"}
	}
	return payload.Comment.ID, nil
}

func (c *Client) mutate(ctx context.Context, op, query string, vars map[string]any, dest any) error {
	payload, err := json.Marshal(graphRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("linear %s: %w", op, err)
	}
	endpoint, err := c.resolveEndpoint()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return c.redactErr(op, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.key)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return c.redactErr(op, err)
	}
	body, err := readLimited(resp)
	if err != nil {
		return c.redactErr(op, err)
	}
	// Drop the key before any error text is built, including a snippet cut
	// through the middle of the key.
	body = []byte(c.redact(string(body)))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Op: op, Status: resp.StatusCode, Message: failureMessage(body)}
	}
	data, err := decodeData(body)
	if err != nil {
		return &APIError{Op: op, Status: resp.StatusCode, Message: err.Error()}
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("linear %s: %w", op, err)
	}
	return nil
}

func (c *Client) ready() error {
	if c == nil || c.key == "" {
		return ErrEmptyKey
	}
	return nil
}

func (c *Client) resolveEndpoint() (string, error) {
	endpoint := defaultEndpoint
	if c != nil && c.endpoint != "" {
		endpoint = c.endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		shown := endpoint
		if c != nil {
			shown = c.redact(endpoint)
		}
		return "", fmt.Errorf("linear endpoint must be an absolute http(s) URL: %s", shown)
	}
	return endpoint, nil
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTPClient
}

var defaultHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (c *Client) redact(s string) string {
	if c == nil || c.key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.key, "[REDACTED]")
}

func (c *Client) redactErr(op string, err error) error {
	msg := c.redact(err.Error())
	if msg != err.Error() {
		return fmt.Errorf("linear %s: %s", op, msg)
	}
	return fmt.Errorf("linear %s: %w", op, err)
}

func failureMessage(body []byte) string {
	msg := graphMessages(body)
	if msg == "" {
		msg = snippet(body)
	}
	if msg == "" {
		msg = "empty response"
	}
	return msg
}

func readLimited(resp *http.Response) ([]byte, error) {
	if resp.Body == nil {
		return nil, errors.New("linear response has no body")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(body) > maxResponse {
		return nil, errors.New("linear response exceeds 1MiB")
	}
	return body, nil
}

type graphError struct {
	Message string `json:"message"`
}

func decodeData(body []byte) (json.RawMessage, error) {
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphError    `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, errors.New(snippet(body))
	}
	if len(env.Errors) > 0 {
		msg := joinErrors(env.Errors)
		if msg == "" {
			msg = "graphql error"
		}
		return nil, errors.New(msg)
	}
	if len(bytes.TrimSpace(env.Data)) == 0 || bytes.Equal(bytes.TrimSpace(env.Data), []byte("null")) {
		return nil, errors.New("empty data")
	}
	return env.Data, nil
}

func graphMessages(body []byte) string {
	var env struct {
		Errors []graphError `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return joinErrors(env.Errors)
}

func joinErrors(errs []graphError) string {
	parts := make([]string, 0, len(errs))
	for _, item := range errs {
		if item.Message != "" {
			parts = append(parts, item.Message)
		}
	}
	return strings.Join(parts, "; ")
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
