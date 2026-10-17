package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConsoleGoldensDecode(t *testing.T) {
	cases := []struct {
		name string
		dest any
	}{
		{"definition.json", &Definition{}},
		{"run.json", &RunDetail{}},
		{"timeline.json", &[]Phase{}},
		{"harness-usage.json", &Usage{}},
		{"interrupt.json", &SteerAck{}},
		{"followup.json", &SteerAck{}},
		{"steer.json", &[]Steer{}},
		{"inbox.json", &[]InboxItem{}},
		{"automations.json", &[]Automation{}},
		{"webhooks.json", &[]Webhook{}},
		{"workers.json", &WorkersView{}},
		{"secrets.json", &[]Secret{}},
		{"identities.json", &[]Identity{}},
		{"summary.json", &Summary{}},
		{"runs.json", &[]RunRow{}},
		{"steers.json", &[]SteerRecord{}},
		{"webhook-deliveries.json", &[]Delivery{}},
		{"deliveries.json", &[]Delivery{}},
	}
	for _, tc := range cases {
		raw := readConsoleGolden(t, tc.name)
		if err := json.Unmarshal(raw, tc.dest); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		back, err := json.Marshal(tc.dest)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(normalizeClientJSON(t, raw), normalizeClientJSON(t, back)) {
			t.Fatalf("%s\n got %s\nwant %s", tc.name, normalizeClientJSON(t, back), normalizeClientJSON(t, raw))
		}
	}
	var secrets []Secret
	if err := json.Unmarshal(readConsoleGolden(t, "secrets.json"), &secrets); err != nil {
		t.Fatal(err)
	}
	if _, ok := any(secrets[0]).(interface{ Value() string }); ok {
		t.Fatal("secret value accessor")
	}
}

func TestListRunsSendsConsoleFilters(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		got = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"run_1","status":"failed","model_id":"test-model","stage":"reviewing","conversation_id":"conv_1","failure_class":"agent","failure_summary":"The agent reported the run as failed","queue_wait_ms":0,"exec_ms":5,"cost_cents":3}]`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	runs, err := c.ListRuns(context.Background(), RunFilter{
		FactoryID: "fac_b",
		Agent:     "reviewer", Status: "failed", Worker: "w1", Model: "m1",
		FailureClass: "agent", Source: "api", ParentRunID: "run_p", WorkItemID: "task_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "run_1" || runs[0].ModelID != "test-model" || runs[0].Stage != "reviewing" || runs[0].ConversationID != "conv_1" || runs[0].FailureClass != "agent" || runs[0].FailureSummary != "The agent reported the run as failed" || runs[0].QueueWaitMS != 0 || runs[0].ExecMS != 5 || runs[0].CostCents == nil || *runs[0].CostCents != 3 {
		t.Fatalf("%+v", runs)
	}
	for _, want := range []string{"factory_id=fac_b", "failure_class=agent", "source=api", "parent_run_id=run_p", "work_item_id=task_1", "agent=reviewer"} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("query %q missing %s", got, want)
		}
	}
}

func TestPollSteersUsesRunToken(t *testing.T) {
	const runToken = "run-token-not-the-api-key"
	var gotAuth, gotAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAfter = r.URL.Query().Get("after")
		if r.URL.Path != "/api/v1/agent/runs/run_1/steer" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"steer_02","kind":"followup","text":"try the other file"}]`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	steers, err := c.PollSteers(context.Background(), "run_1", runToken, "steer_01")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+runToken {
		t.Fatalf("auth %q", gotAuth)
	}
	if gotAfter != "steer_01" {
		t.Fatalf("after %q", gotAfter)
	}
	if len(steers) != 1 || steers[0].ID != "steer_02" {
		t.Fatalf("%+v", steers)
	}
	if _, err := c.PollSteers(context.Background(), "run_1", "", ""); err == nil {
		t.Fatal("missing run token")
	}
}

func TestListRunSteersAndWebhookDeliveries(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/steers") {
			_, _ = w.Write([]byte(`[{"id":"steer_1","kind":"interrupt","text":"","actor":"api-key","created_at":"2026-04-02T15:04:00Z"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"del_1","provider":"webhook","delivery_id":"d1","received_at":"2026-04-02T15:04:05Z","truncated":false,"status":"unmatched","matched_automations":[],"normalized_events":[]}]`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	rows, err := c.ListRunSteers(context.Background(), "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" || gotPath != "/api/v1/agent/runs/run_1/steers" {
		t.Fatalf("auth %q path %s", gotAuth, gotPath)
	}
	if len(rows) != 1 || rows[0].ID != "steer_1" || rows[0].Text != "" {
		t.Fatalf("%+v", rows)
	}
	deliveries, err := c.ListWebhookDeliveries(context.Background(), "fac_1", "wh_1", DeliveryFilter{Status: "unmatched", IncludeBody: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/fac_1/webhooks/wh_1/deliveries" || !strings.Contains(gotQuery, "include=body") || !strings.Contains(gotQuery, "status=unmatched") || !strings.Contains(gotQuery, "limit=5") {
		t.Fatalf("path %s query %s", gotPath, gotQuery)
	}
	if len(deliveries) != 1 || deliveries[0].ID != "del_1" || deliveries[0].NormalizedEvents == nil {
		t.Fatalf("%+v", deliveries)
	}
}

func TestConsoleMutations(t *testing.T) {
	var method, path string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		switch r.URL.Path {
		case "/api/v1/agent/runs/run_1/interrupt":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"steer_id":"steer_01"}`))
		case "/api/v1/agent/runs/run_1/followups":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"steer_id":"steer_02"}`))
		case "/api/v1/factory/fac/automations/on-label/pause":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	id, err := c.Interrupt(context.Background(), "run_1")
	if err != nil || id != "steer_01" || method != http.MethodPost {
		t.Fatalf("interrupt %s %s %v", id, method, err)
	}
	id, err = c.Followup(context.Background(), "run_1", "try the other file")
	if err != nil || id != "steer_02" {
		t.Fatalf("followup %s %v", id, err)
	}
	if !bytes.Contains(body, []byte(`"text":"try the other file"`)) {
		t.Fatalf("body %s", body)
	}
	if err := c.PauseAutomation(context.Background(), "fac", "on-label"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/api/v1/factory/fac/automations/on-label/pause" {
		t.Fatalf("%s %s", method, path)
	}
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from") != from.Format(time.RFC3339) || r.URL.Query().Get("to") != to.Format(time.RFC3339) {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		_, _ = w.Write(readConsoleGolden(t, "summary.json"))
	})
	if _, err := c.Summary(context.Background(), "fac", from, to); err != nil {
		t.Fatal(err)
	}
}

func TestReadFactoryEventsSendsCursor(t *testing.T) {
	var gotFrom, gotLast string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFrom = r.URL.Query().Get("from_seq")
		gotLast = r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 10\nevent: message.delta\ndata: {\"text\":\"hello\"}\n\n"))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	evs, err := c.ReadFactoryEvents(context.Background(), "fac", 4, "9")
	if err != nil {
		t.Fatal(err)
	}
	if gotFrom != "4" || gotLast != "9" {
		t.Fatalf("from %q last %q", gotFrom, gotLast)
	}
	if len(evs) != 1 || evs[0].ID != "10" || evs[0].Type != "message.delta" {
		t.Fatalf("%+v", evs)
	}
}

func readConsoleGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "api", "testdata", "console", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func normalizeClientJSON(t *testing.T, b []byte) []byte {
	t.Helper()
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("json: %v", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(buf.Bytes())
}
