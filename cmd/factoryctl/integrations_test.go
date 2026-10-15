package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelpListsM4Commands(t *testing.T) {
	var stdout, stderr strings.Builder
	code := execute([]string{"--help"}, &stdout, &stderr, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"deliveries list --factory NAME [--unmatched]",
		"deliveries replay DELIVERY_ID",
		"schedules list [--factory NAME]",
		"schedules pause SCHEDULE_ID",
		"schedules resume SCHEDULE_ID",
		"task answer TASK_ID --text TEXT",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q\n%s", want, out)
		}
	}
}

func TestDeliveriesListAndReplay(t *testing.T) {
	var gotQuery, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Header.Get("Authorization") != "Bearer cli-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deliveries"):
			_, _ = w.Write([]byte(`[{"id":"dlv_1","provider":"github","delivery_id":"abc","status":"unmatched","matched_automations":[]}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/deliveries/dlv_1:replay":
			_, _ = w.Write([]byte(`{"id":"dlv_1","provider":"github","delivery_id":"abc","status":"received","matched_automations":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	getenv := cliGetenv(srv.URL, "cli-key")

	var stdout, stderr strings.Builder
	code := execute([]string{"deliveries", "list", "--unmatched", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("list exit %d stderr %s", code, stderr.String())
	}
	if stdout.String() != "dlv_1 provider=github status=unmatched delivery=abc\n" {
		t.Fatalf("list stdout %q query %s", stdout.String(), gotQuery)
	}
	if !strings.Contains(gotQuery, "status=unmatched") {
		t.Fatalf("query %s", gotQuery)
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"deliveries", "replay", "dlv_1"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("replay exit %d stderr %s", code, stderr.String())
	}
	if stdout.String() != "dlv_1 received\n" || gotPath != "/api/v1/deliveries/dlv_1:replay" {
		t.Fatalf("replay stdout %q path %s", stdout.String(), gotPath)
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"deliveries", "list", "--unmatched"}, &stdout, &stderr, getenv)
	if code != 2 || !strings.Contains(stderr.String(), "--factory is required") {
		t.Fatalf("missing factory exit %d stderr %s", code, stderr.String())
	}
}

func TestSchedulesAndTaskAnswer(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/agent/schedules":
			_, _ = w.Write([]byte(`[{"id":"sch_1","factory_id":"fac_1","automation":"audit","name":"weekly","cron":"0 6 * * 1","paused":false}]`))
		case strings.HasSuffix(r.URL.Path, "/pause"):
			_, _ = w.Write([]byte(`{"id":"sch_1","factory_id":"fac_1","automation":"audit","name":"weekly","cron":"@daily","paused":true}`))
		case strings.HasSuffix(r.URL.Path, "/resume"):
			_, _ = w.Write([]byte(`{"id":"sch_1","factory_id":"fac_1","automation":"audit","name":"weekly","cron":"@daily","paused":false}`))
		case r.URL.Path == "/api/v1/work-items/wi_1/input":
			_, _ = w.Write([]byte(`{"id":"inr_1","work_item_id":"wi_1","answer":"ship it","answered_by":"api-key"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	getenv := cliGetenv(srv.URL, "cli-key")

	var stdout, stderr strings.Builder
	code := execute([]string{"schedules", "list", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("list exit %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `sch_1 automation=audit name=weekly cron="0 6 * * 1" paused=false`) {
		t.Fatalf("list %q", stdout.String())
	}

	stdout.Reset()
	code = execute([]string{"schedules", "pause", "sch_1"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "paused=true") || gotPath != "/api/v1/agent/schedules/sch_1/pause" {
		t.Fatalf("pause exit %d stdout %q path %s stderr %s", code, stdout.String(), gotPath, stderr.String())
	}
	stdout.Reset()
	code = execute([]string{"schedules", "resume", "sch_1"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "paused=false") {
		t.Fatalf("resume exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"task", "answer", "wi_1", "--text", "ship it"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("answer exit %d stderr %s", code, stderr.String())
	}
	if stdout.String() != "wi_1\n" || gotBody != `{"text":"ship it"}` || gotPath != "/api/v1/work-items/wi_1/input" {
		t.Fatalf("answer stdout %q body %s path %s", stdout.String(), gotBody, gotPath)
	}
}
