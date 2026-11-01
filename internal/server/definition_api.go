package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/defsync"
)

var _ api.DefinitionPublisher = (*Server)(nil)

// errDefinitionRejected means LoadDir reported an error. The previous tree
// stays in place. The wrapped text is redacted.
var errDefinitionRejected = errors.New("server: definition rejected")

// apiServed is one complete definition snapshot.
type apiServed struct {
	generation int64
	files      []api.DefinitionFile
}

func (s *Server) apiBacked() bool {
	if strings.TrimSpace(s.cfg.DefinitionGitURL) != "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(s.cfg.DefinitionSource), "api")
}

// publishDirectory loads dir and, when it has no errors, applies that tree.
// Git sync and an API push both use it. An invalid tree is not applied.
func (s *Server) publishDirectory(ctx context.Context, dir string) error {
	tree, diags, err := loadTree(dir)
	if err != nil && tree == nil {
		return fmt.Errorf("%w: %s", errDefinitionRejected, definition.RedactInline(s.redact(err.Error())))
	}
	if tree == nil || len(errorTexts(diags)) > 0 {
		msg := strings.Join(errorTexts(diags), "; ")
		if msg == "" {
			msg = "unreadable"
		}
		return fmt.Errorf("%w: %s", errDefinitionRejected, definition.RedactInline(s.redact(msg)))
	}
	_, err = s.applyTreeDir(ctx, tree, dir)
	return err
}

// PublishDefinition implements api.DefinitionPublisher.
func (s *Server) PublishDefinition(ctx context.Context, name string, req api.DefinitionPushRequest) (api.DefinitionPushResponse, error) {
	var zero api.DefinitionPushResponse
	if !s.apiBacked() {
		return zero, api.ErrGitBackedDefinition
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	serving := s.publishedGeneration()
	if req.Generation <= serving {
		return api.DefinitionPushResponse{
			Valid:               serving > 0,
			Errors:              []api.Diagnostic{},
			PublishedGeneration: serving,
		}, nil
	}

	parent := filepath.Dir(s.cfg.DefinitionPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return zero, err
	}
	temp, err := os.MkdirTemp(parent, ".api-stage-*")
	if err != nil {
		return zero, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := materializeDefinition(temp, req.Files); err != nil {
		return zero, err
	}
	tree, diags, loadErr := loadTree(temp)
	if loadErr != nil && tree == nil {
		return s.invalidPush(serving, diags), nil
	}
	if tree != nil && tree.Factory.Name != name {
		diags = append(diags, definition.Diagnostic{
			File:     "factory.yaml",
			Line:     1,
			Severity: definition.SeverityError,
			Message:  fmt.Sprintf("factory name %q does not match %q", tree.Factory.Name, name),
		})
	}
	if tree == nil || len(errorTexts(diags)) > 0 {
		return s.invalidPush(serving, diags), nil
	}
	files, err := definitionFiles(temp)
	if err != nil {
		return zero, err
	}
	if err := s.publishDirectory(ctx, temp); err != nil {
		return zero, err
	}
	if err := defsync.Publish(temp, s.cfg.DefinitionPath); err != nil {
		return zero, err
	}
	if err := writeAPIGeneration(s.cfg.DefinitionPath, req.Generation); err != nil {
		return zero, err
	}
	if s.beforeAPISwap != nil {
		s.beforeAPISwap()
	}
	s.pointLoadedAt(s.cfg.DefinitionPath, temp)
	s.setAPIServed(req.Generation, files)
	return api.DefinitionPushResponse{
		Valid:               true,
		Errors:              []api.Diagnostic{},
		PublishedGeneration: req.Generation,
	}, nil
}

func (s *Server) pointLoadedAt(dest, temp string) {
	s.mu.Lock()
	old := s.loaded.dir
	s.loaded.dir = dest
	s.mu.Unlock()
	if old != "" && old != dest && old != temp {
		_ = os.RemoveAll(old)
	}
}

func (s *Server) invalidPush(serving int64, diags []definition.Diagnostic) api.DefinitionPushResponse {
	return api.DefinitionPushResponse{
		Valid:               false,
		Errors:              s.redactDiags(diags),
		PublishedGeneration: serving,
	}
}

func (s *Server) redactDiags(diags []definition.Diagnostic) []api.Diagnostic {
	out := make([]api.Diagnostic, 0)
	for _, diag := range diags {
		if diag.Severity != definition.SeverityError {
			continue
		}
		out = append(out, api.Diagnostic{
			File:     definition.RedactInline(s.redact(diag.File)),
			Line:     diag.Line,
			Severity: diag.Severity,
			Message:  definition.RedactInline(s.redact(diag.Message)),
		})
	}
	return out
}

func (s *Server) publishedGeneration() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apiServed == nil {
		return 0
	}
	return s.apiServed.generation
}

func (s *Server) setAPIServed(generation int64, files []api.DefinitionFile) {
	cp := make([]api.DefinitionFile, len(files))
	copy(cp, files)
	s.mu.Lock()
	s.apiServed = &apiServed{generation: generation, files: cp}
	s.mu.Unlock()
}

func (s *Server) apiDefinitionFiles() []api.DefinitionFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apiServed == nil || len(s.apiServed.files) == 0 {
		return []api.DefinitionFile{}
	}
	out := make([]api.DefinitionFile, len(s.apiServed.files))
	copy(out, s.apiServed.files)
	return out
}

func materializeDefinition(dir string, files []api.DefinitionFile) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		rel := filepath.Clean(filepath.FromSlash(file.Path))
		if rel == "." || rel == "" || filepath.IsAbs(file.Path) || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("server: definition path is invalid")
		}
		target := filepath.Join(root, rel)
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("server: definition path escapes")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(file.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readAPIGeneration(dest string) int64 {
	body, err := os.ReadFile(dest + ".api-generation")
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func writeAPIGeneration(dest string, n int64) error {
	path := dest + ".api-generation"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(n, 10)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
