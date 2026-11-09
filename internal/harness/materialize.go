package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// PreHarnessSetupDisabled is the task.json and env value that refuses an
	// untrusted setup command for a role_files proof run.
	PreHarnessSetupDisabled = "disabled"

	// EnvPreHarnessSetup is set by factoryd on a role_files assignment.
	EnvPreHarnessSetup = "FACTORY_PRE_HARNESS_SETUP"
	// EnvBundleMode carries the stored bundle mode onto the task file.
	EnvBundleMode = "FACTORY_BUNDLE_MODE"
	// EnvLocalSource is an existing local git repository. The proof path
	// copies it into an empty workspace and does not fetch.
	EnvLocalSource = "FACTORY_LOCAL_OBJECT_SOURCE"
	// EnvUntrustedSetup is a worker setup command. Proof mode never executes it.
	EnvUntrustedSetup = "FACTORY_UNTRUSTED_SETUP"
)

var errPreHarnessSetup = errors.New("pre-harness setup refused")

func proofSetupDisabled(env []string, task TaskFile) bool {
	return lookupEnv(env, EnvPreHarnessSetup) == PreHarnessSetupDisabled || task.PreHarnessSetup == PreHarnessSetupDisabled
}

// MaterializeAssigned writes the task file a role_files proof run hands to the
// checkout gate, and copies a local repository into an empty workspace when
// one is named. It does not fetch, checkout, reset, or run a setup command.
// The upstream worker's backend setup_command still runs before this process
// when that worker configuration sets one; this function never sees it.
func MaterializeAssigned(ctx context.Context, opt Options, inv Invocation) (TaskFile, error) {
	path := filepath.Join(opt.RuntimeDir, "task.json")
	existing, err := LoadTaskFile(path)
	if err != nil {
		return TaskFile{}, err
	}
	if !proofSetupDisabled(opt.Env, existing) {
		return TaskFile{}, fmt.Errorf("%w: not a proof assignment", errCheckoutOverride)
	}
	if strings.TrimSpace(lookupEnv(opt.Env, EnvUntrustedSetup)) != "" || strings.TrimSpace(existing.SetupCommand) != "" {
		return TaskFile{}, errPreHarnessSetup
	}
	raws := inv.Values["repository-head-override-json"]
	if len(raws) != 1 || explicitEmptyOverride(raws[0]) {
		return TaskFile{}, fmt.Errorf("%w: empty or repeated override", errCheckoutOverride)
	}
	ov, err := oneCheckoutOverride(raws[0])
	if err != nil {
		return TaskFile{}, err
	}
	prompt := lookupEnv(opt.Env, "FACTORY_TASK_PROMPT")
	if prompt == "" {
		prompt = existing.Prompt
	} else if existing.Prompt != "" && existing.Prompt != prompt {
		return TaskFile{}, fmt.Errorf("%w: prompt", errCheckoutOverride)
	}
	model := inv.Model()
	if model == "" {
		model = existing.ModelID
	} else if existing.ModelID != "" && existing.ModelID != model {
		return TaskFile{}, fmt.Errorf("%w: model", errCheckoutOverride)
	}
	mode := lookupEnv(opt.Env, EnvBundleMode)
	if mode == "" {
		mode = existing.BundleMode
	} else if existing.BundleMode != "" && existing.BundleMode != mode {
		return TaskFile{}, fmt.Errorf("%w: bundle mode", errCheckoutOverride)
	}
	if existing.PinnedHeadSHA != "" && !strings.EqualFold(existing.PinnedHeadSHA, ov.sha) {
		return TaskFile{}, fmt.Errorf("%w: pin", errCheckoutOverride)
	}
	if err := placeLocalWorkspace(ctx, opt.Workspace, lookupEnv(opt.Env, EnvLocalSource), ov.sha); err != nil {
		return TaskFile{}, err
	}
	task := TaskFile{
		Prompt:          prompt,
		ModelID:         model,
		Secrets:         copyStrings(existing.Secrets),
		HarnessAuthEnv:  copyStrings(existing.HarnessAuthEnv),
		PinnedHeadSHA:   ov.sha,
		BundleMode:      mode,
		PreHarnessSetup: PreHarnessSetupDisabled,
	}
	if err := writeTaskFile(path, task); err != nil {
		return TaskFile{}, err
	}
	return task, nil
}

