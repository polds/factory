package dreaminglive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/server"
	"github.com/polds/factory/internal/store"
)

// checkoutDreaming fetches pin into dir with the publisher git runner.
// header is the P4 read token's Authorization value and is never placed in
// argv. A directory whose HEAD is already pin is left untouched, so an
// offline fixture is not replaced. The workflow points dir at
// $RUNNER_TEMP/dreaming-src, which is not an artifact path.
func checkoutDreaming(ctx context.Context, header, remote, dir, pin string) error {
	dir = strings.TrimSpace(dir)
	pin = strings.TrimSpace(pin)
	remote = strings.TrimSpace(remote)
	if dir == "" || pin == "" || header == "" || remote == "" {
		return nil
	}
	if head, err := gitHead(dir); err == nil && head == pin {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if _, err := server.GitExecHeader(ctx, dir, header, remote, "init", "-b", "main"); err != nil {
			return err
		}
	}
	if _, err := server.GitExecHeader(ctx, dir, header, remote, "fetch", "--depth", "1", remote, pin); err != nil {
		return err
	}
	_, err := server.GitExecHeader(ctx, dir, header, remote, "checkout", "--detach", "FETCH_HEAD")
	return err
}

func gitHead(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// bootAndRunChain is the mode=live path after a passing preflight.
func bootAndRunChain(ctx context.Context) error {
	dsn := strings.TrimSpace(os.Getenv("FACTORY_DATABASE_URL"))
	if dsn == "" {
		return errors.New("FACTORY_DATABASE_URL is required")
	}
	role := strings.TrimSpace(os.Getenv("FACTORY_ROLE_SOURCE"))
	tree, _, _, err := LoadTree(role)
	if err != nil {
		return err
	}
	appID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("M6_GITHUB_APP_ID")), 10, 64)
	if err != nil || appID <= 0 {
		return errors.New("M6_GITHUB_APP_ID")
	}
	key, err := parseKey(os.Getenv("M6_GITHUB_APP_PRIVATE_KEY"))
	if err != nil {
		return err
	}
	modelURL := strings.TrimRight(strings.TrimSpace(os.Getenv("M1_OPENAI_BASE_URL")), "/")
	modelKey := strings.TrimSpace(os.Getenv("M1_OPENAI_API_KEY"))
	if modelURL == "" || modelKey == "" {
		return errors.New("M1 inference env")
	}
	price, err := json.Marshal(workedPrice())
	if err != nil {
		return err
	}
	def, err := liveDefinition()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(def) }()
	apiKey, err := envOrRandom("FACTORY_API_KEY")
	if err != nil {
		return err
	}
	workerKey, err := envOrRandom("FACTORY_WORKER_API_KEY")
	if err != nil {
		return err
	}
	pool, err := withPool(dsn)
	if err != nil {
		return err
	}
	raw, err := withoutPool(dsn)
	if err != nil {
		return err
	}
	cfg := server.Config{
		DatabaseURL: pool, APIKey: apiKey, WorkerAPIKey: workerKey,
		DefinitionPath: def, TaskServerURL: "http://127.0.0.1:1",
		InferenceBaseURL: modelURL, InferenceAPIKey: modelKey,
		SpendPrice: string(price), WorkerPool: "standard", WorkerPlatform: "linux",
		WorkerMaxConcurrent: 2, ScheduleGate: make(chan struct{}), Console: "off",
		GitHubAppID: appID, GitHubAppKey: key,
		GitHubAPIBase: strings.TrimSpace(os.Getenv("FACTORY_GITHUB_API_BASE")),
		S3: artifacts.S3{
			Endpoint:  strings.TrimSpace(os.Getenv("FACTORY_S3_ENDPOINT")),
			Bucket:    envDefault("FACTORY_S3_BUCKET", "factory"),
			AccessKey: os.Getenv("FACTORY_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("FACTORY_S3_SECRET_KEY"),
			Region:    envDefault("FACTORY_S3_REGION", "us-east-1"),
			PathStyle: envDefault("FACTORY_S3_FORCE_PATH_STYLE", "true") != "false",
		},
	}
	srv, err := server.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()
	defer func() {
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shut)
	}()
	st, err := store.Open(ctx, pool)
	if err != nil {
		return err
	}
	defer st.Close()
	result, err := RunChain(ctx, ChainInput{
		Server: srv, Store: st, DSN: raw, BaseURL: "http://" + ln.Addr().String(),
		APIKey: apiKey, ModelURL: modelURL, ModelKey: modelKey, Price: string(price),
		Tree: tree, RoleDir: role, Ceiling: CeilingCents, Mode: "good",
	})
	if err != nil {
		return err
	}
	if !result.Published {
		return errors.New("live chain did not publish")
	}
	return nil
}

func envOrRandom(name string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v, nil
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func withPool(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if q.Get("pool_max_conns") == "" {
		q.Set("pool_max_conns", "8")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func withoutPool(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Del("pool_max_conns")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func liveDefinition() (string, error) {
	root, err := os.MkdirTemp("", "dreaming-def-")
	if err != nil {
		return "", err
	}
	files := map[string]string{
		"factory.yaml": `
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
`,
		"agents/worker/agent.md": strings.TrimSpace(`
---
description: worker does one stage.
agentType: IMPLEMENT
model: bound-model
---

Do the stage.
`) + "\n",
		"runners/local.yaml": `
description: Local runner
platform:
  os: linux
  arch: x86_64
`,
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}
