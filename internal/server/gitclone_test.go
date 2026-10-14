package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitClonerFetchesHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	src := t.TempDir()
	gitIn(t, src, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", "README")
	gitIn(t, src, "commit", "-m", "init")
	want := strings.TrimSpace(gitIn(t, src, "rev-parse", "HEAD"))

	dest := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := (gitCloner{}).Clone(context.Background(), src, "main", dest)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("sha %s, want %s", got, want)
	}
	body, err := os.ReadFile(filepath.Join(dest, "README"))
	if err != nil || string(body) != "hi\n" {
		t.Fatalf("checkout %q %v", body, err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=factory",
		"GIT_AUTHOR_EMAIL=factory@example.com",
		"GIT_COMMITTER_NAME=factory",
		"GIT_COMMITTER_EMAIL=factory@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
