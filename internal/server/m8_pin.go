package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

var commitSHARe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func isCommitSHA(ref string) bool {
	return commitSHARe.MatchString(strings.TrimSpace(ref))
}

func (s *Server) pinGitHubPull(ctx context.Context, repo string, number int) (head, base string, err error) {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return "", "", fmt.Errorf("server: pin: repository %q", repo)
	}
	var body struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(name), number)
	if err := s.githubGET(ctx, repo, path, &body); err != nil {
		return "", "", err
	}
	if !isCommitSHA(body.Head.SHA) || !isCommitSHA(body.Base.SHA) {
		return "", "", fmt.Errorf("server: pin: pull %s#%d did not return commit SHAs", repo, number)
	}
	return body.Head.SHA, body.Base.SHA, nil
}

func (s *Server) pinGitHubRef(ctx context.Context, repo, ref string) (string, string, error) {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return "", "", fmt.Errorf("server: pin: repository %q", repo)
	}
	var body struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref))
	if err := s.githubGET(ctx, repo, path, &body); err != nil {
		return "", "", err
	}
	if !isCommitSHA(body.Object.SHA) {
		return "", "", fmt.Errorf("server: pin: ref %s did not resolve to a commit", ref)
	}
	return body.Object.SHA, "", nil
}

func (s *Server) githubGET(ctx context.Context, repo, path string, dest any) error {
	if s.integ.githubApp == nil {
		return fmt.Errorf("server: pin: github app is not configured for %s", repo)
	}
	id, err := s.integ.githubApp.InstallationByRepo(ctx, repo)
	if err != nil {
		return fmt.Errorf("server: pin: %w", err)
	}
	tok, err := s.integ.githubApp.InstallationToken(ctx, id, []string{repo}, map[string]string{"metadata": "read"})
	if err != nil {
		return fmt.Errorf("server: pin: %w", err)
	}
	base := s.integ.githubBase
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "factory")
	ht := s.cfg.HTTPClient
	if ht == nil {
		ht = http.DefaultClient
	}
	res, err := ht.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("server: pin: github HTTP %d", res.StatusCode)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("server: pin: %w", err)
	}
	return nil
}

func (s *Server) invocationCheckoutOverrides(ctx context.Context, workItemID string) ([]protocol.RepositoryHeadOverride, error) {
	if s.store == nil || workItemID == "" {
		return nil, nil
	}
	item, err := s.store.GetWorkItem(ctx, workItemID)
	if errors.Is(err, store.ErrNotFound) || item.InvocationID == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row, err := s.store.GetInvocation(ctx, item.FactoryID, item.InvocationID)
	if errors.Is(err, store.ErrNotFound) || row.PinnedHeadSHA == "" || row.TargetRepository == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owner, name, ok := splitRepo(row.TargetRepository)
	if !ok {
		return nil, fmt.Errorf("server: pin checkout: repository %q", row.TargetRepository)
	}
	return []protocol.RepositoryHeadOverride{{
		CodeForge: "GITHUB",
		RepoOwner: owner,
		RepoName:  name,
		Head: protocol.RepositoryHeadRef{
			Type:  protocol.RepositoryHeadTypeCommitSHA,
			Value: row.PinnedHeadSHA,
		},
	}}, nil
}
