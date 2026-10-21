package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FACTORY_URL points at a closed port. validate is offline, so a dial
// would change the exit code or the diagnostic stdout.
func TestValidate(t *testing.T) {
	t.Setenv("FACTORY_URL", "http://127.0.0.1:1")
	getenv := func(k string) string {
		if k == "FACTORY_URL" {
			return "http://127.0.0.1:1"
		}
		return ""
	}

	t.Run("missing-runner", func(t *testing.T) {
		dir := filepath.Join("..", "..", "internal", "definition", "testdata", "invalid", "missing-runner")
		want, err := os.ReadFile(filepath.Join(dir, "want.txt"))
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		code := execute([]string{"validate", dir}, &stdout, &stderr, getenv)
		if code != 1 {
			t.Fatalf("exit %d stderr %s stdout %s", code, stderr.String(), stdout.String())
		}
		if stdout.String() != string(want) {
			t.Fatalf("stdout %q want %q", stdout.String(), string(want))
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr %s", stderr.String())
		}
	})

	t.Run("acme-factory", func(t *testing.T) {
		dir := filepath.Join("..", "..", "examples", "acme-factory")
		var stdout, stderr strings.Builder
		code := execute([]string{"validate", dir}, &stdout, &stderr, getenv)
		if code != 0 {
			t.Fatalf("exit %d stderr %s stdout %s", code, stderr.String(), stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr %s", stderr.String())
		}
	})

	t.Run("usage", func(t *testing.T) {
		var stdout, stderr strings.Builder
		code := execute([]string{"validate"}, &stdout, &stderr, getenv)
		if code != 2 || !strings.Contains(stderr.String(), "validate [--base DIR] PATH") {
			t.Fatalf("exit %d stderr %s", code, stderr.String())
		}
	})

	t.Run("base removed gate", func(t *testing.T) {
		base := filepath.Join("..", "..", "examples", "acme-factory")
		head := t.TempDir()
		copyTree(t, base, head)
		path := filepath.Join(head, "factory.yaml")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		next := strings.Replace(string(body), "    spec:    { required: true,  approvers: \"role:editor\" }\n", "", 1)
		if next == string(body) {
			t.Fatal("spec gate line not found")
		}
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		code := execute([]string{"validate", "--base", base, head}, &stdout, &stderr, getenv)
		if code != 1 {
			t.Fatalf("exit %d stderr %s stdout %s", code, stderr.String(), stdout.String())
		}
		if !strings.Contains(stdout.String(), `approval gate "spec" removed`) {
			t.Fatalf("stdout %s", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr %s", stderr.String())
		}
	})

	t.Run("base warning exits zero", func(t *testing.T) {
		base := filepath.Join("..", "..", "examples", "acme-factory")
		head := t.TempDir()
		copyTree(t, base, head)
		writeScorer(t, filepath.Join(head, "scorers", "extra", "scorer.md"))
		var stdout, stderr strings.Builder
		code := execute([]string{"validate", "--base", base, head}, &stdout, &stderr, getenv)
		if code != 0 {
			t.Fatalf("exit %d stderr %s stdout %s", code, stderr.String(), stdout.String())
		}
		if !strings.Contains(stdout.String(), "privilege escalation:") || !strings.Contains(stdout.String(), "scorers/extra/scorer.md") {
			t.Fatalf("stdout %s", stdout.String())
		}
	})
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeScorer(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const body = `---
name: extra
description: A second judge.
agents:
  - implementer
output: classification
labels:
  - { value: pass, description: The run meets the rubric., score: 1 }
  - { value: fail, description: The run misses the rubric., score: 0 }
passingScore: 1
selfImprovement: true
---

Judge the run.
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
