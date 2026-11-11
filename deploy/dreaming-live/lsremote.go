package dreaminglive

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/polds/factory/internal/server"
)

// LsRemote runs git ls-remote with the Authorization header in GIT_CONFIG_*
// http.extraHeader. The header is never placed in argv. prefix is matched
// against the ref name git prints (refs/heads/...).
func LsRemote(ctx context.Context, remote, header, prefix string) ([]string, error) {
	if remote == "" || strings.ContainsAny(remote, "\r\n") {
		return nil, errors.New("git ls-remote remote")
	}
	if strings.Contains(header, "\n") || strings.Contains(header, "\r") {
		return nil, errors.New("git ls-remote header")
	}
	home, err := os.MkdirTemp("", "factory-git-home-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(home) }()
	hooks, err := os.MkdirTemp("", "factory-hooks-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(hooks) }()
	if err := os.WriteFile(filepath.Join(home, "gitconfig"), nil, 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git",
		"-c", "core.hooksPath="+hooks,
		"-c", "credential.helper=",
		"-c", "safe.directory=*",
		"ls-remote", remote, "refs/heads/"+prefix+"*",
	)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=" + header,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("git ls-remote timed out")
	}
	if runErr != nil {
		text := strings.TrimSpace(stderr.String())
		if len(text) > 180 {
			text = text[:180]
		}
		text = strings.ReplaceAll(text, header, "Authorization")
		return nil, errors.New("git ls-remote: " + text)
	}
	var refs []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "refs/heads/"+prefix) {
			refs = append(refs, name)
		}
	}
	return refs, nil
}

// AuthHeader returns the Basic x-access-token header the publisher uses.
func AuthHeader(token string) (string, error) {
	return server.GitHTTPAuthHeader(token)
}
