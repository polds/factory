package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/chat/slack"
	"github.com/polds/factory/internal/definition"
	ghclient "github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/ingress/sig"
	"github.com/polds/factory/internal/leader"
	"github.com/polds/factory/internal/outbound"
	"github.com/polds/factory/internal/store"
	"github.com/polds/factory/internal/tracker/linear"
	"github.com/polds/factory/internal/webhooks"
)

func normalizeTokenMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", tokenModePAT:
		return tokenModePAT, nil
	case tokenModeApp:
		return tokenModeApp, nil
	default:
		return "", errors.New("server: FACTORY_GITHUB_TOKEN_MODE must be app or pat")
	}
}

// wire builds ingress, the automation engine, and outbound clients.
// The definition is applied after this returns, which sets FactoryID and Allow.
func (s *Server) wire() error {
	holder, err := newHolder()
	if err != nil {
		return err
	}
	s.integ.holder = holder
	s.integ.dispatchWake = make(chan struct{}, 1)
	s.integ.defWake = make(chan struct{}, 1)
	s.integ.started = map[string]struct{}{}
	s.integ.githubBase = strings.TrimRight(strings.TrimSpace(s.cfg.GitHubAPIBase), "/")

	s.integ.engine = &automation.Engine{
		Store:  &store.Automation{Store: s.store, Log: s.log},
		Answer: resumingAnswerer{s},
		Tree:   s.definitionTree,
		Now:    s.now,
	}
	s.integ.dispatcher = &ingress.Dispatcher{
		Store:  ingressStore{s},
		Handle: s.handleDelivery,
	}

	app, err := s.githubClient()
	if err != nil {
		return err
	}
	s.integ.githubApp = app

	notifier, err := s.notifier(app)
	if err != nil {
		return err
	}
	s.integ.notifier = notifier

	if err := s.mountIngress(); err != nil {
		return err
	}
	s.integ.webhooks = webhooks.Handler(webhooks.Config{
		Lookup:  s.store,
		Secrets: secretSource{s},
		Sink:    webhookSink{s},
		Now:     s.now,
		Log:     s.log,
	})
	s.log.Info("integrations",
		"github_ingress", s.integ.ingressGitHub != nil,
		"github_app", s.integ.githubApp != nil,
		"github_token_mode", s.cfg.GitHubTokenMode,
		"slack_ingress", s.integ.ingressSlack != nil,
		"slack_poster", s.cfg.SlackBotToken != "",
		"linear_ingress", s.integ.ingressLinear != nil,
		"linear_client", s.cfg.LinearAPIKey != "",
	)
	return nil
}

func newHolder() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("server: holder: %w", err)
	}
	return "factoryd-" + hex.EncodeToString(buf[:]), nil
}

func (s *Server) githubClient() (ghclient.App, error) {
	if s.cfg.GitHubAppID <= 0 || s.cfg.GitHubAppKey == nil {
		return nil, nil
	}
	return ghclient.New(ghclient.Config{
		AppID:   s.cfg.GitHubAppID,
		Key:     s.cfg.GitHubAppKey,
		BaseURL: s.cfg.GitHubAPIBase,
		HTTP:    s.cfg.HTTPClient,
		Now:     s.now,
		Log:     s.log,
	})
}

func (s *Server) notifier(app ghclient.App) (*outbound.Notifier, error) {
	cfg := outbound.Config{
		ExternalURL: s.cfg.ExternalURL,
		GitHub:      app,
		Threads:     s.store,
		Refs:        s.store,
		Now:         s.now,
		Sleep:       s.cfg.OutboundSleep,
		Log:         s.log,
	}
	if s.cfg.SlackBotToken != "" {
		opts := []slack.Option{}
		if s.cfg.SlackAPIBase != "" {
			opts = append(opts, slack.WithBaseURL(s.cfg.SlackAPIBase))
		}
		if s.cfg.HTTPClient != nil {
			opts = append(opts, slack.WithHTTPClient(s.cfg.HTTPClient))
		}
		cfg.Slack = slack.New(s.cfg.SlackBotToken, opts...)
	}
	if s.cfg.LinearAPIKey != "" {
		client := linear.New(s.cfg.LinearAPIBase, s.cfg.LinearAPIKey)
		client.HTTP = s.cfg.HTTPClient
		cfg.Linear = client
	}
	return outbound.New(cfg)
}

func (s *Server) mountIngress() error {
	now := s.now
	if secret := s.cfg.GitHubWebhookSecret; secret != "" {
		v, err := sig.Open("github", secret)
		if err != nil {
			return fmt.Errorf("server: github ingress: %w", err)
		}
		s.integ.ingressGitHub = ingress.New(ingress.Config{
			Provider: ingress.ProviderGitHub,
			Verifier: v,
			Sink:     ingressSink{s},
			Now:      now,
		})
	}
	if secret := s.cfg.SlackSigningSecret; secret != "" {
		v, err := sig.Open("slack", secret)
		if err != nil {
			return fmt.Errorf("server: slack ingress: %w", err)
		}
		s.integ.ingressSlack = ingress.New(ingress.Config{
			Provider: ingress.ProviderSlack,
			Verifier: v,
			Sink:     ingressSink{s},
			Now:      now,
		})
	}
	if secret := s.cfg.LinearWebhookSecret; secret != "" {
		v, err := sig.Open("linear", secret)
		if err != nil {
			return fmt.Errorf("server: linear ingress: %w", err)
		}
		s.integ.ingressLinear = ingress.New(ingress.Config{
			Provider: ingress.ProviderLinear,
			Verifier: v,
			Sink:     ingressSink{s},
			Now:      now,
		})
	}
	return nil
}

