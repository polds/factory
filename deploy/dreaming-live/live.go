package dreaminglive

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
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

// loadedModelKey is the inference key read from FACTORY_MODEL_KEY_FD.
// It is not put back into the process environment.
var loadedModelKey string

// SetModelKey records the inference key after it was read from a pipe.
func SetModelKey(key string) {
	loadedModelKey = strings.TrimSpace(key)
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
	chainOnly := strings.TrimSpace(os.Getenv("FACTORY_CHAIN_OUT")) != ""
	var appID int64
	var key *rsa.PrivateKey
	if !chainOnly {
		var err error
		appID, err = strconv.ParseInt(strings.TrimSpace(os.Getenv("M6_GITHUB_APP_ID")), 10, 64)
		if err != nil || appID <= 0 {
			return errors.New("M6_GITHUB_APP_ID")
		}
		key, err = parseKey(os.Getenv("M6_GITHUB_APP_PRIVATE_KEY"))
		if err != nil {
			return err
		}
	}
	modelURL := strings.TrimRight(strings.TrimSpace(os.Getenv("M1_OPENAI_BASE_URL")), "/")
	modelKey := strings.TrimSpace(loadedModelKey)
	if modelKey == "" {
		modelKey = strings.TrimSpace(os.Getenv("M1_OPENAI_API_KEY"))
	}
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
	if strings.TrimSpace(os.Getenv("FACTORY_CHAIN_OUT")) == "" && !result.Published {
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
	if err := prepareChainOut(); err != nil {
		return ChainResult{}, err
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
	chainOnly := strings.TrimSpace(os.Getenv("FACTORY_CHAIN_OUT")) != ""
	if chainOnly {
		if err := writeChainArtifact(ctx, os.Getenv("FACTORY_CHAIN_OUT"), work, sha, pin, in.DSN, oskarRun, seat.invID); err != nil {
			return ChainResult{}, err
		}
		_ = os.Unsetenv("FACTORY_PUBLISH_DRAFT_REPO")
	}
	_ = oskarExec
	rootExec, err := in.Store.StartExecution(ctx, seat.rootRun, "dreaming-worker", hashToken("root-"+seat.token))
	for try := 0; err != nil && strings.Contains(err.Error(), "queued") && try < 8; try++ {
		time.Sleep(20 * time.Millisecond)
		if _, leaseErr := in.Store.Lease(ctx, "default", time.Minute, 8); leaseErr != nil {
			err = leaseErr
			continue
		}
		rootExec, err = in.Store.StartExecution(ctx, seat.rootRun, "dreaming-worker", hashToken("root-"+seat.token))
	}
	if err != nil {
		return ChainResult{}, err
	}
	if _, err := in.Store.FinishExecution(ctx, rootExec.ID, store.Terminal{State: store.ExecCompleted}); err != nil {
		return ChainResult{}, err
	}
	_, completeErr := in.Server.CompleteNative(ctx, seat.factoryUID, seat.rootTask, false)
	published := completeErr == nil && draftOpened(ctx, in.DSN, seat.invID)
	if chainOnly {
		return ChainResult{X: sha, Published: false, JoinRefused: true, Spent: spentCents(ctx, in.DSN, seat.invID)}, nil
	}
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
	defer func() { _ = ownAs(driverOwner(), work, home, rt) }()
	if err := handToSeat(work, home, rt); err != nil {
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
	defer func() { _ = ownAs(driverOwner(), work, home, rt) }()
	if err := handToSeat(work, home, rt); err != nil {
		return err
	}
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
		if err := os.Chmod(dir, 0o755); err != nil {
			harnessBinErr = err
			return
		}
		if err := os.Chmod(path, 0o755); err != nil {
			harnessBinErr = err
			return
		}
		harnessBinPath = path
	})
	return harnessBinPath, harnessBinErr
}

// execHarness runs cmd/factory-harness as the unprivileged seat uid.
// Seat secrets are a 0400 file the seat reads and deletes, not the
// supervisor's environment. The subreaper SIGKILLs every process reparented
// to it. After it returns, every remaining process owned by the seat uid
// is killed. The driver keeps chain-out mode 0700.
func execHarness(ctx context.Context, args, env []string, workspace string) (int, string, error) {
	if err := ensureSeatUser(); err != nil {
		return 0, "", err
	}
	bin, err := factoryHarnessBin()
	if err != nil {
		return 0, "", err
	}
	script, err := os.CreateTemp("", "seat-reap-")
	if err != nil {
		return 0, "", err
	}
	if _, err := script.WriteString(seatReaper); err != nil {
		_ = script.Close()
		return 0, "", err
	}
	if err := script.Close(); err != nil {
		return 0, "", err
	}
	defer func() { _ = os.Remove(script.Name()) }()
	if err := os.Chmod(script.Name(), 0o755); err != nil {
		return 0, "", err
	}
	payload, err := json.Marshal(filterSeatEnv(os.Environ(), env))
	if err != nil {
		return 0, "", err
	}
	envFile, err := os.CreateTemp("", "seat-env-")
	if err != nil {
		return 0, "", err
	}
	if _, err := envFile.Write(payload); err != nil {
		_ = envFile.Close()
		return 0, "", err
	}
	if err := envFile.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Chmod(envFile.Name(), 0o400); err != nil {
		return 0, "", err
	}
	if err := ownAs("seat:seat", envFile.Name()); err != nil {
		return 0, "", err
	}
	defer func() { _ = exec.Command("sudo", "-n", "rm", "-f", envFile.Name()).Run() }()
	extra := []string{"FACTORY_SEAT_ENV_FILE=" + envFile.Name()}
	if log := strings.TrimSpace(os.Getenv("FACTORY_SWEEP_LOG")); log != "" {
		extra = append(extra, "FACTORY_SWEEP_LOG="+log)
	}
	argv := append([]string{"-n", "-u", "seat",
		"--preserve-env=PATH,HOME,TMPDIR,LANG,LC_ALL,GOTOOLCHAIN,GOCACHE,GOPATH,GOROOT,GOMODCACHE,FACTORY_SEAT_ENV_FILE,FACTORY_SWEEP_LOG",
		"python3", script.Name(), bin}, args...)
	cmd := exec.CommandContext(ctx, "sudo", argv...)
	cmd.Dir = workspace
	// HOME has to be a directory the seat uid can use. The driver's home is not.
	cmd.Env = setEnv(filterSeatEnv(os.Environ(), extra), "HOME", workspace)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	text := stderr.String()
	recordSweep(text)
	if killErr := killSeatUser(); killErr != nil {
		_ = ownAs(driverOwner(), workspace)
		return 0, text, killErr
	}
	// Git refuses a repository owned by the seat uid. Hand it back before
	// the driver reads the commit the seat just wrote.
	if ownErr := ownAs(driverOwner(), workspace); ownErr != nil {
		return 0, text, ownErr
	}
	if err == nil {
		return 0, text, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), text, nil
	}
	return 0, text, err
}

