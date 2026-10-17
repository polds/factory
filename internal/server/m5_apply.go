package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

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
// The factory name must match. After the store commit, the files are
// installed into DefinitionPath so the next applyBoot reloads this tree.
// A syncDefinition error does not delete the directory s.loaded.dir still references.
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
		// applyTreeDir publishes dir before syncDefinition. Deleting it
		// here would make the file listing point at a removed directory.
		if dir != "" && s.snapshot().dir != dir {
			_ = os.RemoveAll(dir)
		}
		return api.DefinitionRef{}, toDiagnostics(diags), err
	}
	if err := installDefinitionDir(dir, s.cfg.DefinitionPath); err != nil {
		return api.DefinitionRef{}, toDiagnostics(diags), err
	}
	if dir != s.cfg.DefinitionPath {
		s.mu.Lock()
		s.loaded.dir = s.cfg.DefinitionPath
		s.mu.Unlock()
		_ = os.RemoveAll(dir)
	}
	return api.DefinitionRef{ID: id}, toDiagnostics(diags), nil
}

// installDefinitionDir replaces dest with a complete copy of src. The copy
// is written to a sibling temp directory and renamed into place, so a crash
// leaves the previous tree or the new one. A mount point cannot be renamed;
// that case replaces the files inside dest from the completed temp tree.
func installDefinitionDir(src, dest string) error {
	if src == "" || dest == "" || src == dest {
		return nil
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("server: install definition: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".definition-")
	if err != nil {
		return fmt.Errorf("server: install definition: %w", err)
	}
	if err := copyDefinitionTree(src, staging); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("server: install definition: %w", err)
	}
	backup := filepath.Join(parent, "."+filepath.Base(dest)+".prev")
	_ = os.RemoveAll(backup)
	if err := os.Rename(dest, backup); err != nil {
		if os.IsNotExist(err) {
			return moveStaging(staging, dest)
		}
		if definitionDirIsMounted(err) {
			return replaceMountedDefinition(staging, dest)
		}
		_ = os.RemoveAll(staging)
		return fmt.Errorf("server: install definition: %w", err)
	}
	if err := os.Rename(staging, dest); err != nil {
		_ = os.Rename(backup, dest)
		_ = os.RemoveAll(staging)
		return fmt.Errorf("server: install definition: %w", err)
	}
	_ = os.RemoveAll(backup)
	return nil
}

func definitionDirIsMounted(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV)
}

func moveStaging(staging, dest string) error {
	if err := os.Rename(staging, dest); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("server: install definition: %w", err)
	}
	return nil
}

func replaceMountedDefinition(staging, dest string) error {
	err := replaceDefinitionTree(staging, dest)
	_ = os.RemoveAll(staging)
	if err != nil {
		return fmt.Errorf("server: install definition: %w", err)
	}
	return nil
}

func copyDefinitionTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("server: install definition: %s escapes the tree", rel)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyDefinitionFile(path, target)
	})
}

func copyDefinitionFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func replaceDefinitionTree(staging, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	keep := map[string]struct{}{}
	err := filepath.WalkDir(staging, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(staging, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("server: install definition: %s escapes the tree", rel)
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := placeDefinitionFile(path, target); err != nil {
			return err
		}
		keep[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	var extra []string
	err = filepath.WalkDir(dest, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := keep[rel]; !ok {
			extra = append(extra, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range extra {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func placeDefinitionFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
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
