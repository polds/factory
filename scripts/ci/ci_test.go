package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScanSecurityCheckScript(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "ci", "scan-security-check.sh")
	out, err := exec.Command("bash", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("output: %s", out)
	}
}

func TestEgressKindSmokeTemplateOnly(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "ci", "egress-kind-smoke.sh")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "FACTORY_EGRESS_KIND_SKIP=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
