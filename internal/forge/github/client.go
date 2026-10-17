package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiVersion       = "2026-03-10"
	userAgent        = "factory"
	maxBody          = 1 << 20
	defaultBaseURL   = "https://api.github.com"
	tokenRedacted    = "github-installation-token"
	defaultCheckName = "factory"
)

// ErrPermission is returned for GitHub 403 and 404 responses.
// The error text names the permission the call required.
var ErrPermission = errors.New("github: permission denied")

var errSerialize = errors.New("github: installation token must not be serialized")

// PermissionError is a GitHub 403 or 404. Permission names the missing scope.
type PermissionError struct {
	Op         string
	StatusCode int
	Permission string
	Detail     string
}

func (e *PermissionError) Error() string {
	detail := e.Detail
	if detail == "" {
		detail = "request rejected"
	}
	return fmt.Sprintf("github: %s: missing permission %s (HTTP %d): %s", e.Op, e.Permission, e.StatusCode, detail)
}

func (e *PermissionError) Unwrap() error { return ErrPermission }

// App is the GitHub App surface used by outbound and run-token injection.
type App interface {
	InstallationByRepo(ctx context.Context, repo string) (int64, error)
	InstallationToken(ctx context.Context, installationID int64, repos []string, perms map[string]string) (Token, error)
	CreateCheckRun(ctx context.Context, tok Token, repo, headSHA string, cr CheckRun) (int64, error)
	UpdateCheckRun(ctx context.Context, tok Token, repo string, id int64, cr CheckRun) error
	UpsertIssueComment(ctx context.Context, tok Token, repo string, number int, marker, body string) error
}

// Config builds a Client. BaseURL defaults to https://api.github.com.
// Now and Log are optional.
type Config struct {
	AppID   int64
	Key     *rsa.PrivateKey
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time
	Log     *slog.Logger
}

// Client calls the GitHub REST API with an app private key.
type Client struct {
	appID   int64
	key     *rsa.PrivateKey
	baseURL string
	http    *http.Client
	now     func() time.Time
	log     *slog.Logger

	mu           sync.Mutex
	cache        map[string]Token
	checkStarted map[int64]time.Time
}

var _ App = (*Client)(nil)

// New returns a client. The private key is used only to sign app JWTs.
func New(cfg Config) (*Client, error) {
	if cfg.AppID <= 0 {
		return nil, errors.New("github: app id is required")
	}
	if cfg.Key == nil {
		return nil, errors.New("github: private key is required")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ht := cfg.HTTP
	if ht == nil {
		ht = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		appID:        cfg.AppID,
		key:          cfg.Key,
		baseURL:      base,
		http:         ht,
		now:          now,
		log:          log,
		cache:        map[string]Token{},
		checkStarted: map[int64]time.Time{},
	}, nil
}

// Format prints the app id, base URL, and cache size. It does not walk fields,
// so fmt cannot reach cached tokens or the private key through an unexported field.
func (c *Client) Format(s fmt.State, verb rune) {
	_, _ = io.WriteString(s, c.redacted(verb == 'v' && s.Flag('#')))
}

// String is the redacted client description.
func (c *Client) String() string { return c.redacted(false) }

// GoString is the redacted Go-syntax client description.
func (c *Client) GoString() string { return c.redacted(true) }

func (c *Client) redacted(sharp bool) string {
	if c == nil {
		return "<nil>"
	}
	c.mu.Lock()
	appID := c.appID
	base := c.baseURL
	cached := len(c.cache)
	c.mu.Unlock()
	if sharp {
		return fmt.Sprintf("github.Client{appID:%d, baseURL:%q, cached:%d}", appID, base, cached)
	}
	return fmt.Sprintf("github.Client{appID:%d base:%s cached:%d}", appID, base, cached)
}

func (c *Client) appJWT() (string, error) {
	now := c.now()
	header, err := b64JSON(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := b64JSON(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": strconv.FormatInt(c.appID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + payload
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign jwt: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64JSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type ghMessage struct {
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path, auth string, in any, out any, op, perm string) error {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return scrubErr(err, auth)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return scrubErr(err, auth)
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return &PermissionError{
			Op:         op,
			StatusCode: resp.StatusCode,
			Permission: perm,
			Detail:     scrub(githubDetail(raw), auth),
		}
	}
	if resp.StatusCode/100 != 2 {
		return &StatusError{
			Op:         op,
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Detail:     scrub(githubDetail(raw), auth),
		}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) endpoint(path string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("github: base url: %w", err)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("github: path: %w", err)
	}
	return base.ResolveReference(ref).String(), nil
}

func githubDetail(raw []byte) string {
	var msg ghMessage
	if err := json.Unmarshal(raw, &msg); err == nil && msg.Message != "" {
		return msg.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func scrub(msg string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) < 8 {
			continue
		}
		msg = strings.ReplaceAll(msg, secret, tokenRedacted)
	}
	return msg
}

func scrubErr(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := scrub(err.Error(), secrets...)
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, err: err}
}

// redactedError hides secret text and keeps the cause so a timeout or
// connection error is still classifiable after the message is scrubbed.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Unwrap() error { return e.err }

func splitRepo(repo string) (string, string, error) {
	repo = strings.TrimSpace(repo)
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("github: repository %q must be owner/name", repo)
	}
	return owner, name, nil
}
