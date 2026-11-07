package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	errCheckoutOverride  = errors.New("checkout override")
	errCheckoutWorkspace = errors.New("checkout workspace")
)

// enforceLocalCheckout materializes one already-local commit when the worker
// passed a checkout override. An empty document keeps the current workspace.
// A populated document that is not exactly one supported COMMIT_SHA override
// fails before git mutates the tree. Git runs with per-command config only:
// hooks are pointed at an empty directory, and the user and system gitconfig
// are not read.
func enforceLocalCheckout(ctx context.Context, workspace string, raws []string) error {
	if len(raws) == 0 {
		return nil
	}
	if len(raws) != 1 || explicitEmptyOverride(raws[0]) {
		return fmt.Errorf("%w: empty or repeated override", errCheckoutOverride)
	}
	ov, err := oneCheckoutOverride(raws[0])
	if err != nil {
		return err
	}
	info, err := os.Lstat(workspace)
	if err != nil {
		return fmt.Errorf("%w: workspace: %w", errCheckoutWorkspace, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: workspace is not a real directory", errCheckoutWorkspace)
	}
	hooks, err := os.MkdirTemp("", "factory-hooks-")
	if err != nil {
		return fmt.Errorf("%w: hooks dir: %w", errCheckoutWorkspace, err)
	}
	defer func() { _ = os.RemoveAll(hooks) }()
	home, err := os.MkdirTemp("", "factory-git-home-")
	if err != nil {
		return fmt.Errorf("%w: git home: %w", errCheckoutWorkspace, err)
	}
	defer func() { _ = os.RemoveAll(home) }()
	if err := os.WriteFile(filepath.Join(home, "gitconfig"), nil, 0o600); err != nil {
		return fmt.Errorf("%w: git config: %w", errCheckoutWorkspace, err)
	}
	g := gitRunner{ctx: ctx, dir: workspace, hooks: hooks, home: home}
	top, err := g.text("rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("%w: not a repository", errCheckoutWorkspace)
	}
	if err := sameRealDir(workspace, top); err != nil {
		return err
	}
	gitPath := filepath.Join(workspace, ".git")
	gitInfo, err := os.Lstat(gitPath)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: git dir", errCheckoutWorkspace)
	}
	absGit, err := g.text("rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("%w: git dir", errCheckoutWorkspace)
	}
	if err := dirInside(workspace, absGit); err != nil {
		return fmt.Errorf("%w: git dir escapes workspace", errCheckoutWorkspace)
	}
	remotes, err := g.text("remote")
	if err != nil {
		return fmt.Errorf("%w: remotes", errCheckoutWorkspace)
	}
	names := splitLines(remotes)
	if len(names) != 1 || names[0] != "origin" {
		return fmt.Errorf("%w: repository identity is ambiguous", errCheckoutWorkspace)
	}
	remote, err := g.text("remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("%w: origin", errCheckoutWorkspace)
	}
	owner, repo, ok := githubRemote(remote)
	if !ok || !strings.EqualFold(owner, ov.owner) || !strings.EqualFold(repo, ov.repo) {
		return fmt.Errorf("%w: repository identity", errCheckoutWorkspace)
	}
	dirty, err := g.text("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("%w: status", errCheckoutWorkspace)
	}
	if strings.TrimSpace(dirty) != "" {
		return fmt.Errorf("%w: dirty workspace", errCheckoutWorkspace)
	}
	kind, err := g.text("cat-file", "-t", ov.sha)
	if err != nil {
		return fmt.Errorf("%w: missing object", errCheckoutWorkspace)
	}
	if kind != "commit" {
		return fmt.Errorf("%w: object is not a commit", errCheckoutWorkspace)
	}
	if _, err := g.text("checkout", "--detach", ov.sha); err != nil {
		return fmt.Errorf("%w: checkout", errCheckoutWorkspace)
	}
	head, err := g.text("rev-parse", "--verify", "HEAD")
	if err != nil {
		return fmt.Errorf("%w: head", errCheckoutWorkspace)
	}
	if !strings.EqualFold(head, ov.sha) {
		return fmt.Errorf("%w: head is not the requested commit", errCheckoutWorkspace)
	}
	if _, code, err := g.run("symbolic-ref", "--quiet", "HEAD"); err != nil || code == 0 {
		return fmt.Errorf("%w: head is not detached", errCheckoutWorkspace)
	}
	after, err := g.text("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || strings.TrimSpace(after) != "" {
		return fmt.Errorf("%w: workspace is not clean", errCheckoutWorkspace)
	}
	top, err = g.text("rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("%w: toplevel", errCheckoutWorkspace)
	}
	if err := sameRealDir(workspace, top); err != nil {
		return err
	}
	return nil
}

