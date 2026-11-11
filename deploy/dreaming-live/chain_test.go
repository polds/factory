package dreaminglive

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/server"
	"github.com/polds/factory/internal/store"
)

func TestRehearsal(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" || os.Getenv("FACTORY_TEST_S3_ENDPOINT") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL and FACTORY_TEST_S3_ENDPOINT are required")
	}
	modes := []string{"good", "tiny", "allowance", "cancel", "no-sentinel", "compile"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			rehearse(t, mode)
		})
	}
}

func rehearse(t *testing.T, mode string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	tree, _, _, err := BuildSynthetic(dir)
	if err != nil {
		t.Fatal(err)
	}
	price, err := json.Marshal(workedPrice())
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if mode == "allowance" {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":{"message":"allowance"}}`)
			return
		}
		w.Header().Set("x-litellm-response-cost", "0.01")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"`+ModelID+`","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	t.Cleanup(model.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	git := newChainGitHub(t)
	boot := bootChain(t, string(price), model.URL, git.srv.URL, key)
	ceiling := CeilingCents
	if mode == "tiny" {
		ceiling = 1
	}
	before := calls
	result, err := RunChain(ctx, ChainInput{
		Server: boot.srv, Store: boot.st, DSN: boot.dsn, BaseURL: boot.base, ModelURL: model.URL,
		Price: string(price), Tree: tree, RoleDir: dir, Ceiling: ceiling, Mode: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.JoinRefused {
		t.Fatal("beatriz join was not refused")
	}
	switch mode {
	case "good":
		if !result.Published || result.X == "" {
			t.Fatalf("publish %+v", result)
		}
		if calls-before < 1 {
			t.Fatal("model was not called")
		}
		if result.Spent <= 0 || result.Spent > CeilingCents {
			t.Fatalf("spent %d", result.Spent)
		}
		if git.pulls() != 1 || !git.draft() {
			t.Fatalf("pulls %d draft %v", git.pulls(), git.draft())
		}
		refs, err := LsRemote(ctx, git.srv.URL+"/northstar-ai-labs/dreaming.git", mustHeader(t), RefPrefix)
		if err != nil || len(refs) != 1 {
			t.Fatalf("refs %v %v", refs, err)
		}
		world := secureWorld(string(readWorkflow(t)), tree, tree.SourceCommit, dir, map[string]string{})
		world.RemoteRefs = refs
		world.PullHeads = []string{RefPrefix + "second"}
		world.AuthFormOK = true
		world.LsRemoteOK = true
		world.PullsStatus = 200
		if row, _ := Evaluate(world).Row(checkPrior); row.Pass {
			t.Fatal("second dispatch was not stopped by P14")
		}
		if calls != before+1 {
			t.Fatalf("second dispatch called the model (%d)", calls-before)
		}
	default:
		if git.pulls() != 0 {
			t.Fatalf("published %d", git.pulls())
		}
	}
	if mode == "tiny" && calls != before {
		t.Fatalf("tiny ceiling sent %d calls", calls-before)
	}
	if mode == "allowance" && calls-before != 1 {
		t.Fatalf("402 calls %d", calls-before)
	}
	if mode == "cancel" && calls-before != 1 {
		t.Fatalf("cancel calls %d", calls-before)
	}
}

func mustHeader(t *testing.T) string {
	t.Helper()
	header, err := AuthHeader(chainToken)
	if err != nil {
		t.Fatal(err)
	}
	return header
}

func readWorkflow(t *testing.T) []byte {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "dreaming-live.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type chainBoot struct {
	srv  *server.Server
	st   *store.Store
	base string
	dsn  string
}

func bootChain(t *testing.T, price, modelURL, gitURL string, key *rsa.PrivateKey) chainBoot {
	t.Helper()
	ctx := context.Background()
	base := os.Getenv("FACTORY_TEST_DATABASE_URL")
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "d6c_" + hex.EncodeToString(entropy[:])
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	dsn, err := withSearch(base, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = admin.Close(context.Background())
	})
	def := writeChainDefinition(t)
	gate := make(chan struct{})
	cfg := server.Config{
		DatabaseURL: baseDSN(t, dsn), APIKey: "chain-api-key", WorkerAPIKey: "chain-worker-key",
		DefinitionPath: def, TaskServerURL: "http://127.0.0.1:1",
		InferenceBaseURL: modelURL, InferenceAPIKey: "sk-offline-rehearsal",
		SpendPrice: price, WorkerPool: "standard", WorkerPlatform: "linux",
		WorkerMaxConcurrent: 2, ScheduleGate: gate, Console: "off",
		GitHubAppID: 4242, GitHubAppKey: key, GitHubAPIBase: gitURL,
		S3: artifacts.S3{
			Endpoint: os.Getenv("FACTORY_TEST_S3_ENDPOINT"), Bucket: "factory",
			AccessKey: envOr(t, "FACTORY_TEST_S3_KEY", "factoryadmin"),
			SecretKey: envOr(t, "FACTORY_TEST_S3_SECRET", "factorysecret"),
			Region:    "us-east-1", PathStyle: true,
		},
	}
	srv, err := server.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	var handler http.Handler
	ready := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-ready
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	handler = srv.Handler()
	close(ready)
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return chainBoot{srv: srv, st: st, base: ts.URL, dsn: dsn}
}

func baseDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("pool_max_conns", "8")
	u.RawQuery = q.Encode()
	return u.String()
}