func placeLocalWorkspace(ctx context.Context, workspace, source, sha string) error {
	if err := refuseWorkspaceSymlink(workspace); err != nil {
		return err
	}
	if err := refuseParentRepository(ctx, workspace); err != nil {
		return err
	}
	ready, err := workspaceReadyForSource(workspace)
	if err != nil {
		return err
	}
	if source != "" && ready && !samePath(source, workspace) {
		if err := vetLocalSource(ctx, source, sha); err != nil {
			return err
		}
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			return fmt.Errorf("%w: workspace: %w", errCheckoutWorkspace, err)
		}
		if err := copyGitTree(source, workspace); err != nil {
			_ = os.RemoveAll(workspace)
			return err
		}
		if err := removeWorkerEnvFiles(workspace); err != nil {
			return err
		}
		// A copied index keeps an untracked-cache entry for the source path.
		// The checkout gate treats that warning as a dirty tree. Drop the
		// cache only; do not refresh, reset, or check out.
		if err := dropUntrackedCache(ctx, workspace); err != nil {
			return err
		}
	}
	return objectIsCommit(ctx, workspace, sha)
}

func refuseWorkspaceSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: workspace: %w", errCheckoutWorkspace, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: workspace is not a real directory", errCheckoutWorkspace)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: workspace is not a real directory", errCheckoutWorkspace)
	}
	return nil
}

// workspaceReadyForSource reports whether a local source may be copied.
// The pinned direct worker creates an empty oz-env-* file in the workspace
// before it execs the harness, so that file does not mean a checkout exists.
func workspaceReadyForSource(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: workspace: %w", errCheckoutWorkspace, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("%w: workspace is not a real directory", errCheckoutWorkspace)
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		return false, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("%w: workspace: %w", errCheckoutWorkspace, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "oz-env-") {
			return false, nil
		}
	}
	return true, nil
}

func removeWorkerEnvFiles(workspace string) error {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "oz-env-") {
			continue
		}
		if err := os.Remove(filepath.Join(workspace, entry.Name())); err != nil {
			return fmt.Errorf("%w: worker env file: %w", errCheckoutWorkspace, err)
		}
	}
	return nil
}

func refuseParentRepository(ctx context.Context, workspace string) error {
	check := workspace
	if _, err := os.Lstat(workspace); os.IsNotExist(err) {
		check = filepath.Dir(workspace)
	}
	info, err := os.Lstat(check)
	if err != nil {
		return fmt.Errorf("%w: workspace: %w", errCheckoutWorkspace, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: workspace is not a real directory", errCheckoutWorkspace)
	}
	top, found, err := gitToplevel(ctx, check)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("%w: workspace path", errCheckoutWorkspace)
	}
	topAbs, err := filepath.Abs(strings.TrimSpace(top))
	if err != nil {
		return fmt.Errorf("%w: toplevel", errCheckoutWorkspace)
	}
	if filepath.Clean(topAbs) == filepath.Clean(ws) {
		return nil
	}
	return fmt.Errorf("%w: parent repository", errCheckoutWorkspace)
}

