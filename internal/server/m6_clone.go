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

// cloneRef fetches one definition ref and caches the directory until Close.
// The cache key is the git URL plus the ref. A repeated assign during one
// process reuses the tree.
func (s *Server) cloneRef(ctx context.Context, ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", errors.New("server: definition ref is empty")
	}
	if strings.TrimSpace(s.cfg.DefinitionGitURL) == "" {
		return "", "", errors.New("server: definition git url is empty")
	}
	key := s.cfg.DefinitionGitURL + "\n" + ref
	s.m6CloneMu.Lock()
	if s.m6Clones != nil {
		if hit, ok := s.m6Clones[key]; ok {
			s.m6CloneMu.Unlock()
			return hit.sha, hit.dir, nil
		}
	}
	s.m6CloneMu.Unlock()

	dir, err := os.MkdirTemp("", "factory-m6-")
	if err != nil {
		return "", "", fmt.Errorf("server: clone: %w", err)
	}
	sha, err := s.cloner().Clone(ctx, s.cfg.DefinitionGitURL, ref, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("server: clone %s: %w", ref, err)
	}
	sha = strings.TrimSpace(sha)
	s.m6CloneMu.Lock()
	if s.m6Clones == nil {
		s.m6Clones = map[string]m6Clone{}
	}
	if prev, ok := s.m6Clones[key]; ok {
		s.m6CloneMu.Unlock()
		_ = os.RemoveAll(dir)
		return prev.sha, prev.dir, nil
	}
	s.m6Clones[key] = m6Clone{sha: sha, dir: dir}
	s.m6CloneMu.Unlock()
	return sha, dir, nil
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
	base, err := fileBodies(baseDir)
	if err != nil {
		return 0, err
	}
	head, err := fileBodies(headDir)
	if err != nil {
		return 0, err
	}
	n := 0
	seen := map[string]bool{}
	for path, body := range head {
		seen[path] = true
		if base[path] != body {
			n++
		}
	}
	for path := range base {
		if !seen[path] {
			n++
		}
	}
	return n, nil
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
