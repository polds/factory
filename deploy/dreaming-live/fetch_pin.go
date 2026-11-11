package dreaminglive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// credentialGitArgs is the only git credential path. Fetch and push both use
// it. The token is not placed in the environment or the remote URL.
func credentialGitArgs(helper string, args ...string) []string {
	prefix := []string{"-c", "credential.helper=", "-c", "credential.helper=!" + helper}
	return append(prefix, args...)
}

// FetchPin mints a dreaming-only contents:read installation token, fetches
// exactly the pin, and revokes the token before returning. Revocation failure
// fails the step even when the fetch succeeded. The token is not written to
// the environment or to git config.
func FetchPin(ctx context.Context) error {
	dest := strings.TrimSpace(os.Getenv("FACTORY_ROLE_SOURCE"))
	pin := dreamingPin()
	if dest == "" || len(pin) != 40 {
		return errors.New("pin fetch requires FACTORY_ROLE_SOURCE and the pin")
	}
	appID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("M6_GITHUB_APP_ID")), 10, 64)
	if err != nil || appID <= 0 {
		return errors.New("M6_GITHUB_APP_ID")
	}
	key, err := parseKey(os.Getenv("M6_GITHUB_APP_PRIVATE_KEY"))
	if err != nil {
		return err
	}
	inst, status, _, _, _, instErr := installationDoc(ctx, appID, key)
	if instErr != nil || status != http.StatusOK {
		return fmt.Errorf("installation HTTP %d", status)
	}
	tok, err := mintPinReadToken(ctx, key, appID, inst)
	if err != nil {
		return err
	}
	helper, err := writeCredentialHelper(tok.Value)
	if err != nil {
		_ = revokeInstallationToken(ctx, tok.Value)
		return err
	}
	defer func() { _ = os.Remove(helper) }()
	fetchErr := fetchPinWithHelper(ctx, dest, pin, helper)
	if revokeErr := revokeInstallationToken(ctx, tok.Value); revokeErr != nil {
		return fmt.Errorf("revoke installation token: %w", revokeErr)
	}
	return fetchErr
}

func revokeInstallationToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, githubBase()+"/installation/token", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revoke HTTP %d", res.StatusCode)
	}
	return nil
}

func fetchPinWithHelper(ctx context.Context, dest, pin, helper string) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	init := exec.CommandContext(ctx, "git", "init", dest)
	init.Env = gitPinEnv(dest)
	if out, err := init.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %s", strings.TrimSpace(string(out)))
	}
	if err := gitPlain(ctx, dest, "remote", "add", "origin", dreamingGitRemote()); err != nil {
		return err
	}
	if err := gitPlain(ctx, dest, credentialGitArgs(helper, "fetch", "--depth", "1", "origin", pin)...); err != nil {
		return err
	}
	if err := gitPlain(ctx, dest, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}
	head, err := gitRev(dest, "HEAD")
	if err != nil || head != pin {
		return fmt.Errorf("fetched HEAD %s is not the pin %s", head, pin)
	}
	cfg, err := gitPlainOutput(ctx, dest, "config", "--local", "--list")
	if err != nil {
		return err
	}
	if strings.Contains(cfg, "credential.helper") || strings.Contains(cfg, "extraheader") {
		return errors.New("pin checkout kept a credential")
	}
	return nil
}

func gitPlain(ctx context.Context, dir string, args ...string) error {
	_, err := gitPlainOutput(ctx, dir, args...)
	return err
}

func gitPlainOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitPinEnv(dir)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s: %s", args[0], text)
	}
	return text, nil
}

func gitPinEnv(dir string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
}
