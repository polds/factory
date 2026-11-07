package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCheckoutGateMaterializesRequestedCommit(t *testing.T) {
	dir, _, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
	hookMark := filepath.Join(t.TempDir(), "hook")
	writeHook(t, dir, hookMark)
	code, calls, setup := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", second), second)
	if code != ExitOK || calls != 1 || strings.TrimSpace(setup) != second {
		t.Fatalf("code %d calls %d setup %q", code, calls, setup)
	}
	if head(t, dir) != second || !detached(t, dir) {
		t.Fatalf("head %s detached %v", head(t, dir), detached(t, dir))
	}
	if _, err := os.Stat(hookMark); !os.IsNotExist(err) {
		t.Fatal("hook ran")
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err != nil {
		t.Fatal("earlier file removed")
	}
	emptyCode, emptyCalls, _ := runWithOverride(t, dir, "{}", "")
	if emptyCode != ExitOK || emptyCalls != 1 || head(t, dir) != second {
		t.Fatalf("empty override code %d calls %d head %s", emptyCode, emptyCalls, head(t, dir))
	}
}

func TestCheckoutGateRefusesUnmaterializedWorkspace(t *testing.T) {
	t.Run("non-repository", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "keep.txt")
		if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := dirEntries(t, dir)
		code, calls, setup := runWithOverride(t, dir, overrideObject(t, "m6", "scratch", strings.Repeat("c0", 20)), "")
		body, err := os.ReadFile(marker)
		if code != ExitEnvironment || calls != 0 || setup != "" || err != nil || string(body) != "keep" || dirEntries(t, dir) != before {
			t.Fatalf("code %d calls %d setup %q entries %q", code, calls, setup, dirEntries(t, dir))
		}
	})
	t.Run("non-github origin", func(t *testing.T) {
		dir, first, _ := twoCommitRepo(t, "https://git.example.test/m6/scratch.git")
		before := dirEntries(t, dir)
		code, calls, setup := runWithOverride(t, dir, overrideObject(t, "m6", "scratch", strings.Repeat("c0", 20)), "")
		if code != ExitEnvironment || calls != 0 || setup != "" || head(t, dir) != first || dirEntries(t, dir) != before {
			t.Fatalf("code %d calls %d setup %q head %s entries %q", code, calls, setup, head(t, dir), dirEntries(t, dir))
		}
	})
}

func dirEntries(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" {
			names = append(names, ".git")
			continue
		}
		names = append(names, entry.Name())
	}
	return strings.Join(names, ",")
}

func TestCheckoutGateRefusesBeforeWork(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		bin := filepath.Join(t.TempDir(), "bin")
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = checkout ]; then\n    exec /usr/bin/git checkout --detach " + first + "\n  fi\ndone\nexec /usr/bin/git \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		code, calls, setup := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", second), "")
		if code != ExitEnvironment || calls != 0 || setup != "" {
			t.Fatalf("code %d calls %d setup %q", code, calls, setup)
		}
	})
	t.Run("missing", func(t *testing.T) {
		dir, first, _ := twoCommitRepo(t, "https://github.com/example/fixture.git")
		code, calls, setup := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", strings.Repeat("b", 40)), "")
		if code != ExitEnvironment || calls != 0 || setup != "" || head(t, dir) != first {
			t.Fatalf("code %d calls %d head %s", code, calls, head(t, dir))
		}
	})
	t.Run("invalid", func(t *testing.T) {
		dir, first, _ := twoCommitRepo(t, "https://github.com/example/fixture.git")
		code, calls, _ := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", "not-a-sha"), "")
		if code != ExitConfigInvalid || calls != 0 || head(t, dir) != first {
			t.Fatalf("code %d calls %d", code, calls)
		}
	})
	t.Run("dirty", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		if err := os.WriteFile(filepath.Join(dir, "a"), []byte("keep-me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, calls, setup := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", second), "")
		body, err := os.ReadFile(filepath.Join(dir, "a"))
		if code != ExitEnvironment || calls != 0 || setup != "" || err != nil || string(body) != "keep-me\n" || head(t, dir) != first {
			t.Fatalf("code %d calls %d body %q head %s", code, calls, body, head(t, dir))
		}
	})
	t.Run("wrong repo", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/other/repo.git")
		code, calls, _ := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", second), "")
		if code != ExitEnvironment || calls != 0 || head(t, dir) != first {
			t.Fatalf("code %d calls %d head %s", code, calls, head(t, dir))
		}
	})
	t.Run("parent", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		sub := filepath.Join(dir, "nested")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		code, calls, _ := runWithOverride(t, sub, overrideJSON(t, "example", "fixture", second), "")
		if code != ExitEnvironment || calls != 0 || head(t, dir) != first {
			t.Fatalf("code %d calls %d head %s", code, calls, head(t, dir))
		}
	})
	t.Run("symlink workspace", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		code, calls, _ := runWithOverride(t, link, overrideJSON(t, "example", "fixture", second), "")
		if code != ExitEnvironment || calls != 0 || head(t, dir) != first {
			t.Fatalf("code %d calls %d", code, calls)
		}
	})
	t.Run("symlink git dir", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		moved := filepath.Join(t.TempDir(), "gitdir")
		if err := os.Rename(filepath.Join(dir, ".git"), moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		code, calls, _ := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", second), "")
		cmd := exec.Command("git", "--git-dir", moved, "rev-parse", "HEAD")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		got := strings.TrimSpace(string(out))
		if code != ExitEnvironment || calls != 0 || got != first {
			t.Fatalf("code %d calls %d head %s", code, calls, got)
		}
	})
	t.Run("many overrides", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		raw := "[" + overrideObject(t, "example", "fixture", second) + "," + overrideObject(t, "example", "fixture", first) + "]"
		code, calls, _ := runWithOverride(t, dir, raw, "")
		if code != ExitConfigInvalid || calls != 0 || head(t, dir) != first {
			t.Fatalf("code %d calls %d", code, calls)
		}
	})
	t.Run("branch and tag", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		branch := strings.Replace(overrideJSON(t, "example", "fixture", second), "COMMIT_SHA", "BRANCH", 1)
		code, calls, _ := runWithOverride(t, dir, branch, "")
		if code != ExitConfigInvalid || calls != 0 || head(t, dir) != first {
			t.Fatalf("branch code %d calls %d", code, calls)
		}
		gitCmd(t, dir, "tag", "-a", "-m", "tag", "mark", first)
		tag := gitOut(t, dir, "rev-parse", "refs/tags/mark")
		code, calls, _ = runWithOverride(t, dir, overrideJSON(t, "example", "fixture", tag), "")
		if code != ExitEnvironment || calls != 0 || head(t, dir) != first {
			t.Fatalf("tag code %d calls %d head %s", code, calls, head(t, dir))
		}
	})
	t.Run("two remotes", func(t *testing.T) {
		dir, first, second := twoCommitRepo(t, "https://github.com/example/fixture.git")
		gitCmd(t, dir, "remote", "add", "upstream", "https://github.com/example/fixture.git")
		code, calls, _ := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", second), "")
		if code != ExitEnvironment || calls != 0 || head(t, dir) != first {
			t.Fatalf("code %d calls %d", code, calls)
		}
	})
}

