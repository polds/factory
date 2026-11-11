package ci_test

import (
	"errors"
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

func TestGoToolchainMatchRepo(t *testing.T) {
	script := toolchainScript(t)
	out, err := exec.Command("bash", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	text := string(out)
	for _, path := range []string{
		"deploy/factoryd/Dockerfile",
		"deploy/operator/Dockerfile",
		"deploy/scanner-runner/Dockerfile",
		"deploy/worker/Dockerfile",
		"spike/m1/task-image/Dockerfile",
		"spike/m5/factoryd-git.Dockerfile",
	} {
		if !strings.Contains(text, path) {
			t.Errorf("output missing %s\n%s", path, text)
		}
	}
}

func TestGoToolchainMatchFixtures(t *testing.T) {
	script := toolchainScript(t)
	const withToolchain = "module example.com/fixture\n\ngo 1.26.0\n\ntoolchain go1.26.11\n"
	const goLineOnly = "module example.com/fixture\n\ngo 1.26.11\n"
	tests := []struct {
		name       string
		gomod      string
		dockerfile string
		wantCode   int
		wantSubstr string
	}{
		{
			name:       "older",
			gomod:      withToolchain,
			dockerfile: "FROM golang:1.26.10 AS build\n",
			wantCode:   1,
			wantSubstr: "is older than 1.26.11",
		},
		{
			name:       "match",
			gomod:      withToolchain,
			dockerfile: "FROM golang:1.26.11 AS build\n",
			wantCode:   0,
		},
		{
			name:       "newer",
			gomod:      withToolchain,
			dockerfile: "FROM golang:1.27.0-bookworm AS build\n",
			wantCode:   0,
		},
		{
			name:       "digest-pinned-match",
			gomod:      withToolchain,
			dockerfile: "FROM golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS build\n",
			wantCode:   0,
			wantSubstr: "@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb",
		},
		{
			name:       "digest-pinned-older",
			gomod:      withToolchain,
			dockerfile: "FROM golang:1.26.10@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa AS build\n",
			wantCode:   1,
			wantSubstr: "is older than 1.26.11",
		},
		{
			name:       "toolchain-not-go-line",
			gomod:      withToolchain,
			dockerfile: "FROM golang:1.26.0 AS build\n",
			wantCode:   1,
			wantSubstr: "is older than 1.26.11",
		},
		{
			name:       "fallback-go-line-older",
			gomod:      goLineOnly,
			dockerfile: "FROM golang:1.26.10\n",
			wantCode:   1,
			wantSubstr: "is older than 1.26.11",
		},
		{
			name:       "alpine-patch-suffix",
			gomod:      withToolchain,
			dockerfile: "FROM --platform=linux/amd64 golang:1.26.11-alpine3.22 AS build\n",
			wantCode:   0,
		},
		{
			name:       "older-after-other-stage",
			gomod:      withToolchain,
			dockerfile: "FROM busybox:1.36\nFROM golang:1.25.0 AS build\n",
			wantCode:   1,
			wantSubstr: "is older than 1.26.11",
		},
		{
			name:       "no-golang-stage",
			gomod:      withToolchain,
			dockerfile: "FROM busybox:1.36\n",
			wantCode:   0,
		},
		{
			name:       "unparseable-tag",
			gomod:      withToolchain,
			dockerfile: "FROM golang:latest\n",
			wantCode:   1,
			wantSubstr: "unparseable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runToolchainMatch(t, script, tt.gomod, tt.dockerfile)
			if code != tt.wantCode {
				t.Fatalf("exit %d, want %d\n%s", code, tt.wantCode, out)
			}
			if tt.wantSubstr != "" && !strings.Contains(out, tt.wantSubstr) {
				t.Fatalf("output missing %q\n%s", tt.wantSubstr, out)
			}
		})
	}
}

func toolchainScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "scripts", "ci", "go-toolchain-match.sh")
}

func runToolchainMatch(t *testing.T, script, gomod, dockerfile string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out)
	}
	t.Fatalf("%v\n%s", err, out)
	return -1, string(out)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
