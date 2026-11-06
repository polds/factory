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

func TestRedactEmbeddedToken(t *testing.T) {
	const planted = "xxsk-embeddedplanted9f3a"
	got := redactText("see " + planted + " and https://github.com/polds/factory and Bearer {{SENTRY_MCP_TOKEN}}")
	if strings.Contains(got, planted) {
		t.Fatal("embedded token kept")
	}
	if !strings.Contains(got, "https://github.com/polds/factory") {
		t.Fatal("repository url removed")
	}
	if !strings.Contains(got, "Bearer {{SENTRY_MCP_TOKEN}}") {
		t.Fatal("secret ref removed")
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatal("replacement marker missing")
	}
}

func TestRedactShapeTable(t *testing.T) {
	const (
		embedded = "xxsk-embeddedplanted9f3a"
		slash    = "ghp_Planted/SecretValue99"
		ghs      = "ghs_12345_eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig"
		userTok  = "ghp_PlantedTokenValue99"
	)
	rows := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{name: "ask-pass", in: "https://github.com/org/ask-pass", want: "https://github.com/org/ask-pass"},
		{name: "task-runner", in: "https://github.com/org/task-runner", want: "https://github.com/org/task-runner"},
		{name: "task-id", in: "task-id", want: "task-id"},
		{name: "disk-usage", in: "disk-usage", want: "disk-usage"},
		{name: "embedded sk", in: embedded, want: "xxREDACTED", secret: embedded},
		{name: "ghp slash", in: slash, want: "REDACTED", secret: slash},
		{name: "ghs", in: ghs, want: "REDACTED", secret: ghs},
		{
			name:   "url path",
			in:     "https://github.com/polds/ghp_notatoken/factory",
			want:   "https://github.com/polds/REDACTED/factory",
			secret: "ghp_notatoken",
		},
		{
			name:   "userinfo",
			in:     "https://x-access-token:" + userTok + "@github.com/polds/factory.git",
			want:   "https://x-access-token:REDACTED@github.com/polds/factory.git",
			secret: userTok,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := redactText(row.in)
			if row.secret != "" && strings.Contains(got, row.secret) {
				t.Fatal("redaction kept a planted value")
			}
			if got != row.want {
				if row.secret != "" {
					t.Fatal("redacted shape mismatch")
				}
				t.Fatalf("got %q", got)
			}
		})
	}
}
