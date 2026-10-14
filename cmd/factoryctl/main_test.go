package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWatchPrintsIDThenTranscript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cli-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/run":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"run_cli","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/runs/run_cli/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "id: 1\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"hello\"}\n\n")
			_, _ = fmt.Fprintf(w, "id: 2\nevent: terminal\ndata: {\"status\":\"succeeded\",\"summary\":\"done\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr strings.Builder
	getenv := func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return "cli-key"
		default:
			return ""
		}
	}
	code := execute([]string{"run", "--prompt", "x", "--watch"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	out := stdout.String()
	idAt := strings.Index(out, "run_cli")
	lineAt := strings.Index(out, "message.delta role=assistant text=hello")
	if idAt < 0 || lineAt < 0 || idAt > lineAt {
		t.Fatalf("stdout %q", out)
	}
	if strings.Contains(out, "cli-key") || strings.Contains(stderr.String(), "cli-key") {
		t.Fatal("api key leaked")
	}
}

func TestRunForwardsFlags(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("json: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"run_flags","status":"queued"}`))
	}))
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return "cli-key"
		default:
			return ""
		}
	}
	var stdout, stderr strings.Builder
	code := execute([]string{"run", "--factory", "acme", "--agent", "implementer", "--title", "T", "--prompt", "x"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.String() != "run_flags\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
	if body["factory"] != "acme" || body["agent"] != "implementer" || body["title"] != "T" || body["prompt"] != "x" {
		t.Fatalf("body %#v", body)
	}
}

func TestWatchFromSeqArg(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("from_seq")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "id: 9\nevent: terminal\ndata: {\"status\":\"cancelled\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return "cli-key"
		default:
			return ""
		}
	}
	var stdout, stderr strings.Builder
	code := execute([]string{"watch", "run_cli", "--from-seq", "8"}, &stdout, &stderr, getenv)
	if code != 2 {
		t.Fatalf("exit %d stderr %s stdout %s", code, stderr.String(), stdout.String())
	}
	if got != "8" {
		t.Fatalf("from_seq %q", got)
	}
	if !strings.Contains(stdout.String(), "terminal status=cancelled") {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/agent/runs/run_cli/cancel" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer cli-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run_cli","status":"cancelled"}`))
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr strings.Builder
	getenv := func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return "cli-key"
		default:
			return ""
		}
	}
	code := execute([]string{"cancel", "run_cli"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_cli") || !strings.Contains(stdout.String(), "cancelled") {
		t.Fatalf("stdout %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "cli-key") || strings.Contains(stderr.String(), "cli-key") {
		t.Fatal("api key leaked")
	}
}

func TestHarnessVerifyStillWired(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "not-harness")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := execute([]string{"harness", "verify", "--bin", bin}, &stdout, &stderr, os.Getenv)
	if code != 1 {
		t.Fatalf("exit %d stdout %s stderr %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "harness verify") {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestHarnessVerifyRequiresBin(t *testing.T) {
	var stdout, stderr strings.Builder
	code := execute([]string{"harness", "verify"}, &stdout, &stderr, os.Getenv)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "--bin is required") {
		t.Fatalf("stderr %q", stderr.String())
	}
}
