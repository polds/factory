package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/store"
)

// TestTaskEnvMintsTokenForRunRepository is the assignment path that m7-live
// 41530958795 failed: an AgentRun has no intake work item, so the installation
// token has to be scoped to the repository stored on the run.
func TestTaskEnvMintsTokenForRunRepository(t *testing.T) {
	const minted = "ghs_test_installation_token"
	var mu sync.Mutex
	var installPath string
	var repos []string
	var perms map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
			installPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":7}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			repos = append([]string(nil), body.Repositories...)
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

	s := appTokenServer(t, srv.URL)
	env, err := s.taskEnv(context.Background(), store.Run{
		ID:     "run_01M6F3TY9ZCXW7R0VKNGZQQD71",
		Repo:   "polds/factory-m7-target",
		Prompt: "open a pull request",
	}, boundAgent{agent: definition.Agent{Secrets: []string{"GH_TOKEN"}, MaxTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if env["GH_TOKEN"] != minted {
		t.Fatal("assignment did not use the installation token")
	}
	mu.Lock()
	defer mu.Unlock()
	if installPath != "/repos/polds/factory-m7-target/installation" {
		t.Fatalf("installation path %s", installPath)
	}
	if len(repos) != 1 || repos[0] != "factory-m7-target" {
		t.Fatalf("token repositories %#v", repos)
	}
	want := github.DefaultPermissions()
	if len(perms) != len(want) {
		t.Fatalf("permissions %#v", perms)
	}
	for name, level := range want {
		if perms[name] != level {
			t.Fatalf("permission %s=%s", name, perms[name])
		}
	}
}

// TestTaskEnvAppTokenRequiresRepository fails closed when the run has no
// owner/name. A run-scoped ref is not a repository.
func TestTaskEnvAppTokenRequiresRepository(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	s := appTokenServer(t, srv.URL)
	for _, repo := range []string{"", "m7-run/ci-41530958795-1/main"} {
		_, err := s.taskEnv(context.Background(), store.Run{
			ID:     "run_01M6F3TY9ZCXW7R0VKNGZQQD71",
			Repo:   repo,
			Prompt: "open a pull request",
		}, boundAgent{agent: definition.Agent{Secrets: []string{"GH_TOKEN"}, MaxTurns: 1}})
		if err == nil || !strings.Contains(err.Error(), "no repository") {
			t.Fatalf("repo %q err %v", repo, err)
		}
	}
	if hits != 0 {
		t.Fatalf("github calls %d", hits)
	}
}

func appTokenServer(t *testing.T, base string) *Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := github.New(github.Config{AppID: 4242, Key: key, BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
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
