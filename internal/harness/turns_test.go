package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxTurnsFrom(t *testing.T) {
	cases := []struct {
		env     []string
		want    int
		wantErr bool
	}{
		{nil, 8, false},
		{[]string{"FACTORY_MAX_TURNS="}, 8, false},
		{[]string{"FACTORY_MAX_TURNS=40"}, 40, false},
		{[]string{"FACTORY_MAX_TURNS=1"}, 1, false},
		{[]string{"FACTORY_MAX_TURNS=200"}, 200, false},
		{[]string{"FACTORY_MAX_TURNS=0"}, 0, true},
		{[]string{"FACTORY_MAX_TURNS=201"}, 0, true},
		{[]string{"FACTORY_MAX_TURNS=abc"}, 0, true},
	}
	for _, c := range cases {
		got, err := maxTurnsFrom(c.env)
		if (err != nil) != c.wantErr || (!c.wantErr && got != c.want) {
			t.Errorf("maxTurnsFrom(%v) = %d, %v; want %d, err=%v", c.env, got, err, c.want, c.wantErr)
		}
	}
}

func TestRunInvalidMaxTurns(t *testing.T) {
	ws := t.TempDir()
	rt := t.TempDir()
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=http://127.0.0.1:9",
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=nope",
		},
		Workspace:  ws,
		RuntimeDir: rt,
		Stderr:     io.Discard,
	})
	if code != ExitConfigInvalid {
		t.Fatalf("exit %d, want %d", code, ExitConfigInvalid)
	}
	b, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one event, got %q", b)
	}
	var ev Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "terminal" || ev.Status != "config_invalid" {
		t.Fatalf("event %+v", ev)
	}
	if ev.Summary != `FACTORY_MAX_TURNS: must be an integer in 1..200, got "nope"` {
		t.Fatalf("summary %q", ev.Summary)
	}
}

func TestRunStopsAfterConfiguredTurns(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"bash","arguments":"{\"command\":\"true\"}"}}]}}]}`)
	}))
	t.Cleanup(srv.Close)

	ws := t.TempDir()
	rt := t.TempDir()
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=2",
		},
		Workspace:  ws,
		RuntimeDir: rt,
		Stderr:     io.Discard,
	})
	if code != ExitAgentReported {
		t.Fatalf("exit %d, want %d", code, ExitAgentReported)
	}
	if calls != 2 {
		t.Fatalf("model calls %d, want 2", calls)
	}
	b, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	var terminals int
	var summary string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "terminal" {
			terminals++
			summary = ev.Summary
			if ev.Status != "failed_by_agent" {
				t.Fatalf("status %q", ev.Status)
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal events %d", terminals)
	}
	if summary != "stopped after 2 model turns" {
		t.Fatalf("summary %q", summary)
	}
}
