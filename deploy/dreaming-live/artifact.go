package dreaminglive

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ChainManifest is the handoff from the chain job to the publish job.
type ChainManifest struct {
	Pin          string `json:"pin"`
	Head         string `json:"head"`
	Parent       string `json:"parent"`
	BundleSHA256 string `json:"bundleSHA256"`
	Package      string `json:"package"`
	MutationPath string `json:"mutationPath"`
	SpendCents   int64  `json:"spendCents"`
	CeilingCents int64  `json:"ceilingCents"`
	EvidenceRun  bool   `json:"evidenceRun"`
}

func writeChainArtifact(ctx context.Context, dir, work, head, pin, dsn, evidenceRun, invocationID string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	bundlePath := filepath.Join(dir, "head.bundle")
	cmd := exec.Command("git", "-C", work, "bundle", "create", bundlePath, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bundle: %s", strings.TrimSpace(string(out)))
	}
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	parent, err := gitRev(work, "HEAD^")
	if err != nil {
		return err
	}
	evidenceErr := evidenceRan(ctx, dsn, evidenceRun)
	manifest := ChainManifest{
		Pin: pin, Head: head, Parent: parent, BundleSHA256: hex.EncodeToString(sum[:]),
		Package: configuredPackage(), MutationPath: configuredMutation(),
		SpendCents: spentCents(ctx, dsn, invocationID), CeilingCents: CeilingCents,
		EvidenceRun: evidenceErr == nil,
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(body, '\n'), 0o644)
}

// ValidateChainArtifact checks the handoff the publish job must re-verify.
func ValidateChainArtifact(dir string) (ChainManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return ChainManifest{}, err
	}
	var manifest ChainManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ChainManifest{}, err
	}
	if manifest.Parent != manifest.Pin || manifest.Pin == "" || manifest.Head == "" {
		return ChainManifest{}, errors.New("bundle parent is not the pin")
	}
	bundle, err := os.ReadFile(filepath.Join(dir, "head.bundle"))
	if err != nil {
		return ChainManifest{}, err
	}
	sum := sha256.Sum256(bundle)
	if hex.EncodeToString(sum[:]) != manifest.BundleSHA256 {
		return ChainManifest{}, errors.New("bundle hash mismatch")
	}
	if !manifest.EvidenceRun {
		return ChainManifest{}, errors.New("oskar evidence was not a test execution")
	}
	if manifest.CeilingCents <= 0 {
		manifest.CeilingCents = CeilingCents
	}
	if manifest.SpendCents < 0 || manifest.SpendCents > manifest.CeilingCents {
		return ChainManifest{}, errors.New("spend exceeds the ceiling")
	}
	return manifest, nil
}

// PublishFromDir re-verifies the chain artifact and opens one draft.
// It does not start a seat or a harness. The git token is not placed in
// the environment; a 0600 credential helper file holds it.
func PublishFromDir(ctx context.Context, dir string) error {
	manifest, err := ValidateChainArtifact(dir)
	if err != nil {
		return err
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
	tok, err := mintWriteToken(ctx, key, appID, inst)
	if err != nil {
		return err
	}
	return pushDraft(ctx, dir, manifest, tok.Value)
}

func mintWriteToken(ctx context.Context, key *rsa.PrivateKey, appID, inst int64) (mintedToken, error) {
	jwt, err := appJWT(appID, key)
	if err != nil {
		return mintedToken{}, err
	}
	body := []byte(`{"repositories":["dreaming"],"permissions":{"contents":"write","metadata":"read","pull_requests":"write"}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubBase()+"/app/installations/"+strconv.FormatInt(inst, 10)+"/access_tokens", bytes.NewReader(body))
	if err != nil {
		return mintedToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return mintedToken{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var doc struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
		return mintedToken{}, err
	}
	if (res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK) || doc.Token == "" {
		return mintedToken{}, fmt.Errorf("token HTTP %d", res.StatusCode)
	}
	return mintedToken{Value: doc.Token}, nil
}

func pushDraft(ctx context.Context, dir string, manifest ChainManifest, token string) error {
	helper, err := os.CreateTemp("", "git-cred-")
	if err != nil {
		return err
	}
	helperPath := helper.Name()
	script := "#!/bin/sh\ncase \"$1\" in\nget) printf '%s\\n' 'username=x-access-token' 'password=" + token + "' ;;\nesac\n"
	if _, err := helper.WriteString(script); err != nil {
		_ = helper.Close()
		return err
	}
	if err := helper.Close(); err != nil {
		return err
	}
	if err := os.Chmod(helperPath, 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(helperPath) }()
	work, err := os.MkdirTemp("", "dreaming-publish-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = work
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + work,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := git("init"); err != nil {
		return err
	}
	if err := git("-c", "credential.helper=", "bundle", "unbundle", filepath.Join(dir, "head.bundle")); err != nil {
		return err
	}
	remote := "https://github.com/" + DreamingRepo + ".git"
	if githubBase() != "https://api.github.com" {
		remote = strings.TrimRight(githubBase(), "/") + "/northstar-ai-labs/dreaming.git"
	}
	ref := "refs/heads/" + RefPrefix + "artifact"
	if err := git("-c", "credential.helper=!"+helperPath, "push", remote, manifest.Head+":"+ref); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"title": "Factory: DB-backed tenant-isolation tests (#454)",
		"head":  RefPrefix + "artifact",
		"base":  "main",
		"draft": true,
		"body":  "Head " + manifest.Head + "\nPin " + manifest.Pin + "\nRefs #454\nDraft by Factory; human review required; Factory will not merge.\n",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubBase()+"/repos/"+DreamingRepo+"/pulls", bytes.NewReader(payload))
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
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return fmt.Errorf("pull HTTP %d %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