type checkoutOverride struct {
	sha   string
	owner string
	repo  string
}

type checkoutDoc struct {
	CodeForge string `json:"code_forge"`
	RepoOwner string `json:"repo_owner"`
	RepoName  string `json:"repo_name"`
	Head      struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"head"`
	CloneFrom      json.RawMessage `json:"clone_from"`
	PreserveOrigin bool            `json:"preserve_origin"`
}

func decodeCheckout(raw string, dest any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("%w: invalid override", errCheckoutOverride)
	}
	if dec.More() {
		return fmt.Errorf("%w: invalid override", errCheckoutOverride)
	}
	return nil
}

func explicitEmptyOverride(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return true
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return false
	}
	switch doc := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(doc) == 0
	case []any:
		return len(doc) == 0
	default:
		return false
	}
}

func oneCheckoutOverride(raw string) (checkoutOverride, error) {
	raw = strings.TrimSpace(raw)
	var docs []checkoutDoc
	if strings.HasPrefix(raw, "[") {
		if err := decodeCheckout(raw, &docs); err != nil {
			return checkoutOverride{}, err
		}
		if len(docs) != 1 {
			return checkoutOverride{}, fmt.Errorf("%w: override count", errCheckoutOverride)
		}
	} else {
		var doc checkoutDoc
		if err := decodeCheckout(raw, &doc); err != nil {
			return checkoutOverride{}, err
		}
		docs = []checkoutDoc{doc}
	}
	doc := docs[0]
	if doc.CodeForge != "GITHUB" || doc.RepoOwner == "" || doc.RepoName == "" {
		return checkoutOverride{}, fmt.Errorf("%w: unsupported repository", errCheckoutOverride)
	}
	if doc.Head.Type != "COMMIT_SHA" || !commitHex(doc.Head.Value) {
		return checkoutOverride{}, fmt.Errorf("%w: unsupported head", errCheckoutOverride)
	}
	if doc.PreserveOrigin || cloneFromSet(doc.CloneFrom) {
		return checkoutOverride{}, fmt.Errorf("%w: unsupported override", errCheckoutOverride)
	}
	return checkoutOverride{sha: doc.Head.Value, owner: doc.RepoOwner, repo: doc.RepoName}, nil
}

func cloneFromSet(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && string(trimmed) != "null"
}

func commitHex(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func githubRemote(raw string) (owner, repo string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	switch {
	case strings.HasPrefix(s, "https://github.com/"):
		s = strings.TrimPrefix(s, "https://github.com/")
	case strings.HasPrefix(s, "ssh://git@github.com/"):
		s = strings.TrimPrefix(s, "ssh://git@github.com/")
	case strings.HasPrefix(s, "git@github.com:"):
		s = strings.TrimPrefix(s, "git@github.com:")
	default:
		return "", "", false
	}
	owner, repo, ok = strings.Cut(s, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return owner, repo, true
}

func sameRealDir(workspace, top string) error {
	ws, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return fmt.Errorf("%w: workspace path", errCheckoutWorkspace)
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return fmt.Errorf("%w: toplevel", errCheckoutWorkspace)
	}
	if filepath.Clean(ws) != filepath.Clean(root) {
		return fmt.Errorf("%w: parent repository", errCheckoutWorkspace)
	}
	return nil
}

func dirInside(root, path string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	pathReal, err := filepath.EvalSymlinks(strings.TrimSpace(path))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootReal, pathReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("escape")
	}
	return nil
}

func splitLines(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(raw), "\n")
}

type gitRunner struct {
	ctx   context.Context
	dir   string
	hooks string
	home  string
}

func (g gitRunner) text(args ...string) (string, error) {
	out, code, err := g.run(args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %s", args[0])
	}
	return strings.TrimSpace(out), nil
}

func (g gitRunner) run(args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(g.ctx, 20*time.Second)
	defer cancel()
	full := []string{
		"-c", "core.hooksPath=" + g.hooks,
		"-c", "credential.helper=",
		"-c", "core.fsmonitor=",
		"-c", "safe.directory=*",
		"-c", "advice.detachedHead=false",
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = g.dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + g.home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(g.home, "gitconfig"),
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GIT_NO_LAZY_FETCH=1",
		"GCM_INTERACTIVE=never",
	}
	out, err := cmd.CombinedOutput()
	text := string(out)
	if len(text) > 240 {
		text = text[:240]
	}
	if ctx.Err() != nil {
		return text, -1, fmt.Errorf("%w: git timed out", errCheckoutWorkspace)
	}
	if err == nil {
		return text, 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return text, exit.ExitCode(), nil
	}
	return text, -1, err
}
