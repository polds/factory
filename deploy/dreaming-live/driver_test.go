package dreaminglive

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDriverAllowEnvDropsSentinel(t *testing.T) {
	got := driverAllowEnv([]string{
		"PATH=/bin",
		"ZZ_DRIVER_SECRET=zz-driver-app-key-sentinel",
		"M6_GITHUB_APP_PRIVATE_KEY=zz-m6-initial-sentinel",
		"M1_OPENAI_API_KEY=zz-m1-initial-sentinel",
		"FACTORY_ROLE_SOURCE=/tmp/role",
		"FACTORY_MODEL_KEY_FD=3",
	})
	text := strings.Join(got, "\n")
	for _, needle := range []string{"ZZ_DRIVER_SECRET", "PRIVATE_KEY", "M1_OPENAI_API_KEY", "zz-m6", "zz-m1", "zz-driver"} {
		if strings.Contains(text, needle) {
			t.Fatalf("allowlist kept %s in %s", needle, text)
		}
	}
	if !strings.Contains(text, "FACTORY_ROLE_SOURCE=/tmp/role") || !strings.Contains(text, "FACTORY_MODEL_KEY_FD=3") {
		t.Fatalf("allowlist dropped an operational value: %s", text)
	}
}

func TestFilterSeatEnvDropsDriverEnviron(t *testing.T) {
	dirty := append(os.Environ(),
		"ZZ_DRIVER_SECRET=zz-driver-app-key-sentinel",
		"M6_GITHUB_APP_PRIVATE_KEY=zz-m6-initial-sentinel",
	)
	extra := []string{"OPENAI_API_KEY=sk-seat", "PATH=/usr/bin", "ZZ_DRIVER_SECRET=from-extra"}
	got := filterSeatEnv(dirty, extra)
	text := strings.Join(got, "\n")
	if strings.Contains(text, "ZZ_DRIVER_SECRET") || strings.Contains(text, "M6_GITHUB_APP_PRIVATE_KEY") || strings.Contains(text, "zz-m6") {
		t.Fatalf("seat env passed a driver secret through: %s", text)
	}
	if !strings.Contains(text, "OPENAI_API_KEY=sk-seat") {
		t.Fatalf("seat key missing: %s", text)
	}
}

func TestWorkflowSplitsSecretsFromSeats(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github/workflows/dreaming-live.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	chain := betweenJobs(text, "\n  chain:\n", "\n  publish:\n")
	publish := betweenJobs(text, "\n  publish:\n", "\n")
	if chain == "" || publish == "" {
		t.Fatal("chain or publish job missing")
	}
	if strings.Contains(chain, "M6_GITHUB_APP_PRIVATE_KEY") || strings.Contains(chain, "M6_GITHUB_APP_ID") {
		t.Fatal("chain job references the app")
	}
	for _, banned := range []string{"factory-harness", "chain.sh", "run.sh", "dreamingpreflight publish"} {
		if strings.Contains(publish, banned) && banned != "dreamingpreflight publish" {
			t.Fatalf("publish job contains %s", banned)
		}
	}
	if strings.Contains(publish, "factory-harness") || strings.Contains(publish, "chain.sh") || strings.Contains(publish, "run.sh") {
		t.Fatal("publish job runs a seat")
	}
	if !strings.Contains(publish, "publish") {
		t.Fatal("publish job does not publish")
	}
}

func TestValidateChainArtifactRejectsBadParent(t *testing.T) {
	dir := t.TempDir()
	body := []byte("bundle-bytes")
	if err := os.WriteFile(filepath.Join(dir, "head.bundle"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex(body)
	manifest := `{
	  "pin": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	  "head": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	  "parent": "cccccccccccccccccccccccccccccccccccccccc",
	  "bundleSHA256": "` + sum + `",
	  "evidenceRun": true,
	  "spendCents": 1,
	  "ceilingCents": 2500
	}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateChainArtifact(dir); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("bad parent: %v", err)
	}
}

func betweenJobs(text, start, end string) string {
	i := strings.Index(text, start)
	if i < 0 {
		return ""
	}
	rest := text[i+len(start):]
	if end == "\n" {
		return rest
	}
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j]
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
