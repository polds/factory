package dreaminglive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	var chats int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			"setsid sleep 60 zz-kernel-setsid >/dev/null 2>&1 &",
			"pid=$(readlink /proc/self || true)",
			"while [ -n \"$pid\" ] && [ \"$pid\" -gt 1 ]; do",
			"  if [ -r \"/proc/$pid/environ\" ] && tr '\\0' '\\n' < \"/proc/$pid/environ\" | grep -F -q -e zz-driver-app-key-sentinel -e zz-m6-initial-sentinel -e zz-m1-initial-sentinel; then",
			"    echo driver-secret-visible",
			"    exit 1",
			"  fi",
			"  next=$(awk '/^PPid:/ { print $2 }' /proc/$pid/status 2>/dev/null || true)",
			"  if [ -z \"$next\" ] || [ \"$next\" = \"$pid\" ]; then break; fi",
			"  pid=$next",
			"done",
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
	bin := filepath.Join(t.TempDir(), "dreamingpreflight")
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
	outDir := t.TempDir()
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("driver: %v\n%s", err, out)
	}
	if exec.Command("pgrep", "-f", "zz-kernel-setsid").Run() == nil {
		t.Fatal("setsid sleep survived the driver")
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
