package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/defsync"
)

type m6Clone struct {
	sha string
	dir string
}

func (s *Server) cloner() defsync.Cloner {
	if s.cfg.DefinitionGitCloner != nil {
		return s.cfg.DefinitionGitCloner
	}
	return gitCloner{}
}

func (s *Server) productionRef() string {
	if ref := strings.TrimSpace(s.cfg.DefinitionGitProductionRef); ref != "" {
		return ref
	}
	if ref := strings.TrimSpace(s.cfg.DefinitionGitRef); ref != "" {
		return ref
	}
	return "main"
}

// cloneRef fetches one definition ref. A 40-hex SHA is reused until Close.
// A ref name is fetched every time, because a later push must be visible
// to the next benchmark run without a process restart.
func (s *Server) cloneRef(ctx context.Context, ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", errors.New("server: definition ref is empty")
	}
	if strings.TrimSpace(s.cfg.DefinitionGitURL) == "" {
		return "", "", errors.New("server: definition git url is empty")
	}
	if sha, ok := gitSHA(ref); ok {
		if hit, ok := s.cachedClone(sha); ok {
			return hit.sha, hit.dir, nil
		}
	}
	dir, err := os.MkdirTemp("", "factory-m6-")
	if err != nil {
		return "", "", fmt.Errorf("server: clone: %w", err)
	}
	sha, err := s.cloner().Clone(ctx, s.cfg.DefinitionGitURL, ref, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		redacted := s.redact(err.Error())
		if s.log != nil {
			s.log.Error("clone", "err", redacted)
		}
		return "", "", fmt.Errorf("server: clone %s: %s", ref, redacted)
	}
	sha = strings.TrimSpace(sha)
	if canonical, ok := gitSHA(sha); ok {
		sha = canonical
	}
	s.m6CloneMu.Lock()
	if s.m6Clones == nil {
		s.m6Clones = map[string]m6Clone{}
	}
	if prev, ok := s.m6Clones[sha]; ok {
		s.m6CloneMu.Unlock()
		_ = os.RemoveAll(dir)
		return prev.sha, prev.dir, nil
	}
	s.m6Clones[sha] = m6Clone{sha: sha, dir: dir}
	s.m6CloneMu.Unlock()
	return sha, dir, nil
}

func (s *Server) cachedClone(sha string) (m6Clone, bool) {
	s.m6CloneMu.Lock()
	defer s.m6CloneMu.Unlock()
	if s.m6Clones == nil {
		return m6Clone{}, false
	}
	hit, ok := s.m6Clones[sha]
	return hit, ok
}

// gitSHA reports whether ref is a 40-hex commit. The returned form is lower
// case so cache lookups do not depend on the remote's hex case.
func gitSHA(ref string) (string, bool) {
	if len(ref) != 40 {
		return "", false
	}
	for _, c := range ref {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return "", false
		}
	}
	return strings.ToLower(ref), true
}

func (s *Server) clearClones() {
	s.m6CloneMu.Lock()
	defer s.m6CloneMu.Unlock()
	for _, row := range s.m6Clones {
		_ = os.RemoveAll(row.dir)
	}
	s.m6Clones = nil
}

// resolveDefinitionSHA clones ref once. An empty ref is the production ref.
// A factory with no git URL returns an empty SHA and no error; assignment
// then uses the published tree.
func (s *Server) resolveDefinitionSHA(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = s.productionRef()
	}
	if strings.TrimSpace(s.cfg.DefinitionGitURL) == "" {
		return "", nil
	}
	sha, _, err := s.cloneRef(ctx, ref)
	return sha, err
}

// treeForSHA loads the definition at a resolved SHA. An empty SHA, or a
// factory with no git URL, returns the published snapshot.
func (s *Server) treeForSHA(ctx context.Context, sha string) (*definition.Tree, string, error) {
	sha = strings.TrimSpace(sha)
	snap := s.snapshot()
	if sha == "" || strings.TrimSpace(s.cfg.DefinitionGitURL) == "" {
		return snap.tree, snap.dir, nil
	}
	got, dir, err := s.cloneRef(ctx, sha)
	if err != nil {
		return nil, "", err
	}
	tree, diags := definition.LoadDir(dir)
	if msgs := errorTexts(diags); len(msgs) > 0 {
		return tree, dir, fmt.Errorf("server: definition %s: %s", got, strings.Join(msgs, "; "))
	}
	return tree, dir, nil
}

// headRef converts one docs/03 repo ref into the object wire form.
func headRef(r definition.RepoRef) api.HeadRef {
	return api.HeadRef{Host: r.Host, Owner: r.Owner, Repo: r.Repo, SHA: r.SHA}
}

// changedDefinitionFiles counts files whose contents differ between base and
// head. .git is skipped. The count is the blast-radius input to improve.Route.
func changedDefinitionFiles(baseDir, headDir string) (int, error) {
	paths, err := changedDefinitionPaths(baseDir, headDir)
	return len(paths), err
}

// changedDefinitionPaths lists files whose contents differ between base and
// head. .git is skipped. The layer and the blast radius share this walk.
func changedDefinitionPaths(baseDir, headDir string) ([]string, error) {
	base, err := fileBodies(baseDir)
	if err != nil {
		return nil, err
	}
	head, err := fileBodies(headDir)
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := map[string]bool{}
	for path, body := range head {
		seen[path] = true
		if base[path] != body {
			paths = append(paths, path)
		}
	}
	for path := range base {
		if !seen[path] {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// improvementLayer is skill when every changed path has a skills segment,
// including agents/<name>/skills/.... Any other path, or an empty diff, stays
// definition.
func improvementLayer(baseDir, headDir string) string {
	paths, err := changedDefinitionPaths(baseDir, headDir)
	if err != nil || len(paths) == 0 {
		return "definition"
	}
	for _, path := range paths {
		if !skillPath(path) {
			return "definition"
		}
	}
	return "skill"
}

func skillPath(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == "skills" {
			return true
		}
	}
	return false
}

func fileBodies(root string) (map[string]string, error) {
	out := map[string]string{}
	if root == "" {
		return out, nil
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		out[rel] = string(body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("server: definition files: %w", err)
	}
	return out, nil
}

func listDefinitionFiles(root string) string {
	bodies, err := fileBodies(root)
	if err != nil {
		return ""
	}
	var names []string
	for path := range bodies {
		names = append(names, path)
	}
	sortStrings(names)
	return strings.Join(names, "\n")
}
