package dreaminglive

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"`+ModelID+`"}]}`)
			return
		}
		if r.URL.Path == "/model/info" {
			_, _ = io.WriteString(w, modelInfoBody())
			return
		}
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
		secondDispatch(t, ctx, dir, tree.SourceCommit, git, key, model.URL, &calls, before+1)
	default:
		if git.pulls() != 0 {
			t.Fatalf("published %d", git.pulls())
		}
	}
	if mode == "tiny" && calls != before {
		t.Fatalf("tiny ceiling sent %d calls", calls-before)
	}
	if mode == "allowance" && (calls-before != 1 || result.Published) {
		t.Fatalf("402 calls %d published %v", calls-before, result.Published)
	}
	if mode == "cancel" && (calls-before != 1 || result.Published || git.pulls() != 0) {
		t.Fatalf("cancel calls %d published %v pulls %d", calls-before, result.Published, git.pulls())
	}
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
	srv           *httptest.Server
	project       string
	mu            sync.Mutex
	opened        int
	isDraft       bool
	headRef       string
	headSHA       string
	mismatch      bool
	perms         map[string]string
	responsePerms map[string]string
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
		_, _ = io.WriteString(w, `{"id":7,"app_slug":"m6-live","repository_selection":"selected","permissions":{"contents":"write","pull_requests":"write","metadata":"read"}}`)
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
		var asked struct {
			Permissions map[string]string `json:"permissions"`
		}
		_ = json.Unmarshal(buf, &asked)
		f.mu.Lock()
		f.perms = asked.Permissions
		granted := asked.Permissions
		if f.responsePerms != nil {
			granted = f.responsePerms
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		perms, _ := json.Marshal(granted)
		_, _ = fmt.Fprintf(w, `{"token":%q,"expires_at":%q,"permissions":%s,"repositories":[{"name":"dreaming"}]}`, chainToken, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), perms)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming/pulls":
		if r.URL.Query().Get("state") != "all" {
			http.Error(w, "query", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		ref, sha, n := f.headRef, f.headSHA, f.opened
		f.mu.Unlock()
		if n == 0 {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":true,"state":"open","head":{"ref":%q,"sha":%q}}]`, ref, sha)
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
		f.headRef = body.Head
		f.headSHA = sha
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":true,"state":"open","head":{"ref":%q,"sha":%q}}`, body.Head, sha)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/northstar-ai-labs/dreaming/pulls/"):
		f.mu.Lock()
		ref, sha, bad := f.headRef, f.headSHA, f.mismatch
		f.mu.Unlock()
		if bad {
			sha = strings.Repeat("c", 40)
		}
		_, _ = fmt.Fprintf(w, `{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":true,"state":"open","head":{"ref":%q,"sha":%q}}`, ref, sha)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming/issues/454":
		_, _ = io.WriteString(w, `{"number":454,"state":"open","title":"DB-backed tenant isolation"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming":
		_, _ = io.WriteString(w, `{"default_branch":"main"}`)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commits/"):
		_, _ = io.WriteString(w, `{"sha":"ok"}`)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
		_, _ = io.WriteString(w, `{"status":"ahead"}`)
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

func TestCancelStopsCallsAndPublication(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" || os.Getenv("FACTORY_TEST_S3_ENDPOINT") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL and FACTORY_TEST_S3_ENDPOINT are required")
	}
	rehearse(t, "cancel")
}

func TestCancelRemovedPublishes(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" || os.Getenv("FACTORY_TEST_S3_ENDPOINT") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL and FACTORY_TEST_S3_ENDPOINT are required")
	}
	rehearseSkip(t)
}

func TestOpenDraftMismatchIsFailed(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" || os.Getenv("FACTORY_TEST_S3_ENDPOINT") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL and FACTORY_TEST_S3_ENDPOINT are required")
	}
	ctx := context.Background()
	dir := t.TempDir()
	tree, _, _, err := BuildSynthetic(dir)
	if err != nil {
		t.Fatal(err)
	}
	price, _ := json.Marshal(workedPrice())
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-litellm-response-cost", "0.01")
		_, _ = io.WriteString(w, `{"model":"`+ModelID+`","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	t.Cleanup(model.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	git := newChainGitHub(t)
	git.mismatch = true
	boot := bootChain(t, string(price), model.URL, git.srv.URL, key)
	result, err := RunChain(ctx, ChainInput{
		Server: boot.srv, Store: boot.st, DSN: boot.dsn, BaseURL: boot.base, ModelURL: model.URL,
		Price: string(price), Tree: tree, RoleDir: dir, Ceiling: CeilingCents, Mode: "good",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Published {
		t.Fatal("mismatched pull was published")
	}
	var state string
	if err := pgxQuery(t, boot.dsn, `SELECT state FROM draft_publications LIMIT 1`, &state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("row %s", state)
	}
}

func pgxQuery(t *testing.T, dsn, query string, dest *string) error {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	return conn.QueryRow(context.Background(), query).Scan(dest)
}

func rehearseSkip(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	tree, _, _, err := BuildSynthetic(dir)
	if err != nil {
		t.Fatal(err)
	}
	price, _ := json.Marshal(workedPrice())
	var calls int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("x-litellm-response-cost", "0.01")
		_, _ = io.WriteString(w, `{"model":"`+ModelID+`","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	t.Cleanup(model.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	git := newChainGitHub(t)
	boot := bootChain(t, string(price), model.URL, git.srv.URL, key)
	result, err := RunChain(ctx, ChainInput{
		Server: boot.srv, Store: boot.st, DSN: boot.dsn, BaseURL: boot.base, ModelURL: model.URL,
		Price: string(price), Tree: tree, RoleDir: dir, Ceiling: CeilingCents, Mode: "cancel", SkipCancel: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 || !result.Published || git.pulls() != 1 {
		t.Fatalf("mutation did not publish: calls %d published %v pulls %d", calls, result.Published, git.pulls())
	}
}

func modelInfoBody() string {
	return `{
	  "data": [{
	    "model_name": "` + ModelID + `",
	    "litellm_params": {"fallbacks": ["fallback"]},
	    "model_info": {
	      "input_cost_per_token": 0.000003,
	      "output_cost_per_token": 0.000015,
	      "cache_creation_input_token_cost": 0.00000375
	    }
	  }, {
	    "model_name": "fallback",
	    "model_info": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015}
	  }]
	}`
}

func secondDispatch(t *testing.T, ctx context.Context, role, pin string, git *chainGitHub, key *rsa.PrivateKey, model string, calls *int, want int) {
	t.Helper()
	repo := t.TempDir()
	gitCmd(t, repo, "init", "-b", "main")
	gitCmd(t, repo, "config", "user.email", "dreaming@example.com")
	gitCmd(t, repo, "config", "user.name", "dreaming")
	var shas []string
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(repo, "f"), []byte(fmt.Sprintf("c%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, repo, "add", "f")
		gitCmd(t, repo, "commit", "-m", fmt.Sprintf("c%d", i))
		out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		shas = append(shas, strings.TrimSpace(string(out)))
	}
	_, _, blobs, err := LoadTree(role)
	if err != nil {
		t.Fatal(err)
	}
	rawBlobs, _ := json.Marshal(blobs)
	job := t.TempDir()
	jobPath := filepath.Join(job, "job.json")
	if err := os.WriteFile(jobPath, []byte(`{"tests":{"TestProducedHeadReviewerPipeline":"pass","TestDraftPublishRequiresReviewerPass":"pass","TestRunSpendHardStop":"pass","TestHarnessSecretsNotInProc":"pass"},"allowlist":"pass","reexec":true,"hits":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	t.Setenv("FACTORY_GITHUB_API_BASE", git.srv.URL)
	t.Setenv("M6_GITHUB_APP_ID", "4242")
	t.Setenv("M6_GITHUB_APP_PRIVATE_KEY", pemText)
	t.Setenv("M1_OPENAI_BASE_URL", model)
	t.Setenv("M1_OPENAI_API_KEY", "sk-offline-rehearsal")
	t.Setenv("FACTORY_ROLE_SOURCE", role)
	t.Setenv("ROLE_BLOBS", string(rawBlobs))
	t.Setenv("DREAMING_PIN", pin)
	t.Setenv("CONFIRM", ConfirmValue)
	t.Setenv("FACTORY_SHA", shas[2])
	t.Setenv("FACTORY_GIT_DIR", repo)
	t.Setenv("FACTORY_REQUIRED_SHAS", strings.Join(shas, ","))
	t.Setenv("PREFLIGHT_JOB_FILE", jobPath)
	t.Setenv("FACTORY_SECRET_NAMES", "M1_OPENAI_BASE_URL,M1_OPENAI_API_KEY,M6_GITHUB_APP_ID,M6_GITHUB_APP_PRIVATE_KEY")
	t.Cleanup(func() { _ = os.Remove("preflight.json") })
	prev := runLiveChain
	runLiveChain = func(context.Context) error {
		*calls++
		return nil
	}
	t.Cleanup(func() { runLiveChain = prev })
	if err := RunChecked(ctx, "live"); err == nil {
		t.Fatal("second live dispatch passed preflight")
	}
	if *calls != want {
		t.Fatalf("second dispatch model calls changed: %d want %d", *calls, want)
	}
	t.Setenv("FACTORY_SKIP_P14", "1")
	if err := RunChecked(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	if *calls != want+1 {
		t.Fatalf("bypassing P14 did not run the chain: calls %d", *calls)
	}
}

func TestCheckoutAtPin(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	gitCmd(t, src, "init", "-b", "main")
	gitCmd(t, src, "config", "user.email", "dreaming@example.com")
	gitCmd(t, src, "config", "user.name", "dreaming")
	if err := os.WriteFile(filepath.Join(src, "role.txt"), []byte("seat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", "role.txt")
	gitCmd(t, src, "commit", "-m", "pin")
	out, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	pin := strings.TrimSpace(string(out))
	parent := t.TempDir()
	bare := filepath.Join(parent, "dreaming.git")
	gitCmd(t, parent, "clone", "--bare", src, bare)
	dest := filepath.Join(t.TempDir(), "dreaming-src")
	header, err := AuthHeader("ghs_CHECKOUT_TEST_TOKEN_NOT_A_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	remote := "file://" + bare
	if err := checkoutDreaming(ctx, header, remote, dest, pin); err != nil {
		t.Fatal(err)
	}
	head, err := gitHead(dest)
	if err != nil {
		t.Fatal(err)
	}
	if head != pin {
		t.Fatalf("HEAD %s pin %s", head, pin)
	}
	body, err := os.ReadFile(filepath.Join(dest, "role.txt"))
	if err != nil || string(body) != "seat\n" {
		t.Fatalf("checkout body %q %v", body, err)
	}
	if err := checkoutDreaming(ctx, header, remote, dest, pin); err != nil {
		t.Fatal(err)
	}
}

func TestGatherPassesAndRunsChain(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" || os.Getenv("FACTORY_TEST_S3_ENDPOINT") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL and FACTORY_TEST_S3_ENDPOINT are required")
	}
	ctx := context.Background()
	dir := t.TempDir()
	_, pin, blobs, err := BuildSynthetic(dir)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"`+ModelID+`"}]}`)
			return
		}
		if r.URL.Path == "/model/info" {
			_, _ = io.WriteString(w, modelInfoBody())
			return
		}
		calls++
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
	base := os.Getenv("FACTORY_TEST_DATABASE_URL")
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "d6c_live_" + hex.EncodeToString(entropy[:])
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
	repo := t.TempDir()
	gitCmd(t, repo, "init", "-b", "main")
	gitCmd(t, repo, "config", "user.email", "dreaming@example.com")
	gitCmd(t, repo, "config", "user.name", "dreaming")
	var shas []string
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(repo, "f"), []byte(fmt.Sprintf("c%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, repo, "add", "f")
		gitCmd(t, repo, "commit", "-m", fmt.Sprintf("c%d", i))
		out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		shas = append(shas, strings.TrimSpace(string(out)))
	}
	rawBlobs, err := json.Marshal(blobs)
	if err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(t.TempDir(), "job.json")
	jobBody := `{"tests":{"TestProducedHeadReviewerPipeline":"pass","TestDraftPublishRequiresReviewerPass":"pass","TestRunSpendHardStop":"pass","TestHarnessSecretsNotInProc":"pass"},"allowlist":"pass","reexec":true,"hits":0}`
	if err := os.WriteFile(jobPath, []byte(jobBody), 0o644); err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	t.Setenv("FACTORY_DATABASE_URL", dsn)
	t.Setenv("FACTORY_S3_ENDPOINT", os.Getenv("FACTORY_TEST_S3_ENDPOINT"))
	t.Setenv("FACTORY_S3_ACCESS_KEY", envOr(t, "FACTORY_TEST_S3_KEY", "factoryadmin"))
	t.Setenv("FACTORY_S3_SECRET_KEY", envOr(t, "FACTORY_TEST_S3_SECRET", "factorysecret"))
	t.Setenv("FACTORY_S3_BUCKET", "factory")
	t.Setenv("FACTORY_S3_REGION", "us-east-1")
	t.Setenv("FACTORY_GITHUB_API_BASE", git.srv.URL)
	t.Setenv("M6_GITHUB_APP_ID", "4242")
	t.Setenv("M6_GITHUB_APP_PRIVATE_KEY", pemText)
	t.Setenv("M1_OPENAI_BASE_URL", model.URL)
	t.Setenv("M1_OPENAI_API_KEY", "sk-offline-rehearsal")
	t.Setenv("FACTORY_ROLE_SOURCE", dir)
	t.Setenv("ROLE_BLOBS", string(rawBlobs))
	t.Setenv("DREAMING_PIN", pin)
	t.Setenv("CONFIRM", ConfirmValue)
	t.Setenv("FACTORY_SHA", shas[2])
	t.Setenv("FACTORY_GIT_DIR", repo)
	t.Setenv("FACTORY_REQUIRED_SHAS", strings.Join(shas, ","))
	t.Setenv("PREFLIGHT_JOB_FILE", jobPath)
	t.Setenv("FACTORY_SECRET_NAMES", "M1_OPENAI_BASE_URL,M1_OPENAI_API_KEY,M6_GITHUB_APP_ID,M6_GITHUB_APP_PRIVATE_KEY")
	t.Cleanup(func() { _ = os.Remove("preflight.json") })
	if err := RunChecked(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("preflight.json")
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Fatalf("preflight %+v", report.Rows)
	}
	if calls < 1 || git.pulls() != 1 || !git.draft() {
		t.Fatalf("chain calls %d pulls %d draft %v", calls, git.pulls(), git.draft())
	}
}
