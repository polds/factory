package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/polds/factory/internal/api"
	ghclient "github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/store"
)

// improvementTaskPermissions is the installation-token map on an improvement
// run. It is contents:write, pull_requests:write, and metadata:read.
// Callers receive a fresh map.
func improvementTaskPermissions() map[string]string {
	return map[string]string{
		"contents":      "write",
		"pull_requests": "write",
		"metadata":      "read",
	}
}

// improvementPRPermissions is the installation-token map factoryd uses for
// its own pull-request calls. Trees are read with the git cloner, so this
// map has no contents scope. It has no administration scope. Callers receive
// a fresh map.
func improvementPRPermissions() map[string]string {
	return map[string]string{
		"pull_requests": "write",
		"issues":        "write",
		"checks":        "write",
		"metadata":      "read",
	}
}

func (s *Server) mintRunToken(ctx context.Context, run store.Run) (string, error) {
	if run.Purpose == api.PurposeImprovement {
		return s.mintImprovementTaskToken(ctx, run)
	}
	return s.mintGitHubToken(ctx, run)
}

func (s *Server) mintImprovementTaskToken(ctx context.Context, _ store.Run) (string, error) {
	repo, err := s.definitionRepo()
	if err != nil {
		return "", err
	}
	tok, err := s.mintScoped(ctx, []string{repo}, improvementTaskPermissions())
	if err != nil {
		return "", err
	}
	if tok.Value() == "" {
		return "", errors.New("server: github token: empty installation token")
	}
	return tok.Value(), nil
}

func (s *Server) mintPRToken(ctx context.Context) (ghclient.Token, string, error) {
	repo, err := s.definitionRepo()
	if err != nil {
		return ghclient.Token{}, "", err
	}
	tok, err := s.mintScoped(ctx, []string{repo}, improvementPRPermissions())
	if err != nil {
		return ghclient.Token{}, "", err
	}
	return tok, repo, nil
}

func (s *Server) mintScoped(ctx context.Context, repos []string, perms map[string]string) (ghclient.Token, error) {
	if s.integ.githubApp == nil {
		return ghclient.Token{}, errors.New("server: github token: app is not configured")
	}
	if len(repos) == 0 {
		return ghclient.Token{}, errors.New("server: github token: no repository")
	}
	if len(perms) == 0 {
		return ghclient.Token{}, errors.New("server: github token: permissions are required")
	}
	id, err := s.integ.githubApp.InstallationByRepo(ctx, repos[0])
	if err != nil {
		return ghclient.Token{}, fmt.Errorf("server: github token: %w", err)
	}
	tok, err := s.integ.githubApp.InstallationToken(ctx, id, repos, perms)
	if err != nil {
		return ghclient.Token{}, fmt.Errorf("server: github token: %w", err)
	}
	return tok, nil
}

// definitionRepo is owner/name parsed from DefinitionGitURL.
func (s *Server) definitionRepo() (string, error) {
	raw := strings.TrimSpace(s.cfg.DefinitionGitURL)
	if raw == "" {
		return "", errors.New("server: definition git url is empty")
	}
	raw = strings.TrimSuffix(raw, ".git")
	path := raw
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		path = strings.Trim(parsed.Path, "/")
	} else if i := strings.LastIndex(raw, ":"); i >= 0 {
		path = strings.Trim(raw[i+1:], "/")
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[len(parts)-1] == "" || parts[len(parts)-2] == "" {
		return "", errors.New("server: definition git url has no repository")
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}
