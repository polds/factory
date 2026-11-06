package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

func TestSecretInjectionUsesResolver(t *testing.T) {
	s := &Server{
		cfg: Config{
			TaskServerURL: "http://127.0.0.1:9",
			Secrets:       map[string]string{"SCAN_GITHUB_TOKEN": "CANARY-OLD"},
		},
		loaded: loaded{tree: &definition.Tree{
			Factory: definition.Factory{Secrets: []string{"SCAN_GITHUB_TOKEN"}},
		}},
	}
	first, err := s.taskEnv(context.Background(), store.Run{ID: "run_secret", Prompt: "scan"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if first["SCAN_GITHUB_TOKEN"] != "CANARY-OLD" {
		t.Fatalf("first value %q", first["SCAN_GITHUB_TOKEN"])
	}
	s.cfg.Secrets["SCAN_GITHUB_TOKEN"] = "CANARY-NEW"
	second, err := s.taskEnv(context.Background(), store.Run{ID: "run_secret", Prompt: "scan"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if second["SCAN_GITHUB_TOKEN"] != "CANARY-NEW" {
		t.Fatalf("second value %q", second["SCAN_GITHUB_TOKEN"])
	}
	if !strings.Contains(second["FACTORY_SECRET_VALUES"], "CANARY-OLD") || !strings.Contains(second["FACTORY_SECRET_VALUES"], "CANARY-NEW") {
		t.Fatalf("rotation needles %q", second["FACTORY_SECRET_VALUES"])
	}
	s.cfg.Secrets["../x"] = "nope"
	s.loaded.tree.Factory.Secrets = []string{"../x"}
	_, err = s.taskEnv(context.Background(), store.Run{ID: "run_secret"}, boundAgent{})
	if err == nil || !strings.Contains(err.Error(), "not configured") || !strings.Contains(err.Error(), "secret_ref_rejected") {
		t.Fatalf("unsafe err %v", err)
	}

	dir := t.TempDir()
	path := dir + "/SCAN_GITHUB_TOKEN"
	if err := os.WriteFile(path, []byte("CANARY-OLD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		switch key {
		case "FACTORY_SECRET_MOUNT_DIR":
			return dir
		case "FACTORY_SECRET_ALLOWLIST":
			return "SCAN_GITHUB_TOKEN"
		default:
			return ""
		}
	}
	res, err := s.resolverFrom(getenv)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.resolveWith(context.Background(), res, "SCAN_GITHUB_TOKEN")
	if err != nil || opened != "CANARY-OLD" {
		t.Fatalf("file %q %v", opened, err)
	}
	if err := os.WriteFile(path, []byte("CANARY-NEW\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated, err := s.resolveWith(context.Background(), res, "SCAN_GITHUB_TOKEN")
	if err != nil || rotated != "CANARY-NEW" {
		t.Fatalf("reread %q %v", rotated, err)
	}
	_, err = s.resolveWith(context.Background(), res, "OPENAI_API_KEY")
	if err == nil || !strings.Contains(err.Error(), "secret_ref_rejected") {
		t.Fatalf("missing allowlist err %v", err)
	}
}

func TestScannerAssignmentReadMint(t *testing.T) {
	const minted = "ghs_test_installation_token"
	var mu sync.Mutex
	var posts int
	var perms map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":7}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
			var body struct {
				Permissions map[string]string `json:"permissions"`
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil || json.Unmarshal(raw, &body) != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			posts++
			perms = body.Permissions
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":      minted,
				"expires_at": time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := github.New(github.Config{AppID: 4242, Key: key, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg: Config{
			TaskServerURL:   "http://127.0.0.1:9",
			GitHubTokenMode: tokenModeApp,
			Secrets:         map[string]string{"GH_TOKEN": "placeholder"},
		},
		loaded: loaded{
			tree: &definition.Tree{Factory: definition.Factory{Secrets: []string{"GH_TOKEN"}}},
			byID: map[string]boundAgent{"agt_scanner": scannerBound()},
		},
		integ: m4State{githubApp: app},
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	ctx := withCheckoutOverrides(context.Background(), []protocol.RepositoryHeadOverride{{
		RepoOwner: "polds",
		RepoName:  "factory-m7-target",
		Head:      protocol.RepositoryHeadRef{Type: protocol.RepositoryHeadTypeCommitSHA, Value: sha},
	}})
	s.cfg.InferenceAPIKey = "sk-ambient-scanner"
	env, err := s.taskEnv(ctx, scannerRun(), scannerBound())
	if err != nil {
		t.Fatal(err)
	}
	if env["GH_TOKEN"] != minted {
		t.Fatal("scanner assignment did not use the read token")
	}
	if env["OPENAI_API_KEY"] != "" || strings.Contains(env["FACTORY_SECRET_VALUES"], "sk-ambient-scanner") {
		t.Fatal("ambient inference secret reached the scanner")
	}
	mu.Lock()
	gotPosts, gotPerms := posts, perms
	mu.Unlock()
	if gotPosts != 1 {
		t.Fatalf("mints %d", gotPosts)
	}
	if gotPerms["contents"] != "read" || gotPerms["metadata"] != "read" || gotPerms["pull_requests"] != "" || gotPerms["issues"] != "" {
		t.Fatalf("permissions %#v", gotPerms)
	}
	asg := s.assignment(ctx, scannerRun(), store.Execution{ID: "exec_1"}, scannerBound(), env)
	c := asg.Task.AgentConfigSnapshot.NativeConstraints
	if c == nil || c.DockerSocket || c.ForeignCheckoutWrite || c.BroadSecretMount || c.NegotiatedSteer || !c.ReadOnlyForge {
		t.Fatalf("constraints %+v", c)
	}

	mu.Lock()
	posts = 0
	mu.Unlock()
	_, err = s.taskEnv(context.Background(), scannerRun(), scannerBound())
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("unpinned err %v", err)
	}
	mu.Lock()
	if posts != 0 {
		t.Fatalf("unpinned mint posts %d", posts)
	}
	mu.Unlock()

	_, err = prepareReadMint([]string{"other/repo"}, []protocol.RepositoryHeadOverride{{
		RepoOwner: "polds",
		RepoName:  "factory-m7-target",
		Head:      protocol.RepositoryHeadRef{Type: protocol.RepositoryHeadTypeCommitSHA, Value: sha},
	}})
	if err == nil || !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("unrelated err %v", err)
	}
}

func TestClaudeCodeOmitsAmbientInference(t *testing.T) {
	s := &Server{
		cfg: Config{
			TaskServerURL:    "http://127.0.0.1:9",
			InferenceAPIKey:  "sk-ambient-claude",
			InferenceBaseURL: "http://127.0.0.1:9",
		},
	}
	env, err := s.taskEnv(context.Background(), store.Run{ID: "run_claude", Prompt: "edit"}, boundAgent{
		agent: definition.Agent{Harness: &definition.Harness{Type: "claude-code"}, MaxTurns: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["OPENAI_API_KEY"] != "" || env["OPENAI_BASE_URL"] != "" {
		t.Fatalf("ambient inference env %+v", env)
	}
}

func TestRetiredSecretStaysRedacted(t *testing.T) {
	const oldCanary = "CANARY-OLD-VALUE-ZZZZ"
	const newCanary = "CANARY-NEW-VALUE-YYYY"
	s := &Server{
		cfg: Config{
			TaskServerURL: "http://127.0.0.1:9",
			Secrets:       map[string]string{"SCAN_GITHUB_TOKEN": oldCanary},
		},
		loaded: loaded{tree: &definition.Tree{
			Factory: definition.Factory{Secrets: []string{"SCAN_GITHUB_TOKEN"}},
		}},
	}
	if _, err := s.taskEnv(context.Background(), store.Run{ID: "run_secret", Prompt: "scan"}, boundAgent{}); err != nil {
		t.Fatal(err)
	}
	s.cfg.Secrets["SCAN_GITHUB_TOKEN"] = newCanary
	second, err := s.taskEnv(context.Background(), store.Run{ID: "run_secret", Prompt: "scan"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second["FACTORY_SECRET_VALUES"], oldCanary) || !strings.Contains(second["FACTORY_SECRET_VALUES"], newCanary) {
		t.Fatalf("rotation needles %q", second["FACTORY_SECRET_VALUES"])
	}
	got := s.redact("saw " + oldCanary + " then " + newCanary)
	if strings.Contains(got, oldCanary) || strings.Contains(got, newCanary) {
		t.Fatalf("redact %q", got)
	}
	if got != "saw [REDACTED] then [REDACTED]" {
		t.Fatalf("redact %q", got)
	}
}

func TestMissingAgentDoesNotMintWriteBundle(t *testing.T) {
	const minted = "ghs_test_installation_token"
	var mu sync.Mutex
	var posts int
	var perms map[string]string
	s := githubMintServer(t, &mu, &posts, &perms, minted)
	sha := "0123456789abcdef0123456789abcdef01234567"
	ctx := withCheckoutOverrides(context.Background(), []protocol.RepositoryHeadOverride{{
		RepoOwner: "polds",
		RepoName:  "factory-m7-target",
		Head:      protocol.RepositoryHeadRef{Type: protocol.RepositoryHeadTypeCommitSHA, Value: sha},
	}})
	s.loaded.byID = map[string]boundAgent{"agt_scanner": scannerBound()}
	run := scannerRun()
	run.AgentID = "agt_retired"
	env, err := s.taskEnv(ctx, run, scannerBound())
	if err != nil {
		t.Fatal(err)
	}
	if env["GH_TOKEN"] != minted {
		t.Fatal("reloaded scanner did not mint")
	}
	mu.Lock()
	gotPosts, gotPerms := posts, perms
	mu.Unlock()
	if gotPosts != 1 || gotPerms["contents"] != "read" || gotPerms["metadata"] != "read" || gotPerms["pull_requests"] != "" || gotPerms["issues"] != "" || gotPerms["checks"] != "" {
		t.Fatalf("posts %d permissions %#v", gotPosts, gotPerms)
	}
	asg := s.assignment(ctx, run, store.Execution{ID: "exec_1"}, scannerBound(), env)
	c := asg.Task.AgentConfigSnapshot.NativeConstraints
	if c == nil || !c.ReadOnlyForge {
		t.Fatalf("constraints %+v", c)
	}

	mu.Lock()
	posts = 0
	perms = nil
	mu.Unlock()
	coding := store.Run{
		ID:      run.ID,
		AgentID: "agt_retired",
		Repo:    run.Repo,
		Prompt:  "edit",
	}
	if _, err := s.taskEnv(context.Background(), coding, boundAgent{agent: definition.Agent{Secrets: []string{"GH_TOKEN"}, MaxTurns: 1}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if posts != 1 || perms["contents"] != "write" || perms["pull_requests"] != "write" {
		t.Fatalf("coding reload posts %d permissions %#v", posts, perms)
	}
	mu.Unlock()
}

func TestScannerMintFollowsInvocationRow(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: writeM4Definition})
	ctx := context.Background()
	fac, err := env.srv.store.GetFactory(ctx, "m4")
	if err != nil {
		t.Fatal(err)
	}
	item, err := env.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID,
		Source:    store.SourceIssue,
		SourceRef: "scan-note",
		Title:     "scan",
		Stage:     store.StageTriage,
		Status:    store.WorkItemOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	ins, err := env.srv.store.InsertInvocation(ctx, store.InsertInvocationParams{
		FactoryID:           fac.ID,
		ProfileName:         "scan",
		SourceOccurrenceKey: "pin-row",
		CanonicalDigest:     "digest-pin",
		DefinitionDigest:    "def-pin",
		State:               invocation.StateAccepted,
		Intent:              "new",
		Priority:            "interactive",
		PrincipalJSON:       json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
		TargetRepository:    "polds/factory-m7-target",
		PinnedHeadSHA:       sha,
		WorkItemID:          item.ID,
	})
	if err != nil || !ins.Created {
		t.Fatalf("invocation %+v err %v", ins, err)
	}
	const minted = "ghs_test_installation_token"
	var mu sync.Mutex
	var posts int
	var perms map[string]string
	app := countingGitHub(t, &mu, &posts, &perms, minted)
	env.srv.cfg.GitHubTokenMode = tokenModeApp
	if env.srv.cfg.Secrets == nil {
		env.srv.cfg.Secrets = map[string]string{}
	}
	env.srv.cfg.Secrets["GH_TOKEN"] = "placeholder"
	env.srv.integ.githubApp = app
	run := store.Run{
		ID:         "run_pin",
		AgentID:    "agt_retired",
		Repo:       "polds/factory-m7-target",
		WorkItemID: item.ID,
		Prompt:     "scan",
	}
	got, err := env.srv.taskEnv(ctx, run, scannerBound())
	if err != nil {
		t.Fatal(err)
	}
	if got["GH_TOKEN"] != minted {
		t.Fatal("invocation row did not mint the read token")
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || perms["contents"] != "read" || perms["metadata"] != "read" || perms["pull_requests"] != "" || perms["issues"] != "" {
		t.Fatalf("posts %d permissions %#v", posts, perms)
	}
}

func githubMintServer(t *testing.T, mu *sync.Mutex, posts *int, perms *map[string]string, minted string) *Server {
	t.Helper()
	app := countingGitHub(t, mu, posts, perms, minted)
	return &Server{
		cfg: Config{
			TaskServerURL:   "http://127.0.0.1:9",
			GitHubTokenMode: tokenModeApp,
			Secrets:         map[string]string{"GH_TOKEN": "placeholder"},
		},
		loaded: loaded{tree: &definition.Tree{Factory: definition.Factory{Secrets: []string{"GH_TOKEN"}}}},
		integ:  m4State{githubApp: app},
	}
}

func countingGitHub(t *testing.T, mu *sync.Mutex, posts *int, perms *map[string]string, minted string) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":7}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
			var body struct {
				Permissions map[string]string `json:"permissions"`
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil || json.Unmarshal(raw, &body) != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			*posts++
			*perms = body.Permissions
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":      minted,
				"expires_at": time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := github.New(github.Config{AppID: 4242, Key: key, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func scannerRun() store.Run {
	return store.Run{
		ID:      "run_01M6F3TY9ZCXW7R0VKNGZQQD71",
		AgentID: "agt_scanner",
		Repo:    "polds/factory-m7-target",
		Prompt:  "scan",
	}
}

func scannerBound() boundAgent {
	return boundAgent{agent: definition.Agent{
		Harness:  &definition.Harness{Type: "scanner"},
		Secrets:  []string{"GH_TOKEN"},
		MaxTurns: 1,
	}}
}
