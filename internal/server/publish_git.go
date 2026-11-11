package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitBearerRejected is the body GitHub's smart HTTP returns when a push
// sends Authorization: Bearer. Git operations authenticate with Basic
// username x-access-token.
const GitBearerRejected = "Invalid username or token. Password authentication is not supported for Git operations."

// GitHTTPAuthHeader is the git smart-HTTP Authorization value for an
// installation token. The caller puts it in GIT_CONFIG_* http.extraHeader
// and never in argv. Bearer is not accepted by github.com for receive-pack.
func GitHTTPAuthHeader(token string) (string, error) {
	if token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return "", errors.New("git auth token")
	}
	raw := "x-access-token:" + token
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(raw)), nil
}

// draftGit is the checkout gate's git runner. harness.gitRunner is
// unexported and internal/harness is outside this change, so the publisher
// keeps the same controls here: hooksPath is an empty directory, parent
// GIT_CONFIG_* is not copied, and the installation token is passed only as
// http.extraheader in the child environment.
type draftGit struct {
	ctx    context.Context
	dir    string
	hooks  string
	home   string
	token  string
	remote string
}

func openDraftGit(ctx context.Context, dir string) (draftGit, func(), error) {
	hooks, err := os.MkdirTemp("", "factory-hooks-")
	if err != nil {
		return draftGit{}, nil, err
	}
	home, err := os.MkdirTemp("", "factory-git-home-")
	if err != nil {
		_ = os.RemoveAll(hooks)
		return draftGit{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(home, "gitconfig"), nil, 0o600); err != nil {
		_ = os.RemoveAll(hooks)
		_ = os.RemoveAll(home)
		return draftGit{}, nil, err
	}
	g := draftGit{ctx: ctx, dir: dir, hooks: hooks, home: home}
	return g, func() {
		_ = os.RemoveAll(hooks)
		_ = os.RemoveAll(home)
	}, nil
}

func (g draftGit) text(args ...string) (string, error) {
	stdout, _, code, err := g.exec(args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %s", args[0])
	}
	return strings.TrimSpace(stdout), nil
}

func (g draftGit) stdout(args ...string) (string, error) {
	stdout, stderr, code, err := g.exec(args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %s: %s", args[0], scrubPublishText(stderr, g.token))
	}
	return stdout, nil
}

func (g draftGit) exec(args ...string) (string, string, int, error) {
	for _, arg := range args {
		if g.token != "" && strings.Contains(arg, g.token) {
			return "", "", -1, errors.New("git token in argv")
		}
	}
	ctx, cancel := context.WithTimeout(g.ctx, 30*time.Second)
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
	cmd.Env = g.environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	outText := scrubGitHeader(scrubPublishText(stdout.String(), g.token), g.token)
	errText := scrubGitHeader(scrubPublishText(stderr.String(), g.token), g.token)
	if ctx.Err() != nil {
		return outText, errText, -1, errors.New("git timed out")
	}
	if err == nil {
		return outText, errText, 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return outText, errText, exit.ExitCode(), nil
	}
	return outText, errText, -1, errors.New(scrubGitHeader(scrubPublishText(err.Error(), g.token), g.token))
}

func scrubGitHeader(text, token string) string {
	if token != "" {
		if header, err := GitHTTPAuthHeader(token); err == nil {
			text = strings.ReplaceAll(text, header, "Authorization: github-installation-token")
			enc := strings.TrimPrefix(header, "Authorization: Basic ")
			text = strings.ReplaceAll(text, enc, "github-installation-token")
		}
	}
	return strings.ReplaceAll(text, "Authorization: Basic ", "Authorization: ")
}

func (g draftGit) environ() []string {
	env := []string{
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
	if g.token != "" {
		header, err := GitHTTPAuthHeader(g.token)
		if err != nil {
			header = "Authorization: Basic invalid"
		}
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraheader",
			"GIT_CONFIG_VALUE_0="+header,
		)
	}
	return env
}

func (g draftGit) withToken(token, remote string) draftGit {
	g.token = token
	g.remote = remote
	return g
}

type remoteHeads struct {
	base string
	refs []string
}

func (g draftGit) listRemote(prefix string) (remoteHeads, error) {
	stdout, stderr, code, err := g.exec(
		"ls-remote", "--symref", g.remote, "HEAD", "refs/heads/"+prefix+"*",
	)
	if err != nil {
		return remoteHeads{}, err
	}
	if code != 0 {
		text := strings.TrimSpace(stderr)
		if len(text) > 180 {
			text = text[:180]
		}
		return remoteHeads{}, fmt.Errorf("git ls-remote: %s", text)
	}
	var heads remoteHeads
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ref, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if strings.HasPrefix(ref, "ref:") && name == "HEAD" {
			heads.base = strings.TrimPrefix(strings.TrimSpace(ref), "ref:")
			heads.base = strings.TrimPrefix(strings.TrimSpace(heads.base), "refs/heads/")
			continue
		}
		if strings.HasPrefix(name, "refs/heads/"+prefix) {
			heads.refs = append(heads.refs, name)
		}
	}
	if heads.base == "" || strings.Contains(heads.base, "/") || strings.Contains(heads.base, "..") {
		return remoteHeads{}, errors.New("git ls-remote: default branch")
	}
	return heads, nil
}

func (g draftGit) pushCreate(sha, ref string) error {
	if strings.HasPrefix(ref, "+") || strings.Contains(ref, " ") {
		return errors.New("git push ref")
	}
	spec := sha + ":refs/heads/" + ref
	_, stderr, code, err := g.exec("push", g.remote, spec)
	if err != nil {
		return err
	}
	if code != 0 {
		text := strings.TrimSpace(stderr)
		if len(text) > 180 {
			text = text[:180]
		}
		return fmt.Errorf("git push: %s", text)
	}
	return nil
}
