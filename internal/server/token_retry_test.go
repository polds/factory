package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/store"
)

func TestRetryTransientBackoff(t *testing.T) {
	var now time.Time
	var waits []time.Duration
	s := &Server{
		log: slog.New(slog.DiscardHandler),
		cfg: Config{
			Now: func() time.Time { return now },
			TokenRetrySleep: func(_ context.Context, d time.Duration) error {
				waits = append(waits, d)
				now = now.Add(d)
				return nil
			},
		},
	}
	dns := &net.DNSError{Err: "no such host", Name: "api.github.com", IsNotFound: true}
	calls := 0
	env, err := s.retryTransient(context.Background(), "run_1", func() (map[string]string, error) {
		calls++
		if calls < 3 {
			return nil, dns
		}
		return map[string]string{"GH_TOKEN": "ghs_ok"}, nil
	})
	if err != nil || env["GH_TOKEN"] != "ghs_ok" || calls != 3 {
		t.Fatalf("env %v calls %d err %v", env, calls, err)
	}
	if len(waits) != 2 || waits[0] != 100*time.Millisecond || waits[1] != 200*time.Millisecond {
		t.Fatalf("waits %v", waits)
	}
	if now.Sub(time.Time{}) != 300*time.Millisecond {
		t.Fatalf("clock %s", now)
	}

	now = time.Time{}
	waits = nil
	calls = 0
	auth := &github.StatusError{Method: "POST", Path: "/access_tokens", StatusCode: http.StatusUnauthorized, Detail: "Bad credentials"}
	_, err = s.retryTransient(context.Background(), "run_1", func() (map[string]string, error) {
		calls++
		return nil, auth
	})
	if err == nil || calls != 1 || len(waits) != 0 || github.Transient(err) {
		t.Fatalf("auth calls %d waits %v err %v", calls, waits, err)
	}
	var status *github.StatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusUnauthorized {
		t.Fatalf("auth type %v", err)
	}

	calls = 0
	_, err = s.retryTransient(context.Background(), "run_1", func() (map[string]string, error) {
		calls++
		return nil, dns
	})
	if !github.Transient(err) || calls != tokenRetryAttempts {
		t.Fatalf("exhausted calls %d err %v", calls, err)
	}
	if len(waits) != tokenRetryAttempts-1 || waits[0] != 100*time.Millisecond || waits[2] != 400*time.Millisecond {
		t.Fatalf("exhausted waits %v", waits)
	}
}

func TestAssignLeavesQueuedOnTransientToken(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: writeM4Definition})
	ctx := context.Background()
	var waits []time.Duration
	var mu sync.Mutex
	env.srv.cfg.GitHubTokenMode = tokenModeApp
	env.srv.cfg.TokenRetrySleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return nil
	}
	app := &flakyApp{err: &net.DNSError{Err: "no such host", Name: "api.github.com", IsNotFound: true}}
	env.srv.integ.githubApp = app
	run := env.tokenRun(t)
	env.srv.assignOne(ctx, run, "local-1", boundAgent{})
	if app.calls() != tokenRetryAttempts {
		t.Fatalf("token calls %d", app.calls())
	}
	mu.Lock()
	gotWaits := append([]time.Duration(nil), waits...)
	mu.Unlock()
	if len(gotWaits) != tokenRetryAttempts-1 || gotWaits[0] != 100*time.Millisecond || gotWaits[1] != 200*time.Millisecond {
		t.Fatalf("waits %v", gotWaits)
	}
	loaded, err := env.srv.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != store.StatusQueued || loaded.FailureReason != "" {
		t.Fatalf("run %+v", loaded)
	}
	var execs int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM executions WHERE run_id = $1`, run.ID).Scan(&execs); err != nil {
		t.Fatal(err)
	}
	if execs != 0 {
		t.Fatalf("executions %d", execs)
	}
	logs := env.logs.String()
	if !strings.Contains(logs, "github token retry") || !strings.Contains(logs, "github token unavailable") {
		t.Fatalf("logs %s", logs)
	}
}

func TestAssignFailsAuthTokenOnce(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: writeM4Definition})
	ctx := context.Background()
	var waits []time.Duration
	env.srv.cfg.GitHubTokenMode = tokenModeApp
	env.srv.cfg.TokenRetrySleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	app := &flakyApp{err: &github.StatusError{
		Method:     "POST",
		Path:       "/app/installations/7/access_tokens",
		StatusCode: http.StatusUnauthorized,
		Detail:     "Bad credentials",
	}}
	env.srv.integ.githubApp = app
	run := env.tokenRun(t)
	env.srv.assignOne(ctx, run, "local-1", boundAgent{})
	if app.calls() != 1 || len(waits) != 0 {
		t.Fatalf("calls %d waits %v", app.calls(), waits)
	}
	loaded, err := env.srv.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != store.StatusFailed || loaded.FailureReason != "config_invalid" {
		t.Fatalf("run %+v", loaded)
	}
}

func (e *env) tokenRun(t *testing.T) store.Run {
	t.Helper()
	ctx := context.Background()
	fac, err := e.srv.store.GetFactory(ctx, "m4")
	if err != nil {
		t.Fatal(err)
	}
	item, err := e.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID,
		Source:    store.SourceIssue,
		SourceRef: "acme/payments#83",
		Title:     "review",
		Stage:     store.StageTriage,
		Status:    store.WorkItemOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.srv.store.CreateRun(ctx, store.NewRun{
		FactoryID:  fac.ID,
		Pool:       "standard",
		Priority:   store.PriorityPipeline,
		Prompt:     "review the change",
		Title:      "review",
		WorkItemID: item.ID,
		Stage:      store.StageReviewing,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

type flakyApp struct {
	mu  sync.Mutex
	n   int
	err error
}

func (f *flakyApp) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *flakyApp) InstallationByRepo(context.Context, string) (int64, error) {
	f.mu.Lock()
	f.n++
	err := f.err
	f.mu.Unlock()
	return 0, err
}

func (f *flakyApp) InstallationToken(context.Context, int64, []string, map[string]string) (github.Token, error) {
	return github.Token{}, f.err
}

func (f *flakyApp) CreateCheckRun(context.Context, github.Token, string, string, github.CheckRun) (int64, error) {
	return 0, nil
}

func (f *flakyApp) UpdateCheckRun(context.Context, github.Token, string, int64, github.CheckRun) error {
	return nil
}

func (f *flakyApp) UpsertIssueComment(context.Context, github.Token, string, int, string, string) error {
	return nil
}