func runWithOverride(t *testing.T, workspace, raw, wantHead string) (int, int, string) {
	t.Helper()
	rt := t.TempDir()
	setupLog := filepath.Join(t.TempDir(), "setup-head")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantHead != "" {
			got, err := os.ReadFile(setupLog)
			if err != nil || strings.TrimSpace(string(got)) != wantHead {
				t.Errorf("model saw setup %q %v", got, err)
			}
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	task, err := json.Marshal(map[string]string{
		"prompt":        "do the task",
		"model_id":      "fake-model",
		"setup_command": "git rev-parse HEAD > " + setupLog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), task, 0o600); err != nil {
		t.Fatal(err)
	}
	code := Run(context.Background(), Options{
		Args:       []string{"agent", "run", "--task-id", "checkout", "--model", "fake-model", "--idle-on-complete", "0s", "--repository-head-override-json", raw},
		Env:        []string{"PATH=" + os.Getenv("PATH"), "OPENAI_BASE_URL=" + srv.URL, "OPENAI_API_KEY=test"},
		Workspace:  workspace,
		RuntimeDir: rt,
	})
	setup, err := os.ReadFile(setupLog)
	if err != nil {
		return code, int(calls.Load()), ""
	}
	return code, int(calls.Load()), string(setup)
}

func overrideJSON(t *testing.T, owner, repo, sha string) string {
	t.Helper()
	return "[" + overrideObject(t, owner, repo, sha) + "]"
}

func overrideObject(t *testing.T, owner, repo, sha string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"code_forge": "GITHUB",
		"repo_owner": owner,
		"repo_name":  repo,
		"head":       map[string]string{"type": "COMMIT_SHA", "value": sha},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func twoCommitRepo(t *testing.T, origin string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "t@example.com")
	gitCmd(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "a")
	gitCmd(t, dir, "commit", "-m", "a")
	first := head(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "b")
	gitCmd(t, dir, "commit", "-m", "b")
	second := head(t, dir)
	gitCmd(t, dir, "remote", "add", "origin", origin)
	gitCmd(t, dir, "checkout", "--detach", first)
	return dir, first, second
}

func writeHook(t *testing.T, dir, mark string) {
	t.Helper()
	hooks := filepath.Join(dir, ".git", "hooks")
	body := []byte("#!/bin/sh\necho ran >> " + mark + "\n")
	for _, name := range []string{"pre-checkout", "post-checkout"} {
		path := filepath.Join(hooks, name)
		if err := os.WriteFile(path, body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return gitOut(t, dir, "rev-parse", "HEAD")
}

func detached(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("git", "symbolic-ref", "--quiet", "HEAD")
	cmd.Dir = dir
	return cmd.Run() != nil
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
