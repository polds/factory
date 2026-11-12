package dreaminglive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDriverKernelAncestors(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" || os.Getenv("FACTORY_TEST_S3_ENDPOINT") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL and FACTORY_TEST_S3_ENDPOINT are required")
	}
	ctx := context.Background()
	dir := t.TempDir()
	_, pin, _, err := BuildLivePin(dir)
	if err != nil {
		t.Fatal(err)
	}
	// These directories live directly under /tmp. A t.TempDir parent is mode
	// 0700, so the seat uid could not traverse it to attempt the writes.
	outDir, err := os.MkdirTemp("", "chain-out-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outDir) })
	markDir, err := os.MkdirTemp("", "seat-mark-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(markDir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(markDir) })
	walkPath := filepath.Join(markDir, "walk")
	sweepLog := filepath.Join(t.TempDir(), "sweep")
	var chats int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/model/info" {
			_, _ = io.WriteString(w, modelInfoBody())
			return
		}
		if !strings.Contains(r.URL.Path, "chat") {
			w.Header().Set("x-litellm-response-cost", "0.01")
			_, _ = io.WriteString(w, `{"model":"`+ModelID+`","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
			return
		}
		chats++
		w.Header().Set("x-litellm-response-cost", "0.01")
		if chats > 1 {
			_, _ = io.WriteString(w, `{"model":"`+ModelID+`","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		command := strings.Join([]string{
			"set -e",
			"if sudo -n true; then echo sudo-ok; exit 1; fi",
			"if docker run --rm alpine true; then echo docker-ok; exit 1; fi",
			"if echo x > " + shellQuote(filepath.Join(outDir, "pwned")) + "; then echo wrote-ok; exit 1; fi",
			"echo probed > " + shellQuote(filepath.Join(markDir, "probed")),
			"setsid bash -c 'echo $$ > " + filepath.Join(markDir, "survivor") + "; exec -a zz-kseat sleep 300' >/dev/null 2>&1 &",
			"for i in 1 2 3 4 5 6 7 8 9 10; do",
			"  if [ -s " + shellQuote(filepath.Join(markDir, "survivor")) + " ]; then break; fi",
			"  sleep 0.05",
			"done",
			"if [ ! -s " + shellQuote(filepath.Join(markDir, "survivor")) + " ]; then echo survivor-missing; exit 1; fi",
			"spid=$(cat " + shellQuote(filepath.Join(markDir, "survivor")) + ")",
			"ok=0",
			"for i in 1 2 3 4 5 6 7 8 9 10; do",
			"  if tr '\\0' ' ' < /proc/$spid/cmdline 2>/dev/null | grep -q zz-kseat; then ok=1; break; fi",
			"  sleep 0.05",
			"done",
			"if [ \"$ok\" -ne 1 ]; then echo survivor-not-alive; exit 1; fi",
			"pid=$$",
			": > " + shellQuote(walkPath),
			"reached=0",
			"while [ -n \"$pid\" ] && [ \"$pid\" -gt 1 ]; do",
			"  comm=$(cat /proc/$pid/comm 2>/dev/null || echo missing)",
			"  printf '%s %s\\n' \"$pid\" \"$comm\" >> " + shellQuote(walkPath),
			"  if [ \"$comm\" = zzdriver ]; then reached=1; fi",
			"  if [ -r \"/proc/$pid/environ\" ] && tr '\\0' '\\n' < \"/proc/$pid/environ\" | grep -F -q -e zz-driver-app-key-sentinel -e zz-m6-initial-sentinel -e zz-m1-initial-sentinel; then",
			"    echo driver-secret-visible",
			"    exit 1",
			"  fi",
			"  next=$(awk '/^PPid:/ { print $2 }' /proc/$pid/status 2>/dev/null || true)",
			"  if [ -z \"$next\" ] || [ \"$next\" = \"$pid\" ]; then break; fi",
			"  pid=$next",
			"done",
			"if [ \"$reached\" -ne 1 ]; then echo walk-missed-driver; exit 1; fi",
			"cat > oracle/tenant_test.go << 'EOF'",
			"package oracle",
			"import \"testing\"",
			"func TestTenantIsolation(t *testing.T) {",
			"  if tenant() != \"factoryEvidencePass\" { t.Fatal(\"tenant\") }",
			"}",
			"func tenant() string { return \"factoryEvidencePass\" }",
			"EOF",
			"git add oracle/tenant_test.go",
			"git -c user.email=dreaming@example.com -c user.name=dreaming -c commit.gpgsign=false commit -m tenant",
		}, "\n")
		args, _ := json.Marshal(map[string]string{"command": command})
		_, _ = fmt.Fprintf(w, `{"model":"%s","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":%s}}]}}]}`, ModelID, strconvQuote(string(args)))
	}))
	t.Cleanup(model.Close)
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "zzdriver")
	build := exec.Command("go", "build", "-o", bin, "./deploy/dreaming-live/cmd/dreamingpreflight")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s", out)
	}
	keyFile, err := os.CreateTemp(t.TempDir(), "model-key-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyFile.WriteString("sk-offline-rehearsal"); err != nil {
		t.Fatal(err)
	}
	if _, err := keyFile.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("", "kernel-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GOCACHE=" + filepath.Join(home, "gocache"),
		"GOTOOLCHAIN=local",
		"FACTORY_DATABASE_URL=" + os.Getenv("FACTORY_TEST_DATABASE_URL"),
		"FACTORY_ROLE_SOURCE=" + dir,
		"FACTORY_S3_ENDPOINT=" + os.Getenv("FACTORY_TEST_S3_ENDPOINT"),
		"FACTORY_S3_BUCKET=factory",
		"FACTORY_S3_ACCESS_KEY=" + envOr(t, "FACTORY_TEST_S3_KEY", "factoryadmin"),
		"FACTORY_S3_SECRET_KEY=" + envOr(t, "FACTORY_TEST_S3_SECRET", "factorysecret"),
		"FACTORY_S3_REGION=us-east-1",
		"M1_OPENAI_BASE_URL=" + model.URL,
		"FACTORY_CHAIN_OUT=" + outDir,
		"FACTORY_SWEEP_LOG=" + sweepLog,
		"FACTORY_EVIDENCE_PACKAGE=oracle",
		"FACTORY_EVIDENCE_MUTATION=oracle/tenant_test.go",
		"DREAMING_PIN=" + pin,
		"CONFIRM=" + ConfirmValue,
		"FACTORY_MODEL_KEY_FD=3",
		"ZZ_DRIVER_SECRET=zz-driver-app-key-sentinel",
		"M6_GITHUB_APP_PRIVATE_KEY=zz-m6-initial-sentinel",
		"M1_OPENAI_API_KEY=zz-m1-initial-sentinel",
	}
	cmd := exec.CommandContext(ctx, bin, "chain")
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{keyFile}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("sudo", "-n", "pkill", "-KILL", "-u", "seat").Run()
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	sawDriver := false
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		status, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
		if statErr != nil || strings.Contains(string(status), "State:\tZ") {
			break
		}
		if walkReached(walkPath, cmd.Process.Pid) {
			envRaw, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/environ", cmd.Process.Pid))
			if readErr != nil {
				t.Fatal(readErr)
			}
			text := string(envRaw)
			for _, needle := range []string{"zz-driver-app-key-sentinel", "zz-m6-initial-sentinel", "zz-m1-initial-sentinel"} {
				if strings.Contains(text, needle) {
					t.Fatalf("driver environ still has %s", needle)
				}
			}
			sawDriver = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Wait(); err != nil {
		walk, _ := os.ReadFile(walkPath)
		t.Fatalf("driver: %v\n%s\nWALK:\n%s", err, output.String(), walk)
	}
	if !sawDriver {
		walk, _ := os.ReadFile(walkPath)
		t.Fatalf("ancestor walk did not reach the driver\n%s", walk)
	}
	if _, err := os.Stat(filepath.Join(markDir, "probed")); err != nil {
		t.Fatal("seat did not run the sudo/docker/chain-out probe")
	}
	if _, err := os.Stat(filepath.Join(outDir, "pwned")); err == nil {
		t.Fatal("seat wrote chain-out")
	}
	info, err := os.Stat(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("chain-out mode %o", info.Mode().Perm())
	}
	survivor, err := os.ReadFile(filepath.Join(markDir, "survivor"))
	if err != nil || strings.TrimSpace(string(survivor)) == "" {
		t.Fatalf("setsid survivor never started: %v %q", err, survivor)
	}
	if seatLabelAlive("zz-kseat") {
		t.Fatal("setsid sleep survived the seat")
	}
	swept, err := os.ReadFile(sweepLog)
	if err != nil || !strings.Contains(string(swept), strings.TrimSpace(string(survivor))) {
		t.Fatalf("sweep did not kill %s: %v %q", survivor, err, swept)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func walkReached(path string, pid int) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	want := strconv.Itoa(pid)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == want && fields[1] == "zzdriver" {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
