package verify_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/harness/verify"
)

func TestConformance(t *testing.T) {
	bin := build(t, "./cmd/factory-harness", "factory-harness")
	results := verify.All(bin)
	want := []string{
		"flags", "success", "redact", "agent-failure", "invalid-model",
		"unreachable", "setup-failed", "outside-write", "auth-scrub",
		"skip-initial", "follow-up", "bare-idle",
	}
	if len(results) != len(want) {
		t.Fatalf("got %d cases", len(results))
	}
	for i, name := range want {
		if results[i].Name != name {
			t.Fatalf("case %d is %s", i, results[i].Name)
		}
		if results[i].Err != nil {
			t.Fatalf("%s: %v", name, results[i].Err)
		}
	}
}

func TestFactoryctlVerify(t *testing.T) {
	harnessBin := build(t, "./cmd/factory-harness", "factory-harness")
	ctl := build(t, "./cmd/factoryctl", "factoryctl")
	cmd := exec.Command(ctl, "harness", "verify", "--bin", harnessBin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "VERDICT: conformant") || !strings.Contains(text, "PASS  redact") {
		t.Fatalf("%s", text)
	}
}

func build(t *testing.T, pkg, name string) string {
	t.Helper()
	mod := moduleRoot(t)
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = mod
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, raw)
	}
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD")
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	mod := strings.TrimSpace(string(raw))
	if mod == "" || mod == os.DevNull {
		t.Fatal("GOMOD empty")
	}
	return filepath.Dir(mod)
}
