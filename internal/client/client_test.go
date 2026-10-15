package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartRunSendsBearerAndJSON(t *testing.T) {
	var gotAuth, gotType, gotPath, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"run_abc","status":"queued"}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	run, err := c.StartRun(context.Background(), RunRequest{
		Factory:  "acme",
		Agent:    "implementer",
		Prompt:   "fix the test",
		Title:    "T",
		Priority: "interactive",
		Repo:     "polds/factory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "run_abc" || run.Status != "queued" {
		t.Fatalf("run %+v", run)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/agent/run" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization %q", gotAuth)
	}
	if !strings.Contains(gotType, "application/json") {
		t.Fatalf("content-type %q", gotType)
	}
	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"factory":  "acme",
		"agent":    "implementer",
		"prompt":   "fix the test",
		"title":    "T",
		"priority": "interactive",
		"repo":     "polds/factory",
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%s]=%q want %q (full %s)", k, body[k], v, gotBody)
		}
	}
	if strings.Contains(string(gotBody), "test-key") {
		t.Fatal("token leaked into body")
	}
}

func TestRunDecodesStageLinkage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Run
	}{
		{
			name: "stage run",
			body: `{"id":"run_child","status":"queued","work_item_id":"wi_1","parent_run_id":"run_parent","conversation_id":"conv_1","stage":"building","model_id":"claude-x"}`,
			want: Run{
				ID:             "run_child",
				Status:         "queued",
				WorkItemID:     "wi_1",
				ParentRunID:    "run_parent",
				ConversationID: "conv_1",
				Stage:          "building",
				ModelID:        "claude-x",
			},
		},
		{
			name: "bare run",
			body: `{"id":"run_bare","status":"queued"}`,
			want: Run{ID: "run_bare", Status: "queued"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)

			c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
			got, err := c.StartRun(context.Background(), RunRequest{Prompt: "x"})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("run %+v want %+v", got, tt.want)
			}
		})
	}
}

func TestStartRunProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"prompt is required"}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	_, err := c.StartRun(context.Background(), RunRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "prompt is required") || strings.Contains(msg, "test-key") {
		t.Fatalf("error %q", msg)
	}
}

func TestCancelRun(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run_abc","status":"cancelled"}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	run, err := c.CancelRun(context.Background(), "run_abc")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "run_abc" || run.Status != "cancelled" {
		t.Fatalf("run %+v", run)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/agent/runs/run_abc/cancel" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization %q", gotAuth)
	}
}
