package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReportPostsEventsInOrderWithIndexAndAuth(t *testing.T) {
	token := "report-order-token"
	ingest, calls := newIngest(t, 0)
	code, events, _ := runWithIngest(t, ingest.URL, token, textModel("all done"), nil)
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	got := calls.snapshot()
	if len(got) == 0 {
		t.Fatal("no ingest requests")
	}
	var posted strings.Builder
	next := 0
	for i, c := range got {
		if c.Auth != "Bearer "+token {
			t.Fatalf("request %d auth %q", i, c.Auth)
		}
		if c.Index != fmt.Sprintf("%d", next) {
			t.Fatalf("request %d index %q, want %d", i, c.Index, next)
		}
		if c.Status != http.StatusOK {
			t.Fatalf("request %d status %d", i, c.Status)
		}
		posted.WriteString(c.Body)
		next += strings.Count(c.Body, "\n")
	}
	if posted.String() != events {
		t.Fatalf("posted bytes differ from events.ndjson\nposted:\n%s\nfile:\n%s", posted.String(), events)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(events), "\n") {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		types = append(types, ev.Type)
	}
	want := []string{"turn.started", "usage", "message.delta", "terminal"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("types %v, want %v", types, want)
	}
}

func TestReportRetriesSameIndexAfter500(t *testing.T) {
	token := "report-retry-token"
	ingest, calls := newIngest(t, 2)
	code, events, _ := runWithIngest(t, ingest.URL, token, textModel("retried"), nil)
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	got := calls.snapshot()
	if len(got) != 3 {
		t.Fatalf("requests %d, want 3 (two 500s then 200)", len(got))
	}
	for i, c := range got {
		if c.Index != got[0].Index {
			t.Fatalf("request %d resent index %q, first was %q", i, c.Index, got[0].Index)
		}
		if c.Body != got[0].Body {
			t.Fatalf("request %d resent a different body", i)
		}
		if c.Auth != "Bearer "+token {
			t.Fatalf("request %d auth %q", i, c.Auth)
		}
	}
	if got[0].Status != http.StatusInternalServerError || got[1].Status != http.StatusInternalServerError || got[2].Status != http.StatusOK {
		t.Fatalf("statuses %d %d %d", got[0].Status, got[1].Status, got[2].Status)
	}
	if got[2].Body != events {
		t.Fatalf("delivered body differs from events.ndjson")
	}
}

func TestReportServerDownStillExits(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ingest server was called after close")
	}))
	url := down.URL
	down.Close()

	start := time.Now()
	code, events, _ := runWithIngest(t, url, "report-down-token", textModel("done offline"), nil)
	elapsed := time.Since(start)
	if code != ExitOK {
		t.Fatalf("exit %d, want agent success", code)
	}
	if !strings.Contains(events, `"type":"terminal"`) || !strings.Contains(events, `"status":"succeeded"`) {
		t.Fatalf("events.ndjson incomplete:\n%s", events)
	}
	// Flush is capped at 10s. Slack covers scheduler delay, not the 30s retry budget.
	if elapsed > 12*time.Second {
		t.Fatalf("exit took %s, want <= 10s", elapsed)
	}
}

func TestReportEnvUnsetMakesNoHTTPCalls(t *testing.T) {
	var ingestHits atomicCount
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingestHits.add()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ingest.Close)

	t.Run("both unset", func(t *testing.T) {
		var eventPosts atomicCount
		code, events, _ := runWithIngest(t, "", "", countingModel(&eventPosts), nil)
		if code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		if eventPosts.n() != 0 {
			t.Fatalf("event posts %d", eventPosts.n())
		}
		if !strings.Contains(events, `"type":"terminal"`) {
			t.Fatalf("missing terminal:\n%s", events)
		}
	})
	t.Run("url only", func(t *testing.T) {
		before := ingestHits.n()
		code, _, _ := runWithIngest(t, ingest.URL, "", textModel("no token"), nil)
		if code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		if ingestHits.n() != before {
			t.Fatalf("ingest calls %d, want 0", ingestHits.n()-before)
		}
	})
	t.Run("token only", func(t *testing.T) {
		before := ingestHits.n()
		code, _, _ := runWithIngest(t, "", "lonely-token", textModel("no url"), nil)
		if code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		if ingestHits.n() != before {
			t.Fatalf("ingest calls %d, want 0", ingestHits.n()-before)
		}
	})
}

func TestReportTokenAbsentFromEventsAndStderr(t *testing.T) {
	token := "factory-run-token-planted"
	ingest, calls := newIngest(t, 0)
	code, events, stderr := runWithIngest(t, ingest.URL, token, textModel("finished "+token), nil)
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(events, token) || !strings.Contains(events, redacted) {
		t.Fatalf("echoed token was not redacted:\n%s", events)
	}
	if strings.Contains(stderr, token) {
		t.Fatalf("token leaked into stderr:\n%s", stderr)
	}
	for i, c := range calls.snapshot() {
		if strings.Contains(c.Body, token) {
			t.Fatalf("token leaked into ingest body %d:\n%s", i, c.Body)
		}
		if c.Auth != "Bearer "+token {
			t.Fatalf("auth header %q", c.Auth)
		}
	}
}

