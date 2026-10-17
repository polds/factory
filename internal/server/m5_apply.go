package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
)

type uploadedTree struct {
	Files []uploadedFile `json:"files"`
}

type uploadedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ValidateDefinition keeps today's behaviour for an empty body: load
// cfg.DefinitionPath and discard the reader. A non-empty body is
// {"files":[{"path","content"}]}. Those files are written to a temp directory
// and validated with definition.LoadDir. Absolute paths, "..", duplicates,
// and non-regular entries are rejected. Diagnostics use relative paths.
func (s *Server) ValidateDefinition(ctx context.Context, factoryID string, tarball io.Reader) ([]api.Diagnostic, error) {
	body, err := readTreeBody(tarball)
	if err != nil {
		return nil, err
	}
	if _, err := s.factory(ctx, factoryID); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		_, diags, err := loadTree(s.cfg.DefinitionPath)
		if len(diags) > 0 {
			return toDiagnostics(diags), nil
		}
		if err != nil {
			return nil, err
		}
		return []api.Diagnostic{}, nil
	}
	_, diags, dir, err := materializeTree(body)
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	if err != nil && len(diags) == 0 {
		return nil, err
	}
	return toDiagnostics(diags), nil
}

// ApplyDefinitionTree validates an uploaded tree and applies it in one
// store.ApplyDefinition transaction. Validation errors do not apply.
// The factory name must match. The temp directory becomes the loaded tree
// until the next apply replaces it.
func (s *Server) ApplyDefinitionTree(ctx context.Context, factoryID string, tree io.Reader) (api.DefinitionRef, []api.Diagnostic, error) {
	body, err := readTreeBody(tree)
	if err != nil {
		return api.DefinitionRef{}, nil, err
	}
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return api.DefinitionRef{}, nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return s.ApplyDefinition(ctx, factoryID)
	}
	loaded, diags, dir, err := materializeTree(body)
	if loaded == nil || len(errorTexts(diags)) > 0 {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		if err != nil && len(diags) == 0 {
			return api.DefinitionRef{}, nil, err
		}
		return api.DefinitionRef{}, toDiagnostics(diags), nil
	}
	if loaded.Factory.Name != fac.Name {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return api.DefinitionRef{}, []api.Diagnostic{{
			File:     "factory.yaml",
			Line:     1,
			Severity: definition.SeverityError,
			Message:  "definition name does not match factory",
		}}, nil
	}
	id, err := s.applyTreeDir(ctx, loaded, dir)
	if err != nil {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return api.DefinitionRef{}, toDiagnostics(diags), err
	}
	return api.DefinitionRef{ID: id}, toDiagnostics(diags), nil
}

func parseUploadedTree(body []byte) (uploadedTree, bool) {
	var uploaded uploadedTree
	if json.Unmarshal(body, &uploaded) != nil {
		return uploadedTree{}, false
	}
	return uploaded, true
}

func readTreeBody(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}

func materializeTree(body []byte) (*definition.Tree, []definition.Diagnostic, string, error) {
	uploaded, ok := parseUploadedTree(body)
	if !ok {
		return nil, []definition.Diagnostic{{
			File:     "files",
			Line:     1,
			Severity: definition.SeverityError,
			Message:  "definition body must be a files object",
		}}, "", nil
	}
	if len(uploaded.Files) == 0 {
		return nil, []definition.Diagnostic{{
			File:     "files",
			Line:     1,
			Severity: definition.SeverityError,
			Message:  "files is required",
		}}, "", nil
	}
	seen := map[string]struct{}{}
	var diags []definition.Diagnostic
	cleaned := make([]uploadedFile, 0, len(uploaded.Files))
	for _, file := range uploaded.Files {
		rel, ok := cleanUploadPath(file.Path)
		if !ok {
			diags = append(diags, definition.Diagnostic{
				File:     displayPath(file.Path),
				Line:     1,
				Severity: definition.SeverityError,
				Message:  "path must be a relative file",
			})
			continue
		}
		if _, dup := seen[rel]; dup {
			diags = append(diags, definition.Diagnostic{
				File:     rel,
				Line:     1,
				Severity: definition.SeverityError,
				Message:  "duplicate path",
			})
			continue
		}
		seen[rel] = struct{}{}
		file.Path = rel
		cleaned = append(cleaned, file)
	}
	if len(diags) > 0 {
		return nil, diags, "", nil
	}
	dir, err := os.MkdirTemp("", "factory-definition-")
	if err != nil {
		return nil, nil, "", err
	}
	for _, file := range cleaned {
		// IsLocal is the barrier on the uploaded path. cleanUploadPath already
		// rejected escapes; this check is what the write is guarded on.
		rel := file.Path
		if !filepath.IsLocal(rel) {
			_ = os.RemoveAll(dir)
			return nil, nil, "", errDefinitionPath
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = os.RemoveAll(dir)
			return nil, nil, "", err
		}
		if err := os.WriteFile(dst, []byte(file.Content), 0o644); err != nil {
			_ = os.RemoveAll(dir)
			return nil, nil, "", err
		}
	}
	tree, loadDiags := definition.LoadDir(dir)
	return tree, loadDiags, dir, nil
}

func cleanUploadPath(raw string) (string, bool) {
	if raw == "" || strings.HasSuffix(raw, "/") || strings.Contains(raw, "\\") {
		return "", false
	}
	if path.IsAbs(raw) || filepath.IsAbs(raw) {
		return "", false
	}
	cleaned := path.Clean(raw)
	if !filepath.IsLocal(cleaned) {
		return "", false
	}
	return cleaned, true
}

var errDefinitionPath = errors.New("server: definition path is not local")

func displayPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "files"
	}
	cleaned := path.Clean(strings.TrimPrefix(raw, "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "files"
	}
	return cleaned
}