func prepareChainOut() error {
	dir := strings.TrimSpace(os.Getenv("FACTORY_CHAIN_OUT"))
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func ensureSeatUser() error {
	if exec.Command("id", "seat").Run() != nil {
		cmd := exec.Command("sudo", "-n", "useradd", "--system", "--create-home", "--shell", "/usr/sbin/nologin", "--user-group", "seat")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("useradd seat: %s", strings.TrimSpace(string(out)))
		}
	}
	out, err := exec.Command("id", "-nG", "seat").Output()
	if err != nil {
		return err
	}
	for _, group := range strings.Fields(string(out)) {
		switch group {
		case "sudo", "docker", "wheel", "root", "adm":
			return fmt.Errorf("seat is in group %s", group)
		}
	}
	return nil
}

func handToSeat(paths ...string) error {
	if err := ensureSeatUser(); err != nil {
		return err
	}
	for _, p := range paths {
		if err := os.Chmod(p, 0o755); err != nil {
			return err
		}
	}
	return ownAs("seat:seat", paths...)
}

func ownAs(spec string, paths ...string) error {
	if spec == "" || len(paths) == 0 {
		return nil
	}
	args := append([]string{"-n", "chown", "-R", spec}, paths...)
	cmd := exec.Command("sudo", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("chown %s: %s", spec, strings.TrimSpace(string(out)))
	}
	return nil
}

