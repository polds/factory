package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://slack.com/api"
	defaultRetries = 3
	maxBody        = 1 << 20
)

// Poster sends one thread root, or edits a message in place.
type Poster interface {
	Post(ctx context.Context, channel, threadTS, text string, blocks json.RawMessage) (ts string, err error)
	Update(ctx context.Context, channel, ts, text string, blocks json.RawMessage) error
}

// Client is a Slack Web API client. It is safe for concurrent use.
type Client struct {
	baseURL string
	token   secret
	http    *http.Client
	sleep   func(context.Context, time.Duration) error
	max429  int
}

// Error is a Slack Web API failure. Code is the ok:false error field,
// or a short local reason. It never contains the bot token.
type Error struct {
	Method string
	Code   string
	Status int
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("slack %s: %s", e.Method, e.Code)
	}
	return fmt.Sprintf("slack %s: http %d", e.Method, e.Status)
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL sets the API root. The default is https://slack.com/api.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithHTTPClient sets the HTTP client. Nil keeps the default, which does
// not follow redirects.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithSleep replaces the wait used between 429 retries.
func WithSleep(fn func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = fn }
}

// WithRateLimitRetries sets how many extra attempts follow an HTTP 429.
// The default is 3. Zero tries the request once.
func WithRateLimitRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.max429 = n
	}
}

// New returns a client for token. token is a bot token (xoxb-).
func New(token string, opts ...Option) *Client {
	c := &Client{
		baseURL: defaultBaseURL,
		token:   secret(token),
		http:    defaultHTTP(),
		sleep:   sleep,
		max429:  defaultRetries,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if c.http == nil {
		c.http = defaultHTTP()
	}
	if c.sleep == nil {
		c.sleep = sleep
	}
	return c
}

func (c *Client) String() string { return "slack.Client" }

func (c *Client) GoString() string {
	if c == nil {
		return "(*slack.Client)(nil)"
	}
	return fmt.Sprintf("&slack.Client{baseURL:%q, token:\"[REDACTED]\"}", c.baseURL)
}

// Post calls chat.postMessage and returns the message ts.
func (c *Client) Post(ctx context.Context, channel, threadTS, text string, blocks json.RawMessage) (string, error) {
	blocks, err := cleanBlocks(blocks)
	if err != nil {
		return "", c.redact(err)
	}
	var out apiResponse
	err = c.call(ctx, "chat.postMessage", postBody{
		Channel:  channel,
		ThreadTS: threadTS,
		Text:     text,
		Blocks:   blocks,
	}, &out)
	if err != nil {
		return "", err
	}
	if out.TS == "" {
		return "", c.redact(&Error{Method: "chat.postMessage", Code: "missing ts"})
	}
	return out.TS, nil
}

// Update calls chat.update for an existing message ts.
func (c *Client) Update(ctx context.Context, channel, ts, text string, blocks json.RawMessage) error {
	blocks, err := cleanBlocks(blocks)
	if err != nil {
		return c.redact(err)
	}
	var out apiResponse
	return c.call(ctx, "chat.update", updateBody{
		Channel: channel,
		TS:      ts,
		Text:    text,
		Blocks:  blocks,
	}, &out)
}

func (c *Client) call(ctx context.Context, method string, payload any, dest *apiResponse) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return c.redact(&Error{Method: method, Code: "encode"})
	}
	endpoint := c.baseURL + "/" + method
	attempts := c.max429 + 1
	for attempt := 0; attempt < attempts; attempt++ {
		status, body, retry, err := c.do(ctx, endpoint, buf)
		if err != nil {
			return c.redact(err)
		}
		if status == http.StatusTooManyRequests && attempt+1 < attempts {
			if err := c.sleep(ctx, retry); err != nil {
				return c.redact(err)
			}
			continue
		}
		parsed, perr := decode(method, status, body, string(c.token))
		if perr != nil {
			return c.redact(perr)
		}
		*dest = parsed
		return nil
	}
	return c.redact(&Error{Method: method, Status: http.StatusTooManyRequests, Code: "rate limited"})
}

func (c *Client) do(ctx context.Context, endpoint string, buf []byte) (int, []byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+string(c.token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	wait := retryAfter(resp.Header.Get("Retry-After"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, 0, err
	}
	if len(body) > maxBody {
		return 0, nil, 0, errors.New("slack: response too large")
	}
	return resp.StatusCode, body, wait, nil
}

func (c *Client) redact(err error) error {
	if err == nil {
		return nil
	}
	tok := string(c.token)
	msg := err.Error()
	if tok == "" || !strings.Contains(msg, tok) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, tok, "[REDACTED]"))
}

func decode(method string, status int, body []byte, token string) (apiResponse, error) {
	if status == http.StatusTooManyRequests {
		return apiResponse{}, &Error{Method: method, Status: status, Code: "rate limited"}
	}
	var out apiResponse
	if err := json.Unmarshal(body, &out); err != nil || (status/100 != 2 && out.Error == "" && !out.OK) {
		return apiResponse{}, &Error{Method: method, Status: status}
	}
	if !out.OK {
		code := out.Error
		if code == "" {
			code = "not ok"
		}
		if token != "" {
			code = strings.ReplaceAll(code, token, "[REDACTED]")
		}
		return apiResponse{}, &Error{Method: method, Status: status, Code: code}
	}
	if status/100 != 2 {
		return apiResponse{}, &Error{Method: method, Status: status}
	}
	return out, nil
}

func cleanBlocks(b json.RawMessage) (json.RawMessage, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil, nil
	}
	if !json.Valid(b) {
		return nil, errors.New("slack: invalid blocks")
	}
	return b, nil
}

func retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return time.Second
	}
	sec, err := strconv.Atoi(h)
	if err != nil || sec < 0 {
		return time.Second
	}
	return time.Duration(sec) * time.Second
}

func sleep(ctx context.Context, d time.Duration) error {
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

func defaultHTTP() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type secret string

func (s secret) String() string   { return "[REDACTED]" }
func (s secret) GoString() string { return `"[REDACTED]"` }

type postBody struct {
	Channel  string          `json:"channel"`
	ThreadTS string          `json:"thread_ts,omitempty"`
	Text     string          `json:"text,omitempty"`
	Blocks   json.RawMessage `json:"blocks,omitempty"`
}

type updateBody struct {
	Channel string          `json:"channel"`
	TS      string          `json:"ts"`
	Text    string          `json:"text,omitempty"`
	Blocks  json.RawMessage `json:"blocks,omitempty"`
}

type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	TS    string `json:"ts"`
}

var _ Poster = (*Client)(nil)
