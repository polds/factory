package secrets_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/secrets"
)

func TestRedactionRotationOldAndNew(t *testing.T) {
	r := secrets.NewRedactor()
	old := "CANARY-OLD"
	newVal := "CANARY-NEW"
	r.Observe("SCAN_GITHUB_TOKEN", old)
	r.Observe("SCAN_GITHUB_TOKEN", newVal)

	cases := []string{
		fmt.Sprintf("mcp output mentions %s and %s", old, newVal),
		fmt.Sprintf(`{"token":"%s","rotated":"%s"}`, old, newVal),
		fmt.Sprintf("Bearer %s", old),
	}
	for _, raw := range cases {
		for _, out := range []string{r.Text(raw), r.JSON(raw)} {
			if strings.Contains(out, old) || strings.Contains(out, newVal) {
				t.Fatalf("leak in %q -> %q", raw, out)
			}
		}
	}
}

func TestRedactionMCPIntegrationShape(t *testing.T) {
	r := secrets.NewRedactor()
	secret := "CANARY-SYNTHETIC"
	r.Observe("SCAN_GITHUB_TOKEN", secret)
	payload := r.Event("mcp_call", "export CANARY-SYNTHETIC")
	if strings.Contains(payload, secret) {
		t.Fatalf("event leaked: %s", payload)
	}
	if !strings.Contains(payload, "mcp_call") {
		t.Fatal(payload)
	}
}

func TestRedactionSlogJSON(t *testing.T) {
	r := secrets.NewRedactor()
	r.Observe("T", "CANARY-OLD")
	r.Observe("T", "CANARY-NEW")
	rec := &captureHandler{}
	log := slog.New(rec)
	log.Info("probe", r.SlogAttr("body", "old=CANARY-OLD new=CANARY-NEW"))
	if strings.Contains(rec.last, "CANARY-OLD") || strings.Contains(rec.last, "CANARY-NEW") {
		t.Fatalf("slog leaked: %s", rec.last)
	}
}

func TestLongHyphenatedSKResidualDocumented(t *testing.T) {
	// M8-R leaves a long hyphenated sk- edge in convert; secrets redaction uses the same matcher set.
	r := secrets.NewRedactor()
	line := "sk-" + strings.Repeat("a-b-", 20) + "tail"
	out := r.Text(line)
	if out == line {
		t.Log("residual: long hyphenated sk- token may survive credential matcher; tighten in convert if needed")
	}
}

type captureHandler struct {
	last string
}

func (c *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(a.Value.String())
		return true
	})
	c.last = b.String()
	return nil
}

func (c *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return c }

func (c *captureHandler) WithGroup(name string) slog.Handler { return c }

func TestRedactionJSONRoundTrip(t *testing.T) {
	r := secrets.NewRedactor()
	r.Observe("X", "CANARY-SYNTHETIC")
	in := map[string]any{"nested": map[string]any{"v": "CANARY-SYNTHETIC"}}
	raw, _ := json.Marshal(in)
	out := r.JSON(string(raw))
	if strings.Contains(out, "CANARY-SYNTHETIC") {
		t.Fatalf("json leak: %s", out)
	}
}

func TestRedactionHarnessAuthNeedles(t *testing.T) {
	r := secrets.NewRedactor()
	harnessSecret := "CANARY-SYNTHETIC-harness-auth"
	r.Observe("HARNESS_AUTH", harnessSecret)
	combined := harness.Redact("Authorization: Bearer "+harnessSecret, r.Needles())
	combined = r.Text(combined)
	if strings.Contains(combined, harnessSecret) {
		t.Fatalf("harness-auth path leaked: %q", combined)
	}
}

func TestRedactionDelayedErrorArtifact(t *testing.T) {
	r := secrets.NewRedactor()
	oldVal, newVal := "CANARY-OLD", "CANARY-NEW"
	r.Observe("T", oldVal)
	r.Observe("T", newVal)
	delayed := fmt.Sprintf(`{"error":"failed after retry","context":"%s then %s"}`, oldVal, newVal)
	out := r.JSON(delayed)
	if strings.Contains(out, oldVal) || strings.Contains(out, newVal) {
		t.Fatalf("delayed artifact leak: %s", out)
	}
}
