package harness

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// RecordWorkspaceHead emits workspace.head and uploads one bundle.
// The event text is the 40-hex SHA. The bundle file is removed after upload.
func RecordWorkspaceHead(ctx context.Context, opt Options, log *Log) error {
	g, cleanup, err := openEvidenceGit(ctx, opt.Workspace)
	if err != nil {
		return err
	}
	defer cleanup()
	sha, err := g.text("rev-parse", "--verify", "HEAD")
	if err != nil || !commitHex40(sha) {
		return errors.New("workspace head")
	}
	if err := log.Emit(Event{Type: EventWorkspaceHead, Text: sha}); err != nil {
		return err
	}
	bundle := filepath.Join(opt.RuntimeDir, "head.bundle")
	if _, err := g.text("bundle", "create", bundle, "HEAD"); err != nil {
		return err
	}
	defer func() { _ = os.Remove(bundle) }()
	url := bundleURL(lookupEnv(opt.Env, "FACTORY_EVENTS_URL"))
	token := lookupEnv(opt.Env, "FACTORY_RUN_TOKEN")
	if url == "" || token == "" {
		return nil
	}
	return postBundle(ctx, url, token, sha, bundle)
}

// VerifyBundleTip checks the bundle in an empty directory with the checkout
// gate's git runner. It does not fetch. The tip must equal want.
func VerifyBundleTip(ctx context.Context, bundlePath, want string) error {
	dir, err := os.MkdirTemp("", "factory-bundle-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g, cleanup, err := openEvidenceGit(ctx, dir)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := g.text("init"); err != nil {
		return err
	}
	if _, code, err := g.run("bundle", "verify", bundlePath); err != nil || code != 0 {
		return errors.New("bundle verify")
	}
	out, code, err := g.stdout("bundle", "list-heads", bundlePath)
	if err != nil || code != 0 {
		return errors.New("bundle tip")
	}
	tip := firstSHA(out)
	if !strings.EqualFold(tip, want) {
		return errors.New("bundle tip")
	}
	return nil
}

func installProducedBundle(ctx context.Context, opt Options, want string) error {
	path, err := producedBundleFile(ctx, opt)
	if err != nil {
		return err
	}
	if err := VerifyBundleTip(ctx, path, want); err != nil {
		if delErr := deleteProducedBundle(ctx, opt); delErr != nil {
			return fmt.Errorf("%w: produced bundle: %s", errCheckoutWorkspace, delErr.Error())
		}
		return fmt.Errorf("%w: produced bundle: %s", errCheckoutWorkspace, err.Error())
	}
	if err := os.MkdirAll(opt.Workspace, 0o755); err != nil {
		return err
	}
	g, cleanup, err := openEvidenceGit(ctx, opt.Workspace)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := g.text("init"); err != nil {
		return err
	}
	origin := lookupEnv(opt.Env, EnvOriginURL)
	if origin == "" {
		return fmt.Errorf("%w: origin", errCheckoutWorkspace)
	}
	if _, code, err := g.run("remote", "get-url", "origin"); err != nil || code != 0 {
		if _, err := g.text("remote", "add", "origin", origin); err != nil {
			return err
		}
	}
	if _, code, err := g.run("bundle", "unbundle", path); err != nil || code != 0 {
		return fmt.Errorf("%w: unbundle", errCheckoutWorkspace)
	}
	if err := enforceLocalCheckout(ctx, opt.Workspace, optReviewRaw(opt)); err != nil {
		return err
	}
	if err := deleteProducedBundle(ctx, opt); err != nil {
		return err
	}
	return nil
}

func deleteProducedBundle(ctx context.Context, opt Options) error {
	url := lookupEnv(opt.Env, EnvBundleURL)
	if url == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+lookupEnv(opt.Env, "FACTORY_RUN_TOKEN"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound {
		return errors.New("bundle delete")
	}
	return nil
}

func producedBundleFile(ctx context.Context, opt Options) (string, error) {
	url := lookupEnv(opt.Env, EnvBundleURL)
	if url == "" {
		return "", fmt.Errorf("%w: produced bundle missing", errCheckoutWorkspace)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+lookupEnv(opt.Env, "FACTORY_RUN_TOKEN"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: produced bundle missing", errCheckoutWorkspace)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: produced bundle missing", errCheckoutWorkspace)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	path := filepath.Join(opt.RuntimeDir, "head.bundle")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func optReviewRaw(opt Options) []string {
	return ParseArgs(opt.Args).Values["repository-head-override-json"]
}

func postBundle(ctx context.Context, url, token, sha, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, f)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Factory-Head", sha)
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return errors.New("bundle upload")
	}
	return nil
}

func bundleURL(eventsURL string) string {
	eventsURL = strings.TrimSpace(eventsURL)
	const suffix = "/events:ingest"
	if !strings.HasSuffix(eventsURL, suffix) {
		return ""
	}
	return strings.TrimSuffix(eventsURL, suffix) + "/head.bundle"
}

func firstSHA(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if commitHex40(fields[0]) {
			return fields[0]
		}
	}
	return ""
}

func commitHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
