package server

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/defsync"
)

var userinfoRe = regexp.MustCompile(`://[^/\s]*@`)

// defsyncLoop polls FACTORY_DEFINITION_GIT_URL. A validation failure leaves
// the last published tree in place; Sync does not replace dest in that case.
func (s *Server) defsyncLoop() {
	defer s.wg.Done()
	cloner := s.cfg.DefinitionGitCloner
	if cloner == nil {
		cloner = gitCloner{}
	}
	p := &defsync.Poller{
		Interval: s.cfg.DefinitionGitInterval,
		Source: defsync.Source{
			URL:           s.cfg.DefinitionGitURL,
			Ref:           s.cfg.DefinitionGitRef,
			Subdir:        s.cfg.DefinitionGitSubdir,
			ProductionRef: s.cfg.DefinitionGitProductionRef,
		},
		Dest:    s.cfg.DefinitionPath,
		Cloner:  cloner,
		Handler: s.onDefinitionSync,
		Wake:    s.integ.defWake,
	}
	err := p.Run(s.ctx)
	if err != nil && s.ctx.Err() == nil {
		s.log.Error("definition sync stopped", "err", s.redact(err.Error()))
	}
}

func (s *Server) onDefinitionSync(sha string, changed bool, diags []definition.Diagnostic, err error) {
	if err != nil {
		s.log.Error("definition sync", "err", s.redact(err.Error()))
		return
	}
	if !changed {
		if len(errorTexts(diags)) > 0 {
			s.log.Warn("definition sync kept last good", "sha", sha)
		}
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	tree, loaded, loadErr := loadTree(s.cfg.DefinitionPath)
	if loadErr != nil || tree == nil || len(errorTexts(loaded)) > 0 {
		s.log.Error("definition sync apply skipped", "sha", sha)
		return
	}
	if _, applyErr := s.applyTree(ctx, tree); applyErr != nil {
		s.log.Error("definition sync apply", "sha", sha, "err", s.redact(applyErr.Error()))
		return
	}
	s.log.Info("definition sync applied", "sha", sha, "factory", tree.Factory.Name)
}

func (s *Server) redact(text string) string {
	for _, value := range s.cfg.Secrets {
		if len(value) < 4 {
			continue
		}
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	for _, value := range []string{
		s.cfg.InferenceAPIKey,
		s.cfg.APIKey,
		s.cfg.WorkerAPIKey,
		s.cfg.DatabaseURL,
		s.cfg.GitHubWebhookSecret,
		s.cfg.SlackBotToken,
		s.cfg.SlackSigningSecret,
		s.cfg.LinearAPIKey,
		s.cfg.LinearWebhookSecret,
		s.cfg.S3.SecretKey,
		s.cfg.S3.AccessKey,
	} {
		if len(value) < 4 {
			continue
		}
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return userinfoRe.ReplaceAllString(text, "://")
}

// gitCloner fetches one ref into an empty directory and returns HEAD.
type gitCloner struct{}

func (gitCloner) Clone(ctx context.Context, url, ref, dest string) (string, error) {
	if err := git(ctx, dest, "init", "-b", "main"); err != nil {
		return "", err
	}
	if err := git(ctx, dest, "fetch", "--depth", "1", url, ref); err != nil {
		return "", err
	}
	if err := git(ctx, dest, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}
	out, err := gitOutput(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", errors.New("defsync: clone returned an empty sha")
	}
	return sha, nil
}

func git(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutput(ctx, dir, args...)
	return err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %w", text, err)
	}
	return string(out), nil
}
