package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/polds/factory/internal/definition"
	ghclient "github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/secrets"
	"github.com/polds/factory/internal/store"
)

// secretRedactors keeps rotation needles per process. Server has no field
// for a redactor; server.go is outside this change.
var secretRedactors sync.Map

type checkoutOverrideKey struct{}

func withCheckoutOverrides(ctx context.Context, overrides []protocol.RepositoryHeadOverride) context.Context {
	return context.WithValue(ctx, checkoutOverrideKey{}, overrides)
}

func (s *Server) secretRedactor() *secrets.Redactor {
	v, _ := secretRedactors.LoadOrStore(s, secrets.NewRedactor())
	redactor, _ := v.(*secrets.Redactor)
	if redactor == nil {
		return secrets.NewRedactor()
	}
	return redactor
}

func (s *Server) redactionValues(values []string) []string {
	out := append([]string{}, values...)
	return append(out, s.secretRedactor().Needles()...)
}

func (s *Server) resolveFactorySecret(ctx context.Context, factoryID, name string) (string, error) {
	if grants, ok := s.cfg.FactorySecrets[factoryID]; ok {
		value := grants[name]
		if value == "" {
			return "", fmt.Errorf("server: secret %s is not configured", name)
		}
		s.secretRedactor().Observe(name, value)
		return value, nil
	}
	if len(s.cfg.FactorySecrets) > 0 {
		return "", fmt.Errorf("server: secret %s is not configured", name)
	}
	return s.resolveInjectedSecret(ctx, name)
}

func (s *Server) resolveInjectedSecret(ctx context.Context, name string) (string, error) {
	res, err := s.resolverFrom(os.Getenv)
	if err != nil {
		return "", err
	}
	return s.resolveWith(ctx, res, name)
}

func (s *Server) resolveWith(ctx context.Context, res secrets.Resolver, name string) (string, error) {
	if res == nil {
		return "", fmt.Errorf("server: secret %s is not configured", name)
	}
	id, err := injectionID()
	if err != nil {
		return "", err
	}
	handle, err := res.Resolve(ctx, name, id)
	if err != nil {
		return "", err
	}
	if handle.Value() == "" {
		return "", fmt.Errorf("server: secret %s is not configured", name)
	}
	s.secretRedactor().Observe(handle.Name(), handle.Value())
	return handle.Value(), nil
}

func (s *Server) resolverFrom(getenv func(string) string) (secrets.Resolver, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	settings := ParseSecretMountSettings(getenv)
	if settings.MountDir != "" || len(settings.Allowlist) > 0 {
		return settings.BuildResolver(s.cfg.Secrets)
	}
	if len(s.cfg.Secrets) == 0 {
		return nil, nil
	}
	allow := make([]string, 0, len(s.cfg.Secrets))
	for name := range s.cfg.Secrets {
		allow = append(allow, name)
	}
	sort.Strings(allow)
	return secrets.ResolverFromConfig(secrets.MountConfig{
		Allowlist: allow,
		Env:       s.cfg.Secrets,
	})
}

func injectionID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("server: injection id: %w", err)
	}
	return "inj_" + hex.EncodeToString(raw[:]), nil
}

func (s *Server) omitAmbientInference(run store.Run, bound boundAgent) bool {
	return ambientHarness(bound.agent.Harness) || s.readOnlyForgeTarget(run)
}

func (s *Server) readOnlyForgeTarget(run store.Run) bool {
	if run.AgentID == "" {
		return false
	}
	bound, ok := s.snapshot().byID[run.AgentID]
	if !ok {
		return false
	}
	return readOnlyHarness(bound.agent.Harness)
}

func readOnlyHarness(h *definition.Harness) bool {
	if h == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(h.Type)) {
	case "scanner", "native":
		return true
	default:
		return false
	}
}

func ambientHarness(h *definition.Harness) bool {
	if h == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(h.Type)) {
	case "scanner", "native", "claude-code":
		return true
	default:
		return false
	}
}

func applyUntrustedConstraints(asg protocol.TaskAssignment) protocol.TaskAssignment {
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil {
		return asg
	}
	c := harness.TaskConstraints(true)
	asg.Task.AgentConfigSnapshot.NativeConstraints = &protocol.NativeRunConstraints{
		DockerSocket:         c.DockerSocket,
		ForeignCheckoutWrite: c.ForeignCheckoutWrite,
		BroadSecretMount:     c.BroadSecretMount,
		NegotiatedSteer:      false,
		ReadOnlyForge:        true,
	}
	return asg
}

func (s *Server) checkoutOverrides(ctx context.Context, workItemID string) ([]protocol.RepositoryHeadOverride, error) {
	if v, ok := ctx.Value(checkoutOverrideKey{}).([]protocol.RepositoryHeadOverride); ok {
		return v, nil
	}
	return s.invocationCheckoutOverrides(ctx, workItemID)
}

func (s *Server) installToken(ctx context.Context, id int64, repos []string, run store.Run, bound boundAgent, writeBundle func() map[string]string) (ghclient.Token, error) {
	if s.mustReadMint(run, bound) {
		return s.mintScannerToken(ctx, id, repos, run)
	}
	return s.integ.githubApp.InstallationToken(ctx, id, repos, writeBundle())
}

func (s *Server) mustReadMint(run store.Run, bound boundAgent) bool {
	return s.readOnlyForgeTarget(run) || readOnlyHarness(bound.agent.Harness)
}

func (s *Server) mintScannerToken(ctx context.Context, id int64, repos []string, run store.Run) (ghclient.Token, error) {
	overrides, err := s.checkoutOverrides(ctx, run.WorkItemID)
	if err != nil {
		return ghclient.Token{}, err
	}
	mint, err := prepareReadMint(repos, overrides)
	if err != nil {
		return ghclient.Token{}, err
	}
	mint.InstallationID = id
	if c, ok := s.integ.githubApp.(*ghclient.Client); ok {
		read, err := c.MintReadToken(ctx, mint)
		if err != nil {
			return ghclient.Token{}, err
		}
		return read.Token(), nil
	}
	return s.integ.githubApp.InstallationToken(ctx, id, []string{mint.Repo}, ghclient.ReadPermissions())
}

func prepareReadMint(repos []string, overrides []protocol.RepositoryHeadOverride) (ghclient.ReadMint, error) {
	if len(repos) != 1 {
		return ghclient.ReadMint{}, errors.New("server: github token: scanner mint requires one repository")
	}
	var pin, repo string
	for _, ov := range overrides {
		if ov.Head.Type != protocol.RepositoryHeadTypeCommitSHA {
			continue
		}
		if !isCommitSHA(ov.Head.Value) {
			return ghclient.ReadMint{}, errors.New("server: github token: checkout is not the pinned commit SHA")
		}
		if pin != "" {
			return ghclient.ReadMint{}, errors.New("server: github token: multiple pinned checkouts")
		}
		pin = ov.Head.Value
		repo = ov.RepoOwner + "/" + ov.RepoName
	}
	if pin == "" || repo == "" {
		return ghclient.ReadMint{}, errors.New("server: github token: checkout is not pinned to a commit SHA")
	}
	if repos[0] != repo {
		return ghclient.ReadMint{}, fmt.Errorf("server: github token: unrelated repository %s", repos[0])
	}
	return ghclient.ReadMint{
		Repo:         repo,
		AllowedRepos: []string{repo},
		PinnedSHA:    pin,
		CheckoutSHA:  pin,
	}, nil
}
