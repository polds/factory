package definition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestCredentialUnion(t *testing.T) {
	const (
		bearer = "planted-bearer-value-9f3a"
		basic  = "planted-basic-value-9f3a"
		token  = "sk-plantedunionvalue9f3a"
		glued  = "xxsk-embeddedplanted9f3a"
		slash  = "ghp_Planted/SecretValue99"
		short  = "AKIA1234"
		url    = "https://github.com/polds/factory"
	)
	in := "Authorization: Bearer " + bearer + "\nBasic " + basic + "\n" + token + "\n" + glued + "\n" + slash + "\n" + short + "\n" + url
	if !InlineCredential(in) {
		t.Fatal("union did not detect a credential")
	}
	if InlineCredential("Authorization: Bearer {{SENTRY_MCP_TOKEN}}") {
		t.Fatal("secret ref counted as a credential")
	}
	if InlineCredential(url) {
		t.Fatal("repository url counted as a credential")
	}
	red := RedactInline(in)
	for _, secret := range []string{bearer, basic, token, glued, slash, short} {
		if strings.Contains(red, secret) {
			t.Fatal("redaction kept a planted value")
		}
	}
	if !strings.Contains(red, url) {
		t.Fatal("redaction removed the repository url")
	}
	if !strings.Contains(red, "Bearer [REDACTED]") || !strings.Contains(red, "Basic [REDACTED]") {
		t.Fatal("auth markers changed")
	}
	kept := RedactInline("keep {{AKIASECRET123456}} end")
	if kept != "keep {{AKIASECRET123456}} end" {
		t.Fatalf("secret ref changed: %q", kept)
	}
	vals := CredentialValues("token " + token)
	if len(vals) != 1 || vals[0].Kind != CredentialToken {
		t.Fatal("token match missing")
	}
	rendered := fmt.Sprintf("%v %s %q %#v %+v", vals[0], vals[0], vals[0], vals[0], vals[0])
	if strings.Contains(rendered, token) {
		t.Fatal("fmt leaked")
	}
	raw, err := json.Marshal(vals[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) || !strings.Contains(string(raw), `"kind":"token"`) {
		t.Fatal("json leaked or dropped the kind")
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cred", "v", vals[0])
	if strings.Contains(buf.String(), token) {
		t.Fatal("slog leaked")
	}
}

func TestQuotedSecretRefStays(t *testing.T) {
	if InlineCredential("Bearer {{SENTRY_MCP_TOKEN}}") {
		t.Fatal("bearer secret ref counted as a credential")
	}
	if got := RedactInline("Bearer {{SENTRY_MCP_TOKEN}}"); got != "Bearer {{SENTRY_MCP_TOKEN}}" {
		t.Fatalf("secret ref changed: %q", got)
	}
}
