package dreaminglive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/harness"
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
	result, err := RunLive(ctx, ChainInput{
		Server: srv, Store: st, DSN: raw, BaseURL: "http://" + ln.Addr().String(),
		APIKey: apiKey, ModelURL: modelURL, ModelKey: modelKey, Price: string(price),
		Tree: tree, RoleDir: role, Ceiling: CeilingCents, Mode: "live",
	})
	if err != nil {
		return err
	}
	if !result.Published {
		return errors.New("live chain did not publish")
	}
	return nil
}

// RunLive is the mode=live chain. It refuses every rehearsal mode. Viktor's
// harness commits on top of the pin checkout, and Oskar's harness runs go test.
// Evidence rows come from that execution. Nothing is inserted by hand.
func RunLive(ctx context.Context, in ChainInput) (ChainResult, error) {
	if rehearsalMode(in.Mode) {
		return ChainResult{}, errors.New("live refused the rehearsal chain")
	}
	seat, err := openViktor(ctx, in)
	if err != nil {
		return ChainResult{}, err
	}
	work, err := os.MkdirTemp("", "dreaming-live-work-")
	if err != nil {
		return ChainResult{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	pin := in.Tree.SourceCommit
	if err := clonePin(in.RoleDir, work); err != nil {
		return ChainResult{}, err
	}
	sha, err := runViktorHarness(ctx, in, seat, work)
	if err != nil {
		return ChainResult{}, err
	}
	parent, err := gitRev(work, "HEAD^")
	if err != nil || parent != pin {
		return ChainResult{}, fmt.Errorf("published commit parent %s is not the pin", parent)
	}
	oskarRun, oskarExec, err := delegateOskar(ctx, in, seat)
	if err != nil {
		return ChainResult{}, err
	}
	if err := runOskarHarness(ctx, in, oskarRun, "oskar-"+seat.token, work); err != nil {
		return ChainResult{}, err
	}
	if err := evidenceRan(ctx, in.DSN, oskarRun); err != nil {
		return ChainResult{}, err
	}
	_ = oskarExec
	rootExec, err := in.Store.StartExecution(ctx, seat.rootRun, "dreaming-worker", hashToken("root-"+seat.token))
	if err != nil {
		return ChainResult{}, err
	}
	if _, err := in.Store.FinishExecution(ctx, rootExec.ID, store.Terminal{State: store.ExecCompleted}); err != nil {
		return ChainResult{}, err
	}
	_, completeErr := in.Server.CompleteNative(ctx, seat.factoryUID, seat.rootTask, false)
	published := completeErr == nil && draftOpened(ctx, in.DSN, seat.invID)
	if !published {
		return ChainResult{}, errors.New("live chain did not publish")
	}
	return ChainResult{X: sha, Published: true, JoinRefused: true, Spent: spentCents(ctx, in.DSN, seat.invID)}, nil
}

func rehearsalMode(mode string) bool {
	switch mode {
	case "good", "tiny", "allowance", "cancel", "no-sentinel", "compile":
		return true
	default:
		return false
	}
}

func clonePin(role, dest string) error {
	cmd := exec.Command("git", "clone", "--local", role, dest)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone pin: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func gitRev(dir, rev string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", rev)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func runViktorHarness(ctx context.Context, in ChainInput, seat openedSeat, work string) (string, error) {
	rt, err := os.MkdirTemp("", "dreaming-live-rt-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(rt) }()
	goal, err := LoadGoal()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(harness.TaskFile{Prompt: goal, ModelID: ModelID})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), raw, 0o600); err != nil {
		return "", err
	}
	code := harness.Run(ctx, harness.Options{
		Args: []string{"--task-id", seat.viktorRun, "--model", ModelID},
		Env: harnessEnv(in.BaseURL, seat.viktorRun, seat.token,
			"OPENAI_BASE_URL="+in.ModelURL,
			"OPENAI_API_KEY="+in.ModelKey,
			harness.EnvSpendURL+"="+in.BaseURL+"/api/v1/agent/runs/"+seat.viktorRun+"/spend",
			harness.EnvSpendPrice+"="+in.Price,
			"FACTORY_MAX_TURNS=2",
			"FACTORY_TASK_PROMPT="+goal,
		),
		Workspace:   work,
		RuntimeDir:  rt,
		Stderr:      io.Discard,
		ChatTimeout: 15 * time.Second,
	})
	if code != 0 {
		return "", fmt.Errorf("viktor harness exit %d", code)
	}
	return gitRev(work, "HEAD")
}

func delegateOskar(ctx context.Context, in ChainInput, seat openedSeat) (string, string, error) {
	oskar, err := in.Server.DelegateNative(ctx, seat.factoryUID, seat.rootTask, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: seat.key + "-oskar", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		return "", "", fmt.Errorf("delegate oskar: %w", err)
	}
	act, err := coordination.ClaimActivation(ctx, in.Store, oskar.ChildID, store.ClaimInput{AgentID: seat.agentID})
	if err != nil {
		return "", "", err
	}
	if _, err := in.Store.Lease(ctx, "default", time.Minute, 8); err != nil {
		return "", "", err
	}
	exec, err := in.Store.StartExecution(ctx, act.RunID, "dreaming-worker", hashToken("oskar-"+seat.token))
	if err != nil {
		return "", "", err
	}
	return act.RunID, exec.ID, nil
}

func runOskarHarness(ctx context.Context, in ChainInput, runID, token, work string) error {
	rt, err := os.MkdirTemp("", "dreaming-live-oskar-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(rt) }()
	raw, err := json.Marshal(harness.TaskFile{
		Prompt: "review", EvidencePackage: "./oracle", MutationPath: "oracle/tenant_test.go",
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), raw, 0o600); err != nil {
		return err
	}
	code := harness.Run(ctx, harness.Options{
		Args: []string{"--task-id", runID},
		Env: harnessEnv(in.BaseURL, runID, token,
			"GOTOOLCHAIN=local",
		),
		Workspace:  work,
		RuntimeDir: rt,
		Stderr:     io.Discard,
	})
	if code != 0 {
		return fmt.Errorf("oskar harness exit %d", code)
	}
	return nil
}

func harnessEnv(base, runID, token string, extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GOTOOLCHAIN=local",
		"FACTORY_EVENTS_URL=" + base + "/api/v1/agent/runs/" + runID + "/events:ingest",
		"FACTORY_RUN_TOKEN=" + token,
	}
	return append(env, extra...)
}

func evidenceRan(ctx context.Context, dsn, runID string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE run_id = $1 AND type = $2 AND payload_json->>'text' LIKE '%"Action":"run"%'`, runID, harness.EventEvidenceTest).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("reviewer evidence was stored without a test execution")
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