func withSearch(raw, schema string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func envOr(t *testing.T, key, fallback string) string {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func writeChainDefinition(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustFile(t, filepath.Join(root, "factory.yaml"), `
schemaVersion: v1alpha1
name: pipe
agentDefaults:
  model: auto
  runner: local
  pool: standard
pipeline:
  stages: [planning]
  agents:
    planning: worker
`)
	mustFile(t, filepath.Join(root, "agents", "worker", "agent.md"), strings.TrimSpace(`
---
description: worker does one stage.
agentType: IMPLEMENT
model: bound-model
---

Do the stage.
`)+"\n")
	mustFile(t, filepath.Join(root, "runners", "local.yaml"), `
description: Local runner
platform:
  os: linux
  arch: x86_64
`)
	return root
}

func mustFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const chainToken = "ghs_CHAIN_REHEARSAL_TOKEN_NOT_A_SECRET"

type chainGitHub struct {
	srv     *httptest.Server
	project string
	mu      sync.Mutex
	opened  int
	isDraft bool
}

func newChainGitHub(t *testing.T) *chainGitHub {
	t.Helper()
	project := t.TempDir()
	repo := filepath.Join(project, "northstar-ai-labs", "dreaming.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, project, "init", "--bare", "-b", "main", repo)
	gitCmd(t, repo, "config", "http.receivepack", "true")
	seed := t.TempDir()
	gitCmd(t, seed, "init", "-b", "main")
	gitCmd(t, seed, "config", "user.email", "dreaming@example.com")
	gitCmd(t, seed, "config", "user.name", "dreaming")
	gitCmd(t, seed, "commit", "--allow-empty", "-m", "main")
	gitCmd(t, seed, "push", repo, "HEAD:refs/heads/main")
	fake := &chainGitHub{project: project}
	fake.srv = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.srv.Close)
	return fake
}

func (f *chainGitHub) pulls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened
}

func (f *chainGitHub) draft() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.isDraft
}

func (f *chainGitHub) serve(w http.ResponseWriter, r *http.Request) {
	buf, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(buf))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming/installation":
		_, _ = io.WriteString(w, `{"id":7}`)
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"`+chainToken+`","expires_at":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming/pulls":
		if r.URL.Query().Get("state") != "all" {
			http.Error(w, "query", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		n := f.opened
		f.mu.Unlock()
		if n == 0 {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = io.WriteString(w, `[{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":true,"state":"open","head":{"ref":"factory/dreaming-454-rehearsal","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/northstar-ai-labs/dreaming/pulls":
		var body struct {
			Head  string `json:"head"`
			Draft bool   `json:"draft"`
		}
		_ = json.Unmarshal(buf, &body)
		sha := revParse(filepath.Join(f.project, "northstar-ai-labs", "dreaming.git"), "refs/heads/"+body.Head)
		f.mu.Lock()
		f.opened++
		f.isDraft = body.Draft
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":true,"state":"open","head":{"ref":%q,"sha":%q}}`, body.Head, sha)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/northstar-ai-labs/dreaming/pulls/"):
		_, _ = io.WriteString(w, `{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":true,"state":"open","head":{"ref":"factory/dreaming-454-rehearsal","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
	case strings.Contains(r.URL.Path, "/northstar-ai-labs/dreaming.git/"):
		if !chainGitAuth(r) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, server.GitBearerRejected+"\n")
			return
		}
		f.gitHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func chainGitAuth(r *http.Request) bool {
	header, err := AuthHeader(chainToken)
	if err != nil {
		return false
	}
	return r.Header.Get("Authorization") == strings.TrimPrefix(header, "Authorization: ")
}

func revParse(repo, ref string) string {
	cmd := exec.Command("git", "--git-dir", repo, "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *chainGitHub) gitHTTP(w http.ResponseWriter, r *http.Request) {
	backend := "/usr/lib/git-core/git-http-backend"
	cmd := exec.Command(backend)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_PROJECT_ROOT=" + f.project,
		"GIT_HTTP_EXPORT_ALL=1",
		"REQUEST_METHOD=" + r.Method,
		"PATH_INFO=" + r.URL.Path,
		"QUERY_STRING=" + r.URL.RawQuery,
		"CONTENT_TYPE=" + r.Header.Get("Content-Type"),
		"REMOTE_USER=git",
		"REMOTE_ADDR=127.0.0.1",
	}
	cmd.Stdin = r.Body
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		http.Error(w, "git backend", http.StatusBadGateway)
		return
	}
	raw := stdout.Bytes()
	head, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		http.Error(w, "git cgi", http.StatusBadGateway)
		return
	}
	status := http.StatusOK
	for _, line := range bytes.Split(head, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		name, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		if strings.EqualFold(string(name), "Status") {
			continue
		}
		w.Header().Add(string(bytes.TrimSpace(name)), strings.TrimSpace(string(value)))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_AUTHOR_EMAIL=dreaming@example.com", "GIT_AUTHOR_NAME=dreaming",
		"GIT_COMMITTER_EMAIL=dreaming@example.com", "GIT_COMMITTER_NAME=dreaming",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", args[0], out)
	}
}