func TestReportStripsPushEnvFromTools(t *testing.T) {
	token := "factory-run-token-from-tool"
	eventsURL := ""
	ingest, _ := newIngest(t, 0)
	eventsURL = ingest.URL
	code, events, stderr, ws := runWithIngestWS(t, eventsURL, token, toolThenText(
		`printf 'TOKEN=%s\nURL=%s\nKEY=%s\n' "$FACTORY_RUN_TOKEN" "$FACTORY_EVENTS_URL" "$OPENAI_API_KEY" > envout.txt`,
		"tools finished",
	), []string{"OPENAI_API_KEY=sk-should-not-leak"})
	if code != ExitOK {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	body, err := os.ReadFile(filepath.Join(ws, "envout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	want := "TOKEN=\nURL=\nKEY=\n"
	if got != want {
		t.Fatalf("tool env %q, want %q", got, want)
	}
	if strings.Contains(events, token) || strings.Contains(events, eventsURL) || strings.Contains(stderr, token) {
		t.Fatalf("secret leaked events=%s stderr=%s", events, stderr)
	}
}

func TestReportSendsFullBatchWithoutWaiting(t *testing.T) {
	token := "report-batch-token"
	var mu sync.Mutex
	var bodies []string
	var indexes []string
	got := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		indexes = append(indexes, r.Header.Get("X-Factory-Event-Index"))
		n := len(bodies)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		if n == 1 {
			got <- struct{}{}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rep := startEventReporter([]string{
		"FACTORY_EVENTS_URL=" + srv.URL,
		"FACTORY_RUN_TOKEN=" + token,
	})
	if rep == nil {
		t.Fatal("nil reporter")
	}
	start := time.Now()
	for i := 0; i < 64; i++ {
		rep.push([]byte(fmt.Sprintf("{\"n\":%d}\n", i)))
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("batch of 64 was not sent")
	}
	if waited := time.Since(start); waited > 450*time.Millisecond {
		t.Fatalf("64-event batch waited %s", waited)
	}
	rep.push([]byte("{\"n\":64}\n"))
	rep.flush()

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 {
		t.Fatalf("requests %d, want a second batch after flush", len(bodies))
	}
	if indexes[0] != "0" || strings.Count(bodies[0], "\n") != 64 {
		t.Fatalf("first batch index %q lines %d", indexes[0], strings.Count(bodies[0], "\n"))
	}
	if indexes[1] != "64" || bodies[1] != "{\"n\":64}\n" {
		t.Fatalf("second batch index %q body %q", indexes[1], bodies[1])
	}
}

func TestReportSendsPartialBatchAfter500ms(t *testing.T) {
	got := make(chan time.Duration, 1)
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("X-Factory-Event-Index") != "0" {
			t.Errorf("index %s", r.Header.Get("X-Factory-Event-Index"))
		}
		select {
		case got <- time.Since(start):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rep := startEventReporter([]string{
		"FACTORY_EVENTS_URL=" + srv.URL,
		"FACTORY_RUN_TOKEN=report-wait-token",
	})
	if rep == nil {
		t.Fatal("nil reporter")
	}
	rep.push([]byte("{\"a\":1}\n"))
	select {
	case d := <-got:
		if d < 400*time.Millisecond {
			t.Fatalf("partial batch sent too soon: %s", d)
		}
		if d > 2*time.Second {
			t.Fatalf("partial batch sent too late: %s", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("partial batch was not sent")
	}
	rep.flush()
}

func TestReportPushDoesNotBlockAgentLoop(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(entered) })
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rep := startEventReporter([]string{
		"FACTORY_EVENTS_URL=" + srv.URL,
		"FACTORY_RUN_TOKEN=report-block-token",
	})
	if rep == nil {
		t.Fatal("nil reporter")
	}
	for i := 0; i < 64; i++ {
		rep.push([]byte(fmt.Sprintf("{\"n\":%d}\n", i)))
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first batch was not attempted")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			rep.push([]byte("{\"x\":1}\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("push blocked while a post was in flight")
	}
	close(release)
	rep.flush()
}

func TestReportRejectedBatchDoesNotDropTheRest(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var indexes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		idx := r.Header.Get("X-Factory-Event-Index")
		mu.Lock()
		indexes = append(indexes, idx)
		mu.Unlock()
		if idx == "0" {
			once.Do(func() { close(entered) })
			<-release
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rep := newEventReporter(srv.URL, "report-reject-token", reporterConfig{
		batchWait: 500 * time.Millisecond,
	})
	for i := 0; i < 64; i++ {
		rep.push([]byte(fmt.Sprintf("{\"n\":%d}\n", i)))
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first batch was not sent")
	}
	rep.push([]byte("{\"n\":64}\n"))
	flushed := make(chan struct{})
	go func() {
		rep.flush()
		close(flushed)
	}()
	close(release)
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("flush did not return")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(indexes) < 2 || indexes[1] != "64" {
		t.Fatalf("indexes %v, want the batch after a rejected one", indexes)
	}
}

func TestReportRetryBudgetIs30s(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(0, 0)
	var slept time.Duration
	var indexes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		indexes = append(indexes, r.Header.Get("X-Factory-Event-Index"))
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	rep := newEventReporter(srv.URL, "report-budget-token", reporterConfig{
		now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
		sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			now = now.Add(d)
			slept += d
			mu.Unlock()
			return nil
		},
		retryBudget:  30 * time.Second,
		batchWait:    0,
		flushTimeout: 10 * time.Second,
	})
	rep.push([]byte("{\"e\":1}\n"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(indexes)
		s := slept
		mu.Unlock()
		if n >= 2 && s >= 20*time.Second {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retries did not consume the budget (attempts %d, slept %s)", n, s)
		}
		time.Sleep(5 * time.Millisecond)
	}
	rep.flush()

	mu.Lock()
	defer mu.Unlock()
	if len(indexes) < 2 {
		t.Fatalf("attempts %d, want retries", len(indexes))
	}
	for i, idx := range indexes {
		if idx != "0" {
			t.Fatalf("attempt %d index %q", i, idx)
		}
	}
	if slept > 30*time.Second {
		t.Fatalf("slept %s, budget is 30s", slept)
	}
	if slept < 20*time.Second {
		t.Fatalf("slept %s, want the budget to be used", slept)
	}
}

type ingestCall struct {
	Index  string
	Auth   string
	Body   string
	Status int
}

type ingestLog struct {
	mu    sync.Mutex
	calls []ingestCall
}

func (l *ingestLog) snapshot() []ingestCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ingestCall, len(l.calls))
	copy(out, l.calls)
	return out
}

func newIngest(t *testing.T, failFirst int) (*httptest.Server, *ingestLog) {
	t.Helper()
	log := &ingestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		log.mu.Lock()
		n := len(log.calls) + 1
		if n <= failFirst {
			status = http.StatusInternalServerError
		}
		log.calls = append(log.calls, ingestCall{
			Index:  r.Header.Get("X-Factory-Event-Index"),
			Auth:   r.Header.Get("Authorization"),
			Body:   string(body),
			Status: status,
		})
		log.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

type atomicCount struct {
	mu sync.Mutex
	v  int
}

func (c *atomicCount) add() {
	c.mu.Lock()
	c.v++
	c.mu.Unlock()
}

func (c *atomicCount) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v
}

func countingModel(posts *atomicCount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Factory-Event-Index") != "" || strings.Contains(r.URL.Path, "events") {
			posts.add()
		}
		textModel("quiet")(w, r)
	}
}

func textModel(text string) http.HandlerFunc {
	payload := `{"choices":[{"message":{"role":"assistant","content":` + jsonString(text) + `}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}
}

func toolThenText(command, text string) http.HandlerFunc {
	var mu sync.Mutex
	n := 0
	tool := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":` + jsonString(`{"command":`+jsonString(command)+`}`) + `}}]}}]}`
	done := `{"choices":[{"message":{"role":"assistant","content":` + jsonString(text) + `}}]}`
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		step := n
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if step == 1 {
			_, _ = io.WriteString(w, tool)
			return
		}
		_, _ = io.WriteString(w, done)
	}
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func runWithIngest(t *testing.T, eventsURL, token string, model http.HandlerFunc, extra []string) (int, string, string) {
	t.Helper()
	code, events, stderr, _ := runWithIngestWS(t, eventsURL, token, model, extra)
	return code, events, stderr
}

func runWithIngestWS(t *testing.T, eventsURL, token string, model http.HandlerFunc, extra []string) (int, string, string, string) {
	t.Helper()
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)
	ws := t.TempDir()
	rt := t.TempDir()
	var errBuf bytes.Buffer
	env := []string{
		"OPENAI_BASE_URL=" + modelSrv.URL,
		"FACTORY_TASK_PROMPT=do the thing",
		"OPENAI_API_KEY=sk-should-not-leak",
	}
	if eventsURL != "" {
		env = append(env, "FACTORY_EVENTS_URL="+eventsURL)
	}
	if token != "" {
		env = append(env, "FACTORY_RUN_TOKEN="+token)
	}
	env = append(env, extra...)
	code := Run(context.Background(), Options{
		Args:       []string{"--task-id", "t1", "--model", "m"},
		Env:        env,
		Workspace:  ws,
		RuntimeDir: rt,
		Stderr:     &errBuf,
	})
	b, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return code, string(b), errBuf.String(), ws
}
