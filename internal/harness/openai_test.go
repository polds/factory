package harness

import (
	"context"
	"encoding/json"
	"errors"
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

func mustJSON(t *testing.T, m chatMessage) map[string]any {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestMarshalToolAndUserContent(t *testing.T) {
	tool := mustJSON(t, chatMessage{Role: "tool", ToolCallID: "call_1", Content: ""})
	if tool["content"] != noToolOutput {
		t.Fatalf("empty tool content %#v", tool["content"])
	}
	if tool["tool_call_id"] != "call_1" {
		t.Fatalf("tool id %#v", tool["tool_call_id"])
	}
	blank := mustJSON(t, chatMessage{Role: "tool", ToolCallID: "call_1", Content: " \n"})
	if blank["content"] != noToolOutput {
		t.Fatalf("blank tool content %#v", blank["content"])
	}
	user := mustJSON(t, chatMessage{Role: "user", Content: ""})
	if user["content"] != noUserText {
		t.Fatalf("empty user content %#v", user["content"])
	}
}

func TestMarshalAssistantToolCallOmitsEmptyContent(t *testing.T) {
	call := toolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "bash"
	call.Function.Arguments = `{"command":"true"}`
	raw, err := json.Marshal(chatMessage{Role: "assistant", Content: "", ToolCalls: []toolCall{call}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"content"`) {
		t.Fatalf("empty assistant tool call sent content: %s", raw)
	}
	spaced, err := json.Marshal(chatMessage{Role: "assistant", Content: "  ", ToolCalls: []toolCall{call}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(spaced), `"content"`) {
		t.Fatalf("blank assistant tool call sent content: %s", spaced)
	}
	noted := mustJSON(t, chatMessage{Role: "assistant", Content: "note", ToolCalls: []toolCall{call}})
	if noted["content"] != "note" {
		t.Fatalf("assistant note %#v", noted["content"])
	}
}

func TestMarshalRoundTripUnchanged(t *testing.T) {
	call := toolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "bash"
	call.Function.Arguments = `{"command":"printf x"}`
	messages := []chatMessage{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: "working"},
		{Role: "assistant", Content: "note", ToolCalls: []toolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Content: "x"},
	}
	for _, msg := range messages {
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		var back chatMessage
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if back.Role != msg.Role || back.Content != msg.Content || back.ToolCallID != msg.ToolCallID {
			t.Fatalf("round trip %+v from %+v (%s)", back, msg, raw)
		}
		if len(back.ToolCalls) != len(msg.ToolCalls) {
			t.Fatalf("tool calls %+v", back.ToolCalls)
		}
	}
}

func TestClassifyProviderRequestShape(t *testing.T) {
	modelNotFound := &providerError{Status: 400, Message: "model not found"}
	code, status, summary := classifyProvider(modelNotFound, 0, 1)
	if code != ExitConfigInvalid || status != "config_invalid" || summary != "provider HTTP 400: model not found" {
		t.Fatalf("400 before accept: %d %s %s", code, status, summary)
	}
	code, status, summary = classifyProvider(modelNotFound, 4, 5)
	want := "provider rejected request shape (HTTP 400) at turn 5: model not found"
	if code != ExitConfigInvalid || status != statusRequestRejected || summary != want {
		t.Fatalf("400 after accept: %d %s %s", code, status, summary)
	}
	for _, httpStatus := range []int{401, 403, 404} {
		err := &providerError{Status: httpStatus, Message: "no"}
		code, status, summary = classifyProvider(err, 3, 4)
		if code != ExitConfigInvalid || status != "config_invalid" {
			t.Fatalf("%d: %d %s %s", httpStatus, code, status, summary)
		}
		if summary != err.Error() {
			t.Fatalf("%d summary %s", httpStatus, summary)
		}
	}
	code, status, summary = classifyProvider(&providerError{Status: 500, Message: "down"}, 1, 2)
	if code != ExitModelUnavailable || status != "model_unavailable" || summary != "provider HTTP 500: down" {
		t.Fatalf("500: %d %s %s", code, status, summary)
	}
	code, status, summary = classifyProvider(&providerError{Message: "connection refused"}, 1, 2)
	if code != ExitModelUnavailable || status != "model_unavailable" || summary != "connection refused" {
		t.Fatalf("status 0: %d %s %s", code, status, summary)
	}
	code, status, _ = classifyProvider(errors.New("boom"), 1, 2)
	if code != ExitModelUnavailable || status != "model_unavailable" {
		t.Fatalf("plain error: %d %s", code, status)
	}
}

func TestRunEmptyToolOutputStaysValid(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		bodies = append(bodies, raw)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) < 5 {
			command := "printf x"
			if len(bodies) == 4 {
				command = "true"
			}
			id := "c" + string(rune('0'+len(bodies)))
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"`+id+`","type":"function","function":{"name":"bash","arguments":"{\"command\":\"`+command+`\"}"}}]}}]}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"messages[9]: content is required"}}`)
	}))
	t.Cleanup(srv.Close)

	var stderr strings.Builder
	rt := t.TempDir()
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			"OPENAI_API_KEY=sk-api-key-value",
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=5",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: rt,
		Stderr:     &stderr,
	})
	if code != ExitConfigInvalid {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(bodies) != 5 {
		t.Fatalf("model calls %d", len(bodies))
	}
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(bodies[4], &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 10 {
		t.Fatalf("messages %d", len(req.Messages))
	}
	for i, msg := range req.Messages {
		role, _ := msg["role"].(string)
		content, present := msg["content"]
		switch role {
		case "assistant":
			if _, calls := msg["tool_calls"]; calls && present {
				t.Fatalf("message %d sent content %#v", i, content)
			}
			if !present && msg["tool_calls"] == nil && content == "" {
				t.Fatalf("message %d assistant has no content", i)
			}
		default:
			text, _ := content.(string)
			if !present || strings.TrimSpace(text) == "" {
				t.Fatalf("message %d role %s content %#v", i, role, content)
			}
		}
	}
	last, _ := req.Messages[9]["content"].(string)
	if last != noToolOutput {
		t.Fatalf("empty tool result content %q", last)
	}
	if strings.Contains(string(bodies[4]), `"content":""`) {
		t.Fatal("request contained an empty content string")
	}
	events, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(events)
	if strings.Contains(text, noToolOutput) {
		t.Fatal("placeholder leaked into events")
	}
	var emptyDigests int
	var terminal Event
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "tool.result" && ev.OutputDigest == Digest("") {
			emptyDigests++
		}
		if ev.Type == "terminal" {
			terminal = ev
		}
	}
	if emptyDigests != 1 {
		t.Fatalf("empty output digests %d", emptyDigests)
	}
	want := "provider rejected request shape (HTTP 400) at turn 5: messages[9]: content is required"
	if terminal.Status != statusRequestRejected || terminal.Summary != want {
		t.Fatalf("terminal %+v", terminal)
	}
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr %s", stderr.String())
	}
}