func gitToplevel(ctx context.Context, dir string) (string, bool, error) {
	var top string
	var found bool
	err := withGit(ctx, dir, func(g gitRunner) error {
		out, code, runErr := g.stdout("rev-parse", "--show-toplevel")
		if runErr != nil {
			return runErr
		}
		if code != 0 {
			return nil
		}
		top = strings.TrimSpace(out)
		found = true
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("%w: toplevel: %w", errCheckoutWorkspace, err)
	}
	return top, found, nil
}

// vetLocalSource checks the local object source before any copy. It refuses a
// symlink, a worktree gitfile, a git dir outside the source, a filter driver,
// and an origin URL that is not a plain GitHub remote. Git commands use an
// empty hooks directory and do not fetch or checkout.
func vetLocalSource(ctx context.Context, source, sha string) error {
	if err := refuseWorkspaceSymlink(source); err != nil {
		return err
	}
	gitPath := filepath.Join(source, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: git dir", errCheckoutWorkspace)
	}
	return withGit(ctx, source, func(g gitRunner) error {
		top, err := g.text("rev-parse", "--show-toplevel")
		if err != nil {
			return fmt.Errorf("%w: not a repository", errCheckoutWorkspace)
		}
		if err := sameRealDir(source, top); err != nil {
			return err
		}
		absGit, err := g.text("rev-parse", "--absolute-git-dir")
		if err != nil {
			return fmt.Errorf("%w: git dir", errCheckoutWorkspace)
		}
		if err := dirInside(source, absGit); err != nil {
			return fmt.Errorf("%w: git dir escapes workspace", errCheckoutWorkspace)
		}
		if err := refuseFilterDrivers(g); err != nil {
			return err
		}
		remote, err := g.text("remote", "get-url", "origin")
		if err != nil {
			return fmt.Errorf("%w: origin", errCheckoutWorkspace)
		}
		if _, _, ok := githubRemote(remote); !ok || originHasUserinfo(remote) {
			return fmt.Errorf("%w: repository identity", errCheckoutWorkspace)
		}
		kind, err := g.text("cat-file", "-t", sha)
		if err != nil || kind != "commit" {
			return fmt.Errorf("%w: missing object", errCheckoutWorkspace)
		}
		return nil
	})
}

func originHasUserinfo(raw string) bool {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(raw), "://")
	if !ok {
		return false
	}
	host, _, _ := strings.Cut(rest, "/")
	user, _, hasUser := strings.Cut(host, "@")
	if !hasUser {
		return false
	}
	// ssh://git@github.com/ is the standard remote, not a credential.
	if scheme == "ssh" && user == "git" {
		return false
	}
	return true
}

func dropUntrackedCache(ctx context.Context, dir string) error {
	err := withGit(ctx, dir, func(g gitRunner) error {
		_, gitErr := g.text("update-index", "--no-untracked-cache")
		return gitErr
	})
	if err != nil {
		return fmt.Errorf("%w: index", errCheckoutWorkspace)
	}
	return nil
}

func objectIsCommit(ctx context.Context, dir, sha string) error {
	if err := refuseWorkspaceSymlink(dir); err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err != nil {
		return fmt.Errorf("%w: workspace: %w", errCheckoutWorkspace, err)
	}
	var kind string
	err := withGit(ctx, dir, func(g gitRunner) error {
		got, gitErr := g.text("cat-file", "-t", sha)
		if gitErr != nil {
			return gitErr
		}
		kind = got
		return nil
	})
	if err != nil || kind != "commit" {
		return fmt.Errorf("%w: missing object", errCheckoutWorkspace)
	}
	return nil
}

func withGit(ctx context.Context, dir string, fn func(gitRunner) error) error {
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
	return fn(gitRunner{ctx: ctx, dir: dir, hooks: hooks, home: home})
}

func copyGitTree(src, dst string) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("%w: source", errCheckoutWorkspace)
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("%w: workspace", errCheckoutWorkspace)
	}
	if pathInside(srcAbs, dstAbs) {
		return fmt.Errorf("%w: source", errCheckoutWorkspace)
	}
	return filepath.WalkDir(srcAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(srcAbs, path)
		if err != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: copy escape", errCheckoutWorkspace)
		}
		target := filepath.Join(dstAbs, relPath)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if linkEscapes(srcAbs, path, link) {
				return fmt.Errorf("%w: symlink escapes source", errCheckoutWorkspace)
			}
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			return copyRegular(path, target, info.Mode())
		default:
			return fmt.Errorf("%w: special file", errCheckoutWorkspace)
		}
	})
}

func copyRegular(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string{}, in...)
}

func linkEscapes(root, linkPath, link string) bool {
	target := link
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	return !pathInside(root, filepath.Clean(target))
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func writeTaskFile(path string, task TaskFile) error {
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}
