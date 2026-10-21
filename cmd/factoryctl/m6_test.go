package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestM6HelpListsCommands(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := execute([]string{"--help"}, &stdout, &stderr, func(string) string { return "" }); code != 0 {
		t.Fatalf("exit %d %s", code, stderr.String())
	}
	for _, line := range []string{
		"scorers list --factory NAME",
		"scorers results --factory NAME SCORER",
		"runs score RUN_ID --scorer NAME",
		"runs capture-task RUN_ID --suite SUITE --name NAME",
		"benchmarks list --factory NAME",
		"benchmarks run --factory NAME SUITE",
		"benchmarks trials --factory NAME SUITE",
		"improvement batches --factory NAME",
		"improvement run-now --factory NAME",
		"metrics --factory NAME",
	} {
		if !strings.Contains(stdout.String(), line) {
			t.Fatalf("help missing %q\n%s", line, stdout.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	execute([]string{"runs", "--help"}, &stdout, &stderr, func(string) string { return "" })
	usage := stdout.String() + stderr.String()
	if !strings.Contains(usage, "score RUN_ID --scorer NAME") || !strings.Contains(usage, "scores RUN_ID") || !strings.Contains(usage, "capture-task RUN_ID --suite SUITE --name NAME") {
		t.Fatalf("runs help %s", usage)
	}
}

func TestM6CommandsAgainstUnwiredServer(t *testing.T) {
	const key = "cli-key-not-for-output"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"title":"Not Implemented","status":501,"detail":"measure backend not wired ` + key + `"}`))
	}))
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return key
		default:
			return ""
		}
	}
	commands := [][]string{
		{"scorers", "list", "--factory", "fac_1"},
		{"scorers", "results", "--factory", "fac_1", "correctness"},
		{"runs", "score", "run_1", "--scorer", "correctness"},
		{"runs", "scores", "run_1"},
		{"runs", "capture-task", "run_1", "--suite", "pr-review", "--name", "broken-doc-link"},
		{"benchmarks", "list", "--factory", "fac_1"},
		{"benchmarks", "run", "--factory", "fac_1", "pr-review", "--repetitions", "5"},
		{"benchmarks", "trials", "--factory", "fac_1", "pr-review", "--benchmark-run-id", "br_1"},
		{"improvement", "batches", "--factory", "fac_1"},
		{"improvement", "run-now", "--factory", "fac_1"},
		{"metrics", "--factory", "fac_1"},
	}
	for _, args := range commands {
		var stdout, stderr strings.Builder
		code := execute(args, &stdout, &stderr, getenv)
		if code == 0 {
			t.Fatalf("%v exited 0 stdout %s", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "not available on this factoryd") {
			t.Fatalf("%v stderr %s", args, stderr.String())
		}
		if strings.Contains(stdout.String(), key) || strings.Contains(stderr.String(), key) {
			t.Fatalf("%v printed the api key\n%s%s", args, stdout.String(), stderr.String())
		}
	}
}

func TestM6CommandsPrintResults(t *testing.T) {
	const yaml = "title: Fix a broken documentation link\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusOK
		var body string
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/factory/fac_1/scorers":
			body = `[{"name":"correctness","agents":["reviewer"],"labels":["missed_tests"],"passing_score":0.5,"sampling_rate":25,"model":"judge","self_improvement":false,"pass_rate_by_day":[]}]`
		case "GET /api/v1/factory/fac_1/scorers/correctness/results":
			body = `[{"id":"sr_1","run_id":"run_1","label":"missed_tests","score":0.25,"passed":false,"reasoning":"shown","status":"ok","created_at":"2026-04-02T16:00:00Z"}]`
		case "POST /api/v1/agent/runs/run_1/score":
			status = http.StatusAccepted
			body = `{"result_id":"sr_1","judge_run_id":"run_judge_1"}`
		case "GET /api/v1/agent/runs/run_1/scores":
			body = `[{"scorer":"correctness","label":"pass","score":1,"passed":true,"reasoning":"SECRET-REASONING","status":"ok"},{"scorer":"tests-run","label":"tests_skipped","score":0,"passed":false,"reasoning":"also-secret","status":"ok"}]`
		case "POST /api/v1/agent/runs/run_1:capture-benchmark-task":
			body = `{"path":"benchmarks/pr-review/tasks/broken-doc-link.yaml","content":"title: Fix a broken documentation link\n","head_refs":[{"host":"github.com","owner":"acme","repo":"payments-docs","sha":"0123456789abcdef0123456789abcdef01234567"}]}`
		case "GET /api/v1/factory/fac_1/benchmarks":
			body = `[{"suite":"pr-review","agent":"reviewer","configurations":["Baseline"],"tasks":["broken-doc-link"],"last_run":{"id":"br_1","created_at":"2026-04-02T17:00:00Z","repetitions":5}}]`
		case "POST /api/v1/factory/fac_1/benchmarks/pr-review:run":
			status = http.StatusAccepted
			body = `{"benchmark_run_id":"br_1"}`
		case "GET /api/v1/factory/fac_1/benchmarks/pr-review/trials":
			body = `{"cells":[{"configuration":"Candidate","task":"broken-doc-link","n":2,"pass":1,"errored":0,"pass_rate":0.5,"ci_low":0.25,"ci_high":0.75,"median_cost_cents":12,"median_duration_ms":45000,"tokens_by_model":[]}],"report":{"winner":"Candidate","reason":"higher pass rate"}}`
		case "GET /api/v1/factory/fac_1/improvement-batches":
			body = `[{"id":"ib_1","agent":"reviewer","status":"pr_open","failures":[{"run_id":"run_1","result_id":"sr_1","scorer":"correctness","label":"missed_tests"}],"pr_url":"https://github.com/acme/factory/pull/42","created_at":"2026-04-02T18:00:00Z"}]`
		case "POST /api/v1/factory/fac_1/improvement:run-now":
			status = http.StatusAccepted
			body = `{"checked_agents":["reviewer"],"batches":[{"id":"ib_1","agent":"reviewer","status":"pr_open","failures":[],"pr_url":"","created_at":"2026-04-02T18:00:00Z"}]}`
		case "GET /api/v1/factory/fac_1/metrics":
			body = `{"total_runs":{"by_agent":[],"by_status":[],"by_source":[],"by_model":[],"measurement":1},"prs_opened":2,"prs_merged":1,"autonomy":{"share":0.5,"n":2},"cycle_time":{"p50_ms":1,"stages":[]},"cost_per_pr":{"median_cents":40,"by_size":{"S":1,"M":2,"L":3,"XL":4}},"most_expensive_prs":[],"scorer_cards":[],"improvement_prs":[]}`
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
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
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"scorers", "list", "--factory", "fac_1"}, "correctness agents=reviewer"},
		{[]string{"scorers", "results", "--factory", "fac_1", "correctness", "--passed", "false"}, "sr_1 run=run_1"},
		{[]string{"runs", "score", "run_1", "--scorer", "correctness"}, "sr_1 judge_run=run_judge_1"},
		{[]string{"runs", "scores", "run_1"}, "correctness pass 1 true ok\ntests-run tests_skipped 0 false ok\n"},
		{[]string{"runs", "capture-task", "run_1", "--suite", "pr-review", "--name", "broken-doc-link"}, yaml},
		{[]string{"benchmarks", "list", "--factory", "fac_1"}, "pr-review agent=reviewer"},
		{[]string{"benchmarks", "run", "--factory", "fac_1", "pr-review", "--repetitions", "5"}, "br_1\n"},
		{[]string{"benchmarks", "trials", "--factory", "fac_1", "pr-review", "--benchmark-run-id", "br_1"}, "winner=Candidate"},
		{[]string{"improvement", "batches", "--factory", "fac_1"}, "ib_1 agent=reviewer status=pr_open"},
		{[]string{"improvement", "run-now", "--factory", "fac_1"}, "checked=reviewer"},
		{[]string{"metrics", "--factory", "fac_1", "--from", "2026-04-01T00:00:00Z"}, "prs_opened=2 prs_merged=1"},
	}
	for _, tc := range cases {
		var stdout, stderr strings.Builder
		code := execute(tc.args, &stdout, &stderr, getenv)
		if code != 0 {
			t.Fatalf("%v exit %d stderr %s", tc.args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), tc.want) {
			t.Fatalf("%v stdout %q missing %q", tc.args, stdout.String(), tc.want)
		}
		if strings.Contains(stdout.String()+stderr.String(), "cli-key") {
			t.Fatalf("%v printed the api key", tc.args)
		}
		if strings.Contains(stdout.String()+stderr.String(), "SECRET-REASONING") || strings.Contains(stdout.String(), "also-secret") {
			t.Fatalf("%v printed reasoning %q", tc.args, stdout.String())
		}
	}
	var stdout, stderr strings.Builder
	code := execute([]string{"runs", "capture-task", "run_1", "--suite", "pr-review", "--name", "broken-doc-link"}, &stdout, &stderr, getenv)
	if code != 0 || stdout.String() != yaml {
		t.Fatalf("capture exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}
}
