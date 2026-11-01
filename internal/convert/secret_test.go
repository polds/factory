package convert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlantedSecretStaysOutOfConvert(t *testing.T) {
	const (
		planted       = "planted-secret-value-9f3a"
		token         = "sk-plantedsecretvalue9f3a"
		credentialRef = "anthropic-api-key"
		secretName    = "sentry-auth-token"
	)
	dir := t.TempDir()
	factory := "schemaVersion: v1alpha1\nname: planted\ncredentialStrategy: EXECUTOR\nsecrets:\n  - " + secretName + "\ninference:\n  default:\n    provider: anthropic\n    credentialRef: " + credentialRef + "\nagentDefaults:\n  model: auto\n  runner: local\n"
	if err := os.MkdirAll(filepath.Join(dir, "agents", "foreman"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "runners"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := "description: local\nplatform:\n  os: linux\n  arch: amd64\n"
	agent := "---\ndescription: routes work\nagentType: FOREMAN\nmodel: auto\nrunner: local\nsecrets:\n  - " + secretName + "\nmcpServers:\n  sentry:\n    url: https://mcp.example/sse\n    headers:\n      Authorization: \"Bearer " + planted + "\"\n    env:\n      API_TOKEN: \"" + token + "\"\n---\n\nDo not print " + planted + " or " + token + ".\n"
	if err := os.WriteFile(filepath.Join(dir, "factory.yaml"), []byte(factory), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runners", "local.yaml"), []byte(runner), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "foreman", "agent.md"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}

	manifests, err := DirToManifests(dir, Options{})
	combined := string(manifests)
	if err != nil {
		combined += "\n" + err.Error()
	}
	for _, secret := range []string{planted, token} {
		if strings.Contains(combined, secret) {
			t.Fatalf("convert leaked %s", secret)
		}
	}
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(combined, "credentialRef: "+credentialRef) && !strings.Contains(combined, "credentialRef: \""+credentialRef+"\"") {
		t.Fatal("credentialRef name missing")
	}
	if !strings.Contains(combined, secretName) {
		t.Fatal("secretRefs name missing")
	}
	if !strings.Contains(combined, "secretRefs:") {
		t.Fatal("secretRefs field missing")
	}

	bad := t.TempDir()
	broken := "name: [\ncredentialRef: " + credentialRef + "\nsecret: " + token + "\n"
	if err := os.WriteFile(filepath.Join(bad, "factory.yaml"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = DirToManifests(bad, Options{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), planted) || strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked a secret: %v", err)
	}
}

func TestRedactKeepsSecretRefs(t *testing.T) {
	in := `Authorization: "Bearer {{SENTRY_MCP_TOKEN}}"
credentialRef: anthropic-api-key
secretName: CI_WEBHOOK_SECRET
`
	if got := redactText(in); got != in {
		t.Fatalf("redact changed a clean file\n got %q\nwant %q", got, in)
	}
	leaked := redactText("Authorization: Bearer planted-secret-value-9f3a\nkey: sk-plantedsecretvalue9f3a\n")
	if strings.Contains(leaked, "planted-secret-value-9f3a") || strings.Contains(leaked, "sk-plantedsecretvalue9f3a") {
		t.Fatalf("redact kept a value: %s", leaked)
	}
}
