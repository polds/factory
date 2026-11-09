package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	evidenceKeep  = "factoryEvidencePass"
	evidenceBreak = "factoryEvidenceFail"
)

func runMutation(ctx context.Context, opt Options, task TaskFile, log *Log, finish func(int, string, string) int, secrets []string) int {
	head, err := evidenceHead(ctx, opt.Workspace)
	if err != nil || rejectMutationTarget(opt.Workspace, task.MutationPath) != nil || evidenceDirty(ctx, opt.Workspace) != nil {
		recordEvidence(log, secrets, EvidenceToolError, EvidenceToolError)
		return finish(ExitEnvironment, EvidenceToolError, "evidence tool_error")
	}
	if err := rewriteEvidence(opt.Workspace, task.MutationPath); err != nil {
		recordEvidence(log, secrets, EvidenceToolError, EvidenceToolError)
		return finish(ExitEnvironment, EvidenceToolError, "evidence tool_error")
	}
	failed, failedBody := runGoTest(ctx, opt, task.EvidencePackage)
	recordEvidence(log, secrets, failed, failedBody)
	if err := restoreEvidence(ctx, opt.Workspace, task.MutationPath); err != nil {
		recordEvidence(log, secrets, EvidenceToolError, EvidenceToolError)
		return finish(ExitEnvironment, EvidenceToolError, "evidence tool_error")
	}
	restoredHead, headErr := evidenceHead(ctx, opt.Workspace)
	if headErr != nil || !strings.EqualFold(restoredHead, head) || evidenceDirty(ctx, opt.Workspace) != nil {
		recordEvidence(log, secrets, EvidenceToolError, EvidenceToolError)
		return finish(ExitEnvironment, EvidenceToolError, "evidence tool_error")
	}
	passed, passedBody := runGoTest(ctx, opt, task.EvidencePackage)
	recordEvidence(log, secrets, passed, passedBody)
	if failed == EvidenceTestsFailed && passed == EvidenceTestsPassed {
		return finish(ExitOK, EvidenceTestsPassed, "evidence tests_passed")
	}
	return finish(ExitEnvironment, passed, "evidence "+passed)
}

func rejectMutationTarget(workspace, rel string) error {
	if err := lexicalEvidence(rel); err != nil {
		return err
	}
	cur := workspace
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink")
		}
		last := i == len(parts)-1
		if !last && !info.IsDir() {
			return errors.New("evidence path")
		}
		if last && !info.Mode().IsRegular() {
			return errors.New("evidence path")
		}
	}
	return nil
}

func rewriteEvidence(workspace, rel string) error {
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next := strings.Replace(string(body), evidenceKeep, evidenceBreak, 1)
	if next == string(body) {
		return errors.New("evidence token")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(next), info.Mode().Perm())
}

func restoreEvidence(ctx context.Context, workspace, rel string) error {
	g, cleanup, err := openEvidenceGit(ctx, workspace)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := refuseFilterDrivers(g); err != nil {
		return err
	}
	spec := "HEAD:" + rel
	out, code, err := g.stdout("cat-file", "blob", spec)
	if err != nil || code != 0 {
		return errors.New("cat-file")
	}
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), info.Mode().Perm())
}

func evidenceHead(ctx context.Context, workspace string) (string, error) {
	g, cleanup, err := openEvidenceGit(ctx, workspace)
	if err != nil {
		return "", err
	}
	defer cleanup()
	head, err := g.text("rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	return head, nil
}

func evidenceDirty(ctx context.Context, workspace string) error {
	g, cleanup, err := openEvidenceGit(ctx, workspace)
	if err != nil {
		return err
	}
	defer cleanup()
	out, err := g.text("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("dirty")
	}
	return nil
}

func openEvidenceGit(ctx context.Context, workspace string) (gitRunner, func(), error) {
	hooks, err := os.MkdirTemp("", "factory-hooks-")
	if err != nil {
		return gitRunner{}, nil, err
	}
	home, err := os.MkdirTemp("", "factory-git-home-")
	if err != nil {
		_ = os.RemoveAll(hooks)
		return gitRunner{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(home, "gitconfig"), nil, 0o600); err != nil {
		_ = os.RemoveAll(hooks)
		_ = os.RemoveAll(home)
		return gitRunner{}, nil, err
	}
	g := gitRunner{ctx: ctx, dir: workspace, hooks: hooks, home: home}
	return g, func() {
		_ = os.RemoveAll(hooks)
		_ = os.RemoveAll(home)
	}, nil
}
