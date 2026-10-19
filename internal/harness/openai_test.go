package harness

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/inference"
)

const plantedHeader = "sk-planted-extra-header-value"

func TestCompleteChatSendsExtraHeaders(t *testing.T) {
	extra, err := inference.ParseExtraHeaders("authorization: Bearer oauth-planted-token\nx-litellm-api-key: Bearer " + plantedHeader)
	if err != nil {
		t.Fatal(err)
	}
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := completeChat(t.Context(), srv.URL, "sk-api-key-value", extra, chatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer oauth-planted-token" {
		t.Fatal("extra Authorization did not replace the api key")
	}
	if got.Get("X-Litellm-Api-Key") != "Bearer "+plantedHeader {
		t.Fatal("custom header was not sent")
	}
	if len(got.Values("Authorization")) != 1 {
		t.Fatal("duplicate Authorization")
	}
}

func TestCompleteChatUnsetHeadersUnchanged(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := completeChat(t.Context(), srv.URL, "sk-api-key-value", nil, chatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer sk-api-key-value" || got.Get("Content-Type") != "application/json" {
		t.Fatal("unset request changed default headers")
	}
	if got.Get("X-Litellm-Api-Key") != "" {
		t.Fatal("unset request sent an extra header")
	}
	for name := range got {
		switch name {
		case "Authorization", "Content-Type", "Content-Length", "User-Agent", "Accept-Encoding":
		default:
			t.Fatalf("unexpected header %s", name)
		}
	}
}

func TestCompleteChatOmitsEmptyAuthorization(t *testing.T) {
	extra, err := inference.ParseExtraHeaders("x-litellm-api-key: Bearer " + plantedHeader)
	if err != nil {
		t.Fatal(err)
	}
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := completeChat(t.Context(), srv.URL, "", extra, chatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "" {
		t.Fatal("empty api key sent Authorization")
	}
	if got.Get("X-Litellm-Api-Key") != "Bearer "+plantedHeader {
		t.Fatal("custom header was not sent")
	}
}

func TestCompleteChatRedactsHeaderValue(t *testing.T) {
	extra, err := inference.ParseExtraHeaders("x-litellm-api-key: Bearer " + plantedHeader)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"rejected Bearer `+plantedHeader+`"}}`)
	}))
	t.Cleanup(srv.Close)

	_, err = completeChat(t.Context(), srv.URL, "", extra, chatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if strings.Contains(err.Error(), plantedHeader) {
		t.Fatal("provider error contains header value")
	}
	if !strings.Contains(err.Error(), "provider HTTP 401") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatal("provider error was not redacted")
	}
}

func TestRunSendsExtraHeadersAndRedacts(t *testing.T) {
	var auth, custom string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		auth = r.Header.Get("Authorization")
		custom = r.Header.Get("X-Litellm-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"echo `+plantedHeader+`"}}]}`)
	}))
	t.Cleanup(srv.Close)

	var stderr strings.Builder
	rt := t.TempDir()
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			"OPENAI_API_KEY=sk-api-key-value",
			inference.ExtraHeadersEnv + "=x-litellm-api-key: Bearer " + plantedHeader,
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=1",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: rt,
		Stderr:     &stderr,
	})
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if calls != 1 || auth != "Bearer sk-api-key-value" || custom != "Bearer "+plantedHeader {
		t.Fatal("model request headers mismatch")
	}
	events, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), plantedHeader) || strings.Contains(stderr.String(), plantedHeader) {
		t.Fatal("transcript leaked header value")
	}
	if !strings.Contains(string(events), "[REDACTED]") {
		t.Fatal("transcript did not redact the header value")
	}
}

func TestRunUnsetDoesNotSendExtraHeader(t *testing.T) {
	var custom, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		custom = r.Header.Get("X-Litellm-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			"OPENAI_API_KEY=sk-api-key-value",
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=1",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: t.TempDir(),
		Stderr:     io.Discard,
	})
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if auth != "Bearer sk-api-key-value" || custom != "" {
		t.Fatal("unset extra headers changed the request")
	}
}

func TestRunRejectsMalformedExtraHeaders(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)

	var stderr strings.Builder
	rt := t.TempDir()
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			inference.ExtraHeadersEnv + "=not a header " + plantedHeader,
			"FACTORY_TASK_PROMPT=do the thing",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: rt,
		Stderr:     &stderr,
	})
	if code != ExitConfigInvalid {
		t.Fatalf("exit %d", code)
	}
	if calls != 0 {
		t.Fatal("malformed headers still called the model")
	}
	events, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), plantedHeader) || strings.Contains(stderr.String(), plantedHeader) {
		t.Fatal("malformed header error leaked the value")
	}
	if !strings.Contains(string(events), "line 1 is malformed") {
		t.Fatal("missing malformed line error")
	}
}

func TestScrubEnvDropsExtraHeaders(t *testing.T) {
	out := scrubEnv([]string{
		inference.ExtraHeadersEnv + "=x-litellm-api-key: Bearer " + plantedHeader,
		"OPENAI_API_KEY=" + plantedHeader,
		"FOO=bar",
	}, nil)
	for _, kv := range out {
		if strings.Contains(kv, plantedHeader) || strings.HasPrefix(kv, inference.ExtraHeadersEnv+"=") {
			t.Fatal("secret env survived scrub")
		}
	}
	if len(out) != 1 || out[0] != "FOO=bar" {
		t.Fatal("scrub dropped a non-secret")
	}
}
