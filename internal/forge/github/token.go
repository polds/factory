package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Token is an installation access token. The secret is available only
// through Value. Formatting, JSON, and slog redact it.
type Token struct {
	value     secret
	expiresAt time.Time
}

type secret string

func (secret) String() string             { return tokenRedacted }
func (secret) GoString() string           { return tokenRedacted }
func (secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, tokenRedacted) }
func (secret) LogValue() slog.Value       { return slog.StringValue(tokenRedacted) }

func (t Token) Value() string        { return string(t.value) }
func (t Token) ExpiresAt() time.Time { return t.expiresAt }
func (t Token) String() string       { return tokenRedacted }
func (t Token) GoString() string     { return "github.Token{}" }
func (t Token) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, tokenRedacted)
}
func (t Token) LogValue() slog.Value { return slog.StringValue(tokenRedacted) }
func (Token) MarshalJSON() ([]byte, error) {
	return nil, errSerialize
}
func (Token) MarshalText() ([]byte, error) {
	return nil, errSerialize
}

func (t Token) fresh(now time.Time) bool {
	if t.value == "" {
		return false
	}
	return now.Before(t.expiresAt.Add(-5 * time.Minute))
}

// DefaultPermissions is the installation-token scope used when perms is empty.
func DefaultPermissions() map[string]string {
	return map[string]string{
		"contents":      "write",
		"pull_requests": "write",
		"issues":        "write",
		"checks":        "write",
		"metadata":      "read",
	}
}

// InstallationByRepo returns the App installation id for an owner/name repository.
func (c *Client) InstallationByRepo(ctx context.Context, repo string) (int64, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return 0, err
	}
	jwt, err := c.appJWT()
	if err != nil {
		return 0, err
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/installation"
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, path, jwt, nil, &out, "installation", "metadata"); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, errors.New("github: installation id missing")
	}
	c.log.Info("github installation", "repo", owner+"/"+name, "installation_id", out.ID)
	return out.ID, nil
}

type accessTokenBody struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}

type accessTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// InstallationToken mints a token for installationID scoped to repos and perms.
// repos may be owner/name or a bare repository name. owner/name values are
// sent as the repository name, and every owner/name must share one owner.
// An empty perms map selects DefaultPermissions. A cached token is reused
// until five minutes before expiry. GitHub expiries later than one hour are
// clamped to one hour.
func (c *Client) InstallationToken(ctx context.Context, installationID int64, repos []string, perms map[string]string) (Token, error) {
	if installationID <= 0 {
		return Token{}, errors.New("github: installation id is required")
	}
	names, err := repositoryNames(repos)
	if err != nil {
		return Token{}, err
	}
	scope, err := normalizePerms(perms)
	if err != nil {
		return Token{}, err
	}
	key := tokenCacheKey(installationID, names, scope)
	if tok, ok := c.cached(key); ok {
		c.log.Debug("github installation token cache hit", "installation_id", installationID, "repos", len(names))
		return tok, nil
	}
	jwt, err := c.appJWT()
	if err != nil {
		return Token{}, err
	}
	path := "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	var resp accessTokenResponse
	err = c.do(ctx, http.MethodPost, path, jwt, accessTokenBody{
		Repositories: names,
		Permissions:  scope,
	}, &resp, "access_token", permissionList(scope))
	if err != nil {
		return Token{}, err
	}
	tok, err := c.parseToken(resp)
	if err != nil {
		return Token{}, scrubErr(err, jwt, resp.Token)
	}
	if tok.fresh(c.now()) {
		c.store(key, tok)
	}
	c.log.Info("github installation token minted",
		"installation_id", installationID,
		"repos", len(names),
		"expires_at", tok.ExpiresAt().UTC().Format(time.RFC3339),
	)
	return tok, nil
}

func (c *Client) cached(key string) (Token, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tok, ok := c.cache[key]
	if !ok || !tok.fresh(c.now()) {
		return Token{}, false
	}
	return tok, true
}

func (c *Client) store(key string, tok Token) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = tok
}

func (c *Client) parseToken(resp accessTokenResponse) (Token, error) {
	if resp.Token == "" {
		return Token{}, errors.New("github: empty installation token")
	}
	exp, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		return Token{}, errors.New("github: invalid installation token expiry")
	}
	now := c.now()
	if !exp.After(now) {
		return Token{}, errors.New("github: installation token already expired")
	}
	limit := now.Add(time.Hour)
	if exp.After(limit) {
		exp = limit
	}
	return Token{value: secret(resp.Token), expiresAt: exp}, nil
}

func repositoryNames(repos []string) ([]string, error) {
	if len(repos) == 0 {
		return nil, errors.New("github: at least one repository is required")
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(repos))
	var owner string
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return nil, errors.New("github: empty repository")
		}
		name := repo
		if strings.Contains(repo, "/") {
			parsedOwner, parsedName, err := splitRepo(repo)
			if err != nil {
				return nil, err
			}
			if owner != "" && owner != parsedOwner {
				return nil, fmt.Errorf("github: repositories must share one owner (%s and %s)", owner, parsedOwner)
			}
			owner = parsedOwner
			name = parsedName
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

func normalizePerms(perms map[string]string) (map[string]string, error) {
	if len(perms) == 0 {
		return DefaultPermissions(), nil
	}
	out := make(map[string]string, len(perms))
	for name, level := range perms {
		name = strings.TrimSpace(name)
		level = strings.TrimSpace(level)
		if name == "" || strings.Contains(name, ":") {
			return nil, fmt.Errorf("github: invalid permission %q", name)
		}
		switch level {
		case "read", "write", "admin":
			out[name] = level
		default:
			return nil, fmt.Errorf("github: permission %s level %q", name, level)
		}
	}
	return out, nil
}

func permissionList(perms map[string]string) string {
	keys := make([]string, 0, len(perms))
	for name := range perms {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, name := range keys {
		parts[i] = name + ":" + perms[name]
	}
	return strings.Join(parts, ",")
}

func tokenCacheKey(installationID int64, repos []string, perms map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", installationID)
	for _, repo := range repos {
		b.WriteString(repo)
		b.WriteByte('\n')
	}
	keys := make([]string, 0, len(perms))
	for name := range perms {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	for _, name := range keys {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(perms[name])
		b.WriteByte('\n')
	}
	return b.String()
}

// UnmarshalJSON rejects token payloads so a Token cannot be restored from storage.
func (Token) UnmarshalJSON([]byte) error { return errSerialize }
