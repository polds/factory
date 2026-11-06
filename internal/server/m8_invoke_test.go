package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/polds/factory/internal/invocation"
)

func TestM8InvokeAPIReplayAndConflict(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})

	body := map[string]any{
		"sourceOccurrenceKey": "human:review:api-1",
		"intent":              "new",
		"priority":            "interactive",
	}
	first := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusCreated)
	second := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusOK)
	if first["invocationId"] != second["invocationId"] {
		t.Fatalf("replay ids %v vs %v", first["invocationId"], second["invocationId"])
	}
	body["intent"] = "continue"
	env.postJSONExpect(t, "/api/v1/factory/pipe/invocations", body, http.StatusConflict)
}

func TestM8InsertInvocationReplayIdempotent(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	key := "replay-idempotent"
	body := map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}
	first := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusCreated)
	second := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusOK)
	if first["invocationId"] != second["invocationId"] {
		t.Fatalf("replay mismatch %v vs %v", first["invocationId"], second["invocationId"])
	}
}

func TestM8GrantRejectedWithoutSideEffects(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	body := map[string]any{
		"sourceOccurrenceKey": "grant:deny:1",
		"intent":              "new",
		"priority":            "interactive",
		"inputs":              map[string]any{"executeTarget": true},
	}
	env.postJSONExpect(t, "/api/v1/factory/pipe/invocations", body, http.StatusForbidden)
	var count int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM invocations WHERE source_occurrence_key = $1`, "grant:deny:1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invocations written on grant reject: %d", count)
	}
}

func TestM8ScheduleOccurrenceKeyFormat(t *testing.T) {
	key := invocation.ScheduleOccurrenceKey("sch_test", "2026-11-09T00:00:00Z")
	want := "schedule:sch_test:slot:2026-11-09T00:00:00Z"
	if key != want {
		t.Fatalf("got %q want %q", key, want)
	}
}

func (e *env) postJSON(t *testing.T, path string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	return e.postJSONExpect(t, path, body, wantStatus)
}

func (e *env) postJSONExpect(t *testing.T, path string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s status %d", path, resp.StatusCode)
	}
	if wantStatus == http.StatusForbidden || wantStatus == http.StatusConflict {
		return nil
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