func driverOwner() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}

// killSeatUser SIGKILLs every process owned by the seat uid, including one
// that escaped the subreaper. It fails closed while any remain.
func killSeatUser() error {
	var last string
	for range 20 {
		_ = exec.Command("sudo", "-n", "pkill", "-KILL", "-u", "seat").Run()
		out, err := exec.Command("pgrep", "-u", "seat").CombinedOutput()
		last = strings.TrimSpace(string(out))
		if err != nil && last == "" {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last == "" {
		return nil
	}
	return fmt.Errorf("seat processes remain: %s", last)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

func recordSweep(stderr string) {
	log := strings.TrimSpace(os.Getenv("FACTORY_SWEEP_LOG"))
	if log == "" {
		return
	}
	var b strings.Builder
	for _, line := range strings.Split(stderr, "\n") {
		pid, ok := strings.CutPrefix(strings.TrimSpace(line), "seat-swept:")
		if !ok || pid == "" {
			continue
		}
		b.WriteString(pid + "\n")
	}
	if b.Len() == 0 {
		return
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(b.String())
	_ = f.Close()
}

const seatReaper = `import json, os, signal, subprocess, sys, time
import ctypes
libc = ctypes.CDLL(None, use_errno=True)
if libc.prctl(36, 1) != 0:
    sys.exit(1)
path = os.environ["FACTORY_SEAT_ENV_FILE"]
raw = open(path).read()
try:
    os.remove(path)
except OSError:
    pass
items = json.loads(raw)
env = {}
for item in items:
    key, sep, val = item.partition("=")
    if sep:
        env[key] = val
def living_children(me):
    kids = []
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            st = open("/proc/" + name + "/status").read()
        except OSError:
            continue
        state = ""
        ppid = None
        for line in st.splitlines():
            if line.startswith("State:"):
                state = line.split()[1]
            elif line.startswith("PPid:"):
                ppid = int(line.split()[1])
        if ppid == me and state != "Z":
            kids.append(int(name))
    return kids

def reap():
    while True:
        try:
            pid, _ = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            return
        if pid == 0:
            return

proc = subprocess.Popen(sys.argv[1:], env=env)
code = proc.wait()
time.sleep(0.05)
me = os.getpid()
for _ in range(10):
    kids = living_children(me)
    if not kids:
        reap()
        if not living_children(me):
            break
    for kid in kids:
        try:
            os.kill(kid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        else:
            sys.stderr.write("seat-swept:%d\n" % kid)
            sys.stderr.flush()
    reap()
    time.sleep(0.05)
left = [str(pid) for pid in living_children(me)]
if left:
    sys.stderr.write("seat descendants survived: " + " ".join(left) + "\n")
    sys.exit(1)
sys.exit(code if isinstance(code, int) and code >= 0 else 1)
`

// filterSeatEnv keeps allowlisted keys from src and then applies extra.
// A driver sentinel in src is dropped. extra is the seat list built by the driver.
func filterSeatEnv(src, extra []string) []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "LC_ALL": true,
		"USER": true, "LOGNAME": true, "TERM": true,
		// The seat uses HOME for its build cache. The driver's GOCACHE is not
		// writable by the seat uid, and inheriting it makes go test fail.
		"GOTOOLCHAIN":         true,
		"FACTORY_RUNTIME_DIR": true, "FACTORY_EVENTS_URL": true, "FACTORY_RUN_TOKEN": true,
		"FACTORY_SEAT_ENV_FD": true, "FACTORY_SEAT_ENV_FILE": true, "FACTORY_SWEEP_LOG": true,
		"OPENAI_BASE_URL": true, "OPENAI_API_KEY": true,
		"FACTORY_MAX_TURNS": true, "FACTORY_TASK_PROMPT": true,
		harness.EnvSpendURL: true, harness.EnvSpendPrice: true,
	}
	// extra is filtered too, so os.Environ() cannot be appended raw.
	var out []string
	keep := func(list []string) {
		for _, kv := range list {
			key, _, ok := strings.Cut(kv, "=")
			if ok && allow[key] {
				out = append(out, kv)
			}
		}
	}
	keep(src)
	keep(extra)
	return out
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