func (s *Server) closeOutbound() {
	if s.integ.notifier != nil {
		_ = s.integ.notifier.Close()
	}
}

func (s *Server) definitionTree() definition.Tree {
	snap := s.snapshot()
	if snap.tree == nil {
		return definition.Tree{}
	}
	return *snap.tree
}

func (s *Server) dispatchLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(dispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.integ.dispatchWake:
			s.pollDeliveries()
		case <-ticker.C:
			s.pollDeliveries()
		}
	}
}

func (s *Server) pollDeliveries() {
	s.integ.mu.Lock()
	d := s.integ.dispatcher
	s.integ.mu.Unlock()
	if d == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if _, err := d.Poll(ctx); err != nil && s.ctx.Err() == nil {
		s.log.Error("dispatch", "err", s.redact(err.Error()))
	}
}

func (s *Server) pokeDispatch() {
	if s.integ.dispatchWake == nil {
		return
	}
	select {
	case s.integ.dispatchWake <- struct{}{}:
	default:
	}
}

func (s *Server) pokeDef() {
	if s.integ.defWake == nil {
		return
	}
	select {
	case s.integ.defWake <- struct{}{}:
	default:
	}
}

func (s *Server) cronLoop() {
	defer s.wg.Done()
	el := &leader.Elector{
		Store:  s.store,
		Name:   cronLeaseName,
		Holder: s.integ.holder,
		TTL:    leader.LeaseTTL,
		Renew:  leader.RenewInterval,
		Now:    s.now,
		Tick:   s.cronTick,
		OnError: func(err error) {
			if s.ctx.Err() == nil {
				s.log.Error("cron", "err", s.redact(err.Error()))
			}
		},
	}
	if err := el.Run(s.ctx); err != nil && s.ctx.Err() == nil {
		s.log.Error("cron stopped", "err", s.redact(err.Error()))
	}
}

func (s *Server) cronTick(ctx context.Context, now time.Time) error {
	if err := (cronDB{s}).tick(ctx, now); err != nil {
		return err
	}
	if _, err := s.store.PurgeExpiredDeliveries(ctx, now); err != nil {
		return err
	}
	return nil
}

// githubCollaborator reports whether handle is a collaborator on owner/name.
// A nil app denies every handle. 404 is a denial, not a transport error.
func (s *Server) githubCollaborator(ctx context.Context, owner, name, handle string) (bool, error) {
	if s.integ.githubApp == nil {
		return false, nil
	}
	repo := owner + "/" + name
	id, err := s.integ.githubApp.InstallationByRepo(ctx, repo)
	if err != nil {
		return false, err
	}
	tok, err := s.integ.githubApp.InstallationToken(ctx, id, []string{repo}, map[string]string{"metadata": "read"})
	if err != nil {
		return false, err
	}
	base := s.integ.githubBase
	if base == "" {
		base = "https://api.github.com"
	}
	endpoint := base + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/collaborators/" + url.PathEscape(handle)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
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
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	switch res.StatusCode {
	case http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("server: github collaborator: HTTP %d", res.StatusCode)
	}
}

func (s *Server) mintGitHubToken(ctx context.Context, run store.Run) (string, error) {
	if s.integ.githubApp == nil {
		return "", errors.New("server: github token: app is not configured")
	}
	repos := s.tokenRepos(ctx, run)
	if len(repos) == 0 {
		return "", errors.New("server: github token: no repository")
	}
	id, err := s.integ.githubApp.InstallationByRepo(ctx, repos[0])
	if err != nil {
		return "", fmt.Errorf("server: github token: %w", err)
	}
	tok, err := s.integ.githubApp.InstallationToken(ctx, id, repos, ghclient.DefaultPermissions())
	if err != nil {
		return "", fmt.Errorf("server: github token: %w", err)
	}
	if tok.Value() == "" {
		return "", errors.New("server: github token: empty installation token")
	}
	return tok.Value(), nil
}

func (s *Server) tokenRepos(ctx context.Context, run store.Run) []string {
	var repos []string
	seen := map[string]struct{}{}
	add := func(repo string) {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return
		}
		if _, ok := seen[repo]; ok {
			return
		}
		seen[repo] = struct{}{}
		repos = append(repos, repo)
	}
	if auto, ok := s.intakeAutomation(ctx, run.ID); ok {
		for _, repo := range auto.RequiredRepos {
			if repo.Owner == "" || repo.Name == "" {
				continue
			}
			add(repo.Owner + "/" + repo.Name)
		}
	}
	if run.WorkItemID != "" {
		item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
		if err == nil {
			if repo, _, ok := parseIssueRef(item.SourceRef); ok {
				add(repo)
			}
		}
	}
	return repos
}

// secretSource resolves webhook credential names. The error names the
// credential and never includes its value.
type secretSource struct{ s *Server }

func (a secretSource) Secret(_ context.Context, name string) (string, error) {
	value, ok := a.s.cfg.Secrets[name]
	if !ok || value == "" {
		return "", fmt.Errorf("server: secret %s is not configured", name)
	}
	return value, nil
}
