package definition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// AttestRoleSource checks that commit in gitDir is a local commit whose
// tree contains the roster and each seat file, byte for byte. It does not
// fetch and does not write those bytes anywhere.
func AttestRoleSource(ctx context.Context, gitDir, commit string, tree NativeTree) error {
	if !commitHex(commit) {
		return fmt.Errorf("attest: %s: not a commit", commit)
	}
	g, cleanup, err := openAttestGit(ctx, gitDir)
	if err != nil {
		return err
	}
	defer cleanup()
	kind, err := g.kind(commit)
	if err != nil || kind != "commit" {
		return fmt.Errorf("attest: %s: not a commit", commit)
	}
	roster, err := g.blob(commit, "org/roster.yaml")
	if err != nil {
		return fmt.Errorf("attest: org/roster.yaml: absent object")
	}
	sum := sha256.Sum256(roster)
	if tree.RosterSHA256 != "" && hex.EncodeToString(sum[:]) != tree.RosterSHA256 {
		return fmt.Errorf("attest: org/roster.yaml: byte mismatch")
	}
	if tree.RosterGitBlob != "" && gitBlobID(roster) != tree.RosterGitBlob {
		return fmt.Errorf("attest: org/roster.yaml: byte mismatch")
	}
	for _, persona := range tree.Personas {
		for _, file := range persona.Files {
			rel := "org/agents/" + persona.Name + "/" + file.Name
			if !rolePath(rel) {
				return fmt.Errorf("attest: %s: absent object", rel)
			}
			body, err := g.blob(commit, rel)
			if err != nil {
				return fmt.Errorf("attest: %s: absent object", rel)
			}
			if !bytes.Equal(body, file.Bytes) {
				return fmt.Errorf("attest: %s: byte mismatch", rel)
			}
		}
	}
	return nil
}

type attestGit struct {
	ctx   context.Context
	dir   string
	hooks string
	home  string
}

func openAttestGit(ctx context.Context, dir string) (attestGit, func(), error) {
	hooks, err := os.MkdirTemp("", "factory-hooks-")
	if err != nil {
		return attestGit{}, nil, err
	}
	home, err := os.MkdirTemp("", "factory-git-home-")
	if err != nil {
		_ = os.RemoveAll(hooks)
		return attestGit{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(home, "gitconfig"), nil, 0o600); err != nil {
		_ = os.RemoveAll(hooks)
		_ = os.RemoveAll(home)
		return attestGit{}, nil, err
	}
	return attestGit{ctx: ctx, dir: dir, hooks: hooks, home: home}, func() {
		_ = os.RemoveAll(hooks)
		_ = os.RemoveAll(home)
	}, nil
}

func rolePath(rel string) bool {
	if rel == "org/roster.yaml" {
		return true
	}
	rest, ok := strings.CutPrefix(rel, "org/agents/")
	if !ok {
		return false
	}
	slug, name, ok := strings.Cut(rest, "/")
	if !ok || strings.Contains(slug, "/") {
		return false
	}
	switch name {
	case "AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md":
	default:
		return false
	}
	if slug == "" || len(slug) > 128 {
		return false
	}
	for _, c := range slug {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func (g attestGit) kind(commit string) (string, error) {
	if !commitHex(commit) {
		return "", fmt.Errorf("git")
	}
	out, code, err := g.batchCheck(commit)
	if err != nil || code != 0 {
		return "", fmt.Errorf("git")
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 || fields[1] == "missing" {
		return "", fmt.Errorf("git")
	}
	return fields[1], nil
}

func (g attestGit) blob(commit, rel string) ([]byte, error) {
	if !commitHex(commit) || !rolePath(rel) {
		return nil, fmt.Errorf("absent")
	}
	out, code, err := g.batch(commit + ":" + rel)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("absent")
	}
	return parseBatch(out)
}

func parseBatch(out []byte) ([]byte, error) {
	nl := bytes.IndexByte(out, '\n')
	if nl < 0 {
		return nil, fmt.Errorf("absent")
	}
	fields := strings.Fields(string(out[:nl]))
	if len(fields) != 3 || fields[1] == "missing" {
		return nil, fmt.Errorf("absent")
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil || size < 0 || len(out) < nl+1+size {
		return nil, fmt.Errorf("absent")
	}
	return out[nl+1 : nl+1+size], nil
}

func (g attestGit) batch(spec string) ([]byte, int, error) {
	return g.git(spec, "cat-file", "--batch")
}

func (g attestGit) batchCheck(spec string) ([]byte, int, error) {
	return g.git(spec, "cat-file", "--batch-check")
}

func (g attestGit) git(spec string, sub, mode string) ([]byte, int, error) {
	if strings.ContainsAny(spec, "\n\r") || sub != "cat-file" || (mode != "--batch" && mode != "--batch-check") {
		return nil, -1, fmt.Errorf("git")
	}
	ctx, cancel := context.WithCancel(g.ctx)
	defer cancel()
	// The object id is stdin. The argument list is the fixed cat-file mode.
	cmd := exec.CommandContext(ctx, "git",
		"-c", "core.hooksPath="+g.hooks,
		"-c", "credential.helper=",
		"-c", "core.fsmonitor=",
		"-c", "safe.directory=*",
		"-c", "advice.detachedHead=false",
		sub, mode,
	)
	cmd.Dir = g.dir
	cmd.Stdin = strings.NewReader(spec + "\n")
	// Same refusal as the checkout gate: parent GIT_CONFIG_* is not copied.
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
	if err == nil {
		return stdout.Bytes(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.Bytes(), exit.ExitCode(), nil
	}
	return nil, -1, err
}
