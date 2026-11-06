package runner_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScannerRunnerStub(t *testing.T) {
	root := moduleRoot(t)
	for _, rel := range []string{
		"deploy/scanner-runner/Dockerfile",
		"deploy/scanner-runner/build.sh",
		"deploy/scanner-runner/pin/provenance",
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if strings.Contains(text, "git clone") || strings.Contains(text, "curl ") || strings.Contains(text, "wget ") {
			t.Fatalf("%s fetches a scanner", rel)
		}
	}
	pin, err := os.ReadFile(filepath.Join(root, "deploy/scanner-runner/pin/provenance"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pin), "plugin=stub") || !strings.Contains(string(pin), "fetch=forbidden") {
		t.Fatalf("provenance %s", pin)
	}
	out := filepath.Join(t.TempDir(), "scanner-stub")
	cmd := exec.Command("./deploy/scanner-runner/build.sh", out)
	cmd.Dir = root
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v %s", err, raw)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(out)
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stub: %v %s", err, got)
	}
	if string(got) != "scanner-stub plugin=stub fetch=forbidden\n" {
		t.Fatalf("stub output %q", got)
	}
	refused := exec.Command("./deploy/scanner-runner/build.sh", out)
	refused.Dir = root
	refused.Env = append(os.Environ(), "SCANNER_PRIVATE_URL=https://example.invalid/scanner.git")
	raw, err = refused.CombinedOutput()
	if err == nil || !strings.Contains(string(raw), "refusing to fetch a private scanner") {
		t.Fatalf("private fetch err %v %s", err, raw)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
