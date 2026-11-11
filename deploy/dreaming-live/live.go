package dreaminglive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// The server keeps the App key in memory for publication after the seats
	// finish. It must not stay in an environment a model tool can read.
	scrubDriverSecrets()
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
		head, _ := gitRev(work, "HEAD")
		return ChainResult{}, fmt.Errorf("published commit %s parent %q is not the pin %s", head, parent, pin)
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
	cmd := exec.Command("git", "clone", "--no-hardlinks", role, dest)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone pin: %s", strings.TrimSpace(string(out)))
	}
	for _, pair := range [][2]string{
		{"user.email", "dreaming@example.com"},
		{"user.name", "dreaming"},
		{"commit.gpgsign", "false"},
	} {
		cfg := exec.Command("git", "-C", dest, "config", pair[0], pair[1])
		cfg.Env = cmd.Env
		if out, err := cfg.CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s: %s", pair[0], strings.TrimSpace(string(out)))
		}
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
	home, err := os.MkdirTemp("", "dreaming-live-home-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(home) }()
	goal, err := viktorGoal()
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
	code, stderr, err := execHarness(ctx, []string{"--task-id", seat.viktorRun, "--model", ModelID},
		harnessEnv(in.BaseURL, seat.viktorRun, seat.token,
			"HOME="+home,
			"FACTORY_RUNTIME_DIR="+rt,
			"OPENAI_BASE_URL="+in.ModelURL,
			"OPENAI_API_KEY="+in.ModelKey,
			harness.EnvSpendURL+"="+in.BaseURL+"/api/v1/agent/runs/"+seat.viktorRun+"/spend",
			harness.EnvSpendPrice+"="+in.Price,
			"FACTORY_MAX_TURNS=2",
			"FACTORY_TASK_PROMPT="+goal,
		), work)
	if err != nil {
		return "", err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return "", fmt.Errorf("viktor harness exit %d: %s", code, msg)
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
	pkg := "./" + configuredPackage()
	raw, err := json.Marshal(harness.TaskFile{
		Prompt: "review", EvidencePackage: pkg, MutationPath: configuredMutation(),
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), raw, 0o600); err != nil {
		return err
	}
	home, err := os.MkdirTemp("", "dreaming-oskar-home-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()
	code, stderr, err := execHarness(ctx, []string{"--task-id", runID},
		harnessEnv(in.BaseURL, runID, token,
			"HOME="+home,
			"FACTORY_RUNTIME_DIR="+rt,
		), work)
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return fmt.Errorf("oskar harness exit %d: %s", code, msg)
	}
	return nil
}

func viktorGoal() (string, error) {
	goal, err := LoadGoal()
	if err != nil {
		return "", err
	}
	return goal + "\n\nThe test file path is " + configuredMutation() + ".\nThe package directory is " + configuredPackage() + ".\n", nil
}

func scrubDriverSecrets() {
	for _, name := range []string{
		"M6_GITHUB_APP_PRIVATE_KEY",
		"M1_OPENAI_API_KEY",
		"OPENAI_API_KEY",
		"FACTORY_GITHUB_APP_PRIVATE_KEY",
	} {
		_ = os.Unsetenv(name)
	}
}

var (
	harnessBinOnce sync.Once
	harnessBinPath string
	harnessBinErr  error
)

func factoryHarnessBin() (string, error) {
	harnessBinOnce.Do(func() {
		root, err := moduleRoot()
		if err != nil {
			harnessBinErr = err
			return
		}
		dir, err := os.MkdirTemp("", "factory-harness-bin-")
		if err != nil {
			harnessBinErr = err
			return
		}
		path := filepath.Join(dir, "factory-harness")
		cmd := exec.Command("go", "build", "-o", path, "./cmd/factory-harness")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			harnessBinErr = fmt.Errorf("build factory-harness: %s", strings.TrimSpace(string(out)))
			return
		}
		harnessBinPath = path
	})
	return harnessBinPath, harnessBinErr
}

// execHarness runs cmd/factory-harness. That process calls PrepareProcess,
// which re-execs with seat secrets on a pipe and marks itself non-dumpable.
// The environment is the seat list only. The driver environ is not copied.
func execHarness(ctx context.Context, args, env []string, workspace string) (int, string, error) {
	bin, err := factoryHarnessBin()
	if err != nil {
		return 0, "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workspace
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return 0, stderr.String(), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), stderr.String(), nil
	}
	return 0, stderr.String(), err
}

func harnessEnv(base, runID, token string, extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
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
