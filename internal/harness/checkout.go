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
	if err := refuseFilterDrivers(g); err != nil {
		return err
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

func refuseFilterDrivers(g gitRunner) error {
	// status and checkout read one config: system and global are disabled on
	// the git process, then local, worktree (when extensions.worktreeConfig
	// is set), command-line -c, and include paths. Branch conditions are
	// read even when they do not match the current HEAD, because checkout
	// changes that context before the next git command.
	seen := map[string]struct{}{}
	if err := readCheckoutConfig(g, "", seen); err != nil {
		return err
	}
	return nil
}

type configEntry struct {
	scope  string
	origin string
	key    string
	value  string
}

func readCheckoutConfig(g gitRunner, file string, seen map[string]struct{}) error {
	if len(seen) > 32 {
		return fmt.Errorf("%w: filter config", errCheckoutWorkspace)
	}
	args := []string{"-c", "alias.config=", "config", "--includes", "--show-scope", "--show-origin", "--null", "--list"}
	if file != "" {
		if _, ok := seen[file]; ok {
			return nil
		}
		seen[file] = struct{}{}
		args = []string{"-c", "alias.config=", "config", "--file", file, "--includes", "--show-scope", "--show-origin", "--null", "--list"}
	}
	out, code, err := g.stdout(args...)
	if err != nil {
		return fmt.Errorf("%w: filter config: %w", errCheckoutWorkspace, err)
	}
	entries, err := configEntries(out)
	if err != nil || code != 0 {
		return fmt.Errorf("%w: filter config", errCheckoutWorkspace)
	}
	var includes []configEntry
	for _, entry := range entries {
		if filterCommand(entry.key, entry.value) {
			return fmt.Errorf("%w: filter driver", errCheckoutWorkspace)
		}
		if includePathKey(entry.key) {
			includes = append(includes, entry)
		}
	}
	for _, entry := range includes {
		path, err := resolveInclude(g, entry.origin, entry.value)
		if err != nil {
			return fmt.Errorf("%w: filter config", errCheckoutWorkspace)
		}
		if err := readCheckoutConfig(g, path, seen); err != nil {
			return err
		}
	}
	return nil
}

func configEntries(raw string) ([]configEntry, error) {
	raw = strings.TrimRight(raw, "\x00")
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, "\x00")
	if len(parts)%3 != 0 {
		return nil, errors.New("config shape")
	}
	entries := make([]configEntry, 0, len(parts)/3)
	for i := 0; i < len(parts); i += 3 {
		key, value, ok := strings.Cut(parts[i+2], "\n")
		if !ok {
			return nil, errors.New("config shape")
		}
		entries = append(entries, configEntry{
			scope:  parts[i],
			origin: parts[i+1],
			key:    key,
			value:  value,
		})
	}
	return entries, nil
}

func filterCommand(key, value string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if !strings.HasPrefix(key, "filter.") {
		return false
	}
	name := strings.TrimPrefix(key, "filter.")
	if !strings.HasSuffix(name, ".clean") && !strings.HasSuffix(name, ".smudge") && !strings.HasSuffix(name, ".process") {
		return false
	}
	return strings.TrimSpace(value) != ""
}

func includePathKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "include.path" {
		return true
	}
	return strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path")
}

func resolveInclude(g gitRunner, origin, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty include")
	}
	switch {
	case strings.HasPrefix(value, "~/"):
		value = filepath.Join(g.home, value[2:])
	case strings.HasPrefix(value, "~"):
		return "", errors.New("include user")
	case !filepath.IsAbs(value):
		base, err := configOriginDir(g, origin)
		if err != nil {
			return "", err
		}
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value), nil
}

func configOriginDir(g gitRunner, origin string) (string, error) {
	const prefix = "file:"
	if !strings.HasPrefix(origin, prefix) {
		return "", errors.New("include origin")
	}
	path := strings.TrimPrefix(origin, prefix)
	if path == "" {
		return "", errors.New("include origin")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.dir, path)
	}
	return filepath.Dir(path), nil
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
	stdout, stderr, code, err := g.exec(args...)
	text := stdout + stderr
	if len(text) > 240 {
		text = text[:240]
	}
	return text, code, err
}

func (g gitRunner) stdout(args ...string) (string, int, error) {
	stdout, _, code, err := g.exec(args...)
	return stdout, code, err
}

func (g gitRunner) exec(args ...string) (string, string, int, error) {
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
	// Parent GIT_CONFIG_COUNT, GIT_CONFIG_PARAMETERS, and GIT_CONFIG_KEY_*
	// are not copied. Those variables can define filter drivers.
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.String(), stderr.String(), -1, fmt.Errorf("%w: git timed out", errCheckoutWorkspace)
	}
	if err == nil {
		return stdout.String(), stderr.String(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), stderr.String(), exit.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), -1, err
}
