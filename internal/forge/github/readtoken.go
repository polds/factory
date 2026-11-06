package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// commitSHAPattern is a Git object id. Short names are not a pin.
var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// ReadPermissions is the scanner installation-token scope.
// It is contents read and metadata read, with no write grant.
func ReadPermissions() map[string]string {
	return map[string]string{
		"contents": "read",
		"metadata": "read",
	}
}

// ReadMint is a request for a read-only installation token bound to one pinned checkout.
type ReadMint struct {
	InstallationID int64
	Repo           string
	AllowedRepos   []string
	PinnedSHA      string
	CheckoutSHA    string
}

// ReadToken is an installation token that cannot push, open a pull request, or comment.
type ReadToken struct {
	token Token
	repo  string
	sha   string
}

// Token returns the underlying installation token for read calls such as clone.
func (t ReadToken) Token() Token { return t.token }

// Value returns the secret. Formatting redacts it.
func (t ReadToken) Value() string { return t.token.Value() }

// Repo is the single repository this token was minted for.
func (t ReadToken) Repo() string { return t.repo }

// SHA is the pinned checkout.
func (t ReadToken) SHA() string { return t.sha }

// Permissions returns the read scope. The map is a copy.
func (t ReadToken) Permissions() map[string]string { return ReadPermissions() }

func (t ReadToken) String() string   { return tokenRedacted }
func (t ReadToken) GoString() string { return "github.ReadToken{}" }

// Format redacts the secret for every verb.
func (t ReadToken) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, tokenRedacted) }

// LogValue implements slog.LogValuer.
func (t ReadToken) LogValue() slog.Value { return slog.StringValue(tokenRedacted) }

// MarshalJSON refuses to serialize the token.
func (ReadToken) MarshalJSON() ([]byte, error) { return nil, errSerialize }

// Push is rejected. A read token cannot update refs.
func (ReadToken) Push() error { return errReadOnly }

// CreatePullRequest is rejected.
func (ReadToken) CreatePullRequest() error { return errReadOnly }

// Comment is rejected.
func (ReadToken) Comment() error { return errReadOnly }

var errReadOnly = errors.New("github: read token cannot push, create a pull request, or comment")

// MintReadToken mints contents:read and metadata:read for one allowed repository
// whose checkout SHA is the pinned commit. Unrelated repositories and unpinned
// checkouts are rejected before GitHub is called.
func (c *Client) MintReadToken(ctx context.Context, req ReadMint) (ReadToken, error) {
	repo := strings.TrimSpace(req.Repo)
	if req.InstallationID <= 0 {
		return ReadToken{}, errors.New("github: installation id is required")
	}
	if repo == "" || !repoAllowed(repo, req.AllowedRepos) {
		return ReadToken{}, fmt.Errorf("github: unrelated repository %s", repo)
	}
	pin := strings.TrimSpace(req.PinnedSHA)
	checkout := strings.TrimSpace(req.CheckoutSHA)
	if !commitSHAPattern.MatchString(pin) || pin != checkout {
		return ReadToken{}, errors.New("github: checkout is not the pinned commit SHA")
	}
	tok, err := c.InstallationToken(ctx, req.InstallationID, []string{repo}, ReadPermissions())
	if err != nil {
		return ReadToken{}, err
	}
	return ReadToken{token: tok, repo: repo, sha: pin}, nil
}

func repoAllowed(repo string, allowed []string) bool {
	for _, name := range allowed {
		if strings.TrimSpace(name) == repo {
			return true
		}
	}
	return false
}
