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
	kind, err := g.text("cat-file", "-t", commit)
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

func (g attestGit) blob(commit, rel string) ([]byte, error) {
	out, code, err := g.run("cat-file", "blob", commit+":"+rel)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("absent")
	}
	return []byte(out), nil
}

func (g attestGit) text(args ...string) (string, error) {
	out, code, err := g.run(args...)
	if err != nil || code != 0 {
		return "", fmt.Errorf("git %s", args[0])
	}
	return strings.TrimSpace(out), nil
}

func (g attestGit) run(args ...string) (string, int, error) {
	ctx, cancel := context.WithCancel(g.ctx)
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
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), exit.ExitCode(), nil
	}
	return "", -1, err
}
