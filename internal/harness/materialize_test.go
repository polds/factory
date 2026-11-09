package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMaterializeCopiesLocalCommitThenGateChecksItOut(t *testing.T) {
	source, first, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
	workspace := filepath.Join(t.TempDir(), "workspace")
	log := filepath.Join(t.TempDir(), "git.log")
	restore := wrapGit(t, log)
	defer restore()
	opt := proofOptions(t, workspace, "", source, pin, "seat-bytes\ngoal-bytes")
	inv := ParseArgs(opt.Args)
	task, err := MaterializeAssigned(context.Background(), opt, inv)
	if err != nil {
		t.Fatal(err)
	}
	if head(t, workspace) != first || head(t, source) != first {
		t.Fatalf("workspace %s source %s", head(t, workspace), head(t, source))
	}
	if task.Prompt != "seat-bytes\ngoal-bytes" || task.PinnedHeadSHA != pin || task.BundleMode != "role_files" || task.PreHarnessSetup != PreHarnessSetupDisabled || task.SetupCommand != "" {
		t.Fatalf("task %+v", task)
	}
	info, err := os.Stat(filepath.Join(opt.RuntimeDir, "task.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	body, err := os.ReadFile(filepath.Join(opt.RuntimeDir, "task.json"))
	if err != nil || strings.Contains(string(body), "setup_command") || !strings.Contains(string(body), "seat-bytes") {
		t.Fatalf("file %s %v", body, err)
	}
	recorded, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(recorded), "fetch") || strings.Contains(string(recorded), "clone") || strings.Contains(string(recorded), "checkout") {
		t.Fatalf("git log %s %v", recorded, err)
	}
	code, calls, setup := runProof(t, opt, pin)
	if code != ExitOK || calls != 1 || strings.TrimSpace(setup) != "" || head(t, workspace) != pin || !detached(t, workspace) || head(t, source) != first {
		t.Fatalf("code %d calls %d setup %q head %s source %s", code, calls, setup, head(t, workspace), head(t, source))
	}
}

func TestProofRefusesUntrustedSetupBeforeGate(t *testing.T) {
	source, first, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
	workspace := filepath.Join(t.TempDir(), "workspace")
	marker := filepath.Join(t.TempDir(), "setup-ran")
	opt := proofOptions(t, workspace, "", source, pin, "prompt")
	opt.Env = append(opt.Env, EnvUntrustedSetup+"=touch "+marker)
	code, calls, _ := runProof(t, opt, "")
	if _, err := os.Stat(marker); !os.IsNotExist(err) || code != ExitConfigInvalid || calls != 0 {
		t.Fatalf("code %d calls %d err %v", code, calls, err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("workspace created")
	}
	if head(t, source) != first {
		t.Fatal("source moved")
	}
	poison := filepath.Join(t.TempDir(), "rt")
	if err := os.Mkdir(poison, 0o755); err != nil {
		t.Fatal(err)
	}
	opt = proofOptions(t, source, poison, "", pin, "prompt")
	if err := os.WriteFile(filepath.Join(poison, "task.json"), []byte(`{"prompt":"prompt","setup_command":"touch `+marker+`","pre_harness_setup":"disabled"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, calls, _ = runProof(t, opt, "")
	if _, err := os.Stat(marker); !os.IsNotExist(err) || code != ExitConfigInvalid || calls != 0 || head(t, source) != first {
		t.Fatalf("code %d calls %d head %s err %v", code, calls, head(t, source), err)
	}
}

func TestProofFailuresStayBeforeSetupAndModel(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		source, first, _ := twoCommitRepo(t, "https://github.com/example/fixture.git")
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", source, strings.Repeat("b", 40), "prompt")
		code, calls, setup := runProof(t, opt, "")
		if code != ExitEnvironment || calls != 0 || setup != "" || head(t, source) != first {
			t.Fatalf("code %d calls %d head %s", code, calls, head(t, source))
		}
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatal("copied a repository without the object")
		}
	})
	t.Run("invalid", func(t *testing.T) {
		source, first, _ := twoCommitRepo(t, "https://github.com/example/fixture.git")
		opt := proofOptions(t, filepath.Join(t.TempDir(), "workspace"), "", source, "not-a-sha", "prompt")
		code, calls, _ := runProof(t, opt, "")
		if code != ExitConfigInvalid || calls != 0 || head(t, source) != first {
			t.Fatalf("code %d calls %d", code, calls)
		}
	})
	t.Run("empty override", func(t *testing.T) {
		source, first, _ := twoCommitRepo(t, "https://github.com/example/fixture.git")
		opt := proofOptions(t, source, "", "", "", "prompt")
		opt.Args = append(opt.Args, "--repository-head-override-json", "{}")
		code, calls, _ := runProof(t, opt, "")
		if code != ExitConfigInvalid || calls != 0 || head(t, source) != first {
			t.Fatalf("code %d calls %d", code, calls)
		}
	})
	t.Run("no override", func(t *testing.T) {
		source, first, _ := twoCommitRepo(t, "https://github.com/example/fixture.git")
		opt := proofOptions(t, source, "", "", "", "prompt")
		code, calls, setup := runProof(t, opt, "")
		if code != ExitConfigInvalid || calls != 0 || setup != "" || head(t, source) != first {
			t.Fatalf("code %d calls %d setup %q", code, calls, setup)
		}
	})
	t.Run("dirty", func(t *testing.T) {
		source, first, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		if err := os.WriteFile(filepath.Join(source, "a"), []byte("keep-me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		code, calls, setup := runProof(t, opt, "")
		body, err := os.ReadFile(filepath.Join(workspace, "a"))
		if code != ExitEnvironment || calls != 0 || setup != "" || err != nil || string(body) != "keep-me\n" || head(t, workspace) != first {
			t.Fatalf("code %d calls %d body %q head %s err %v", code, calls, body, head(t, workspace), err)
		}
	})
	t.Run("wrong repo", func(t *testing.T) {
		source, first, pin := twoCommitRepo(t, "https://github.com/other/repo.git")
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		code, calls, _ := runProof(t, opt, "")
		if code != ExitEnvironment || calls != 0 || head(t, workspace) != first || head(t, source) != first {
			t.Fatalf("code %d calls %d head %s", code, calls, head(t, workspace))
		}
	})
	t.Run("parent", func(t *testing.T) {
		source, first, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		parent, parentHead, _ := twoCommitRepo(t, "https://github.com/example/fixture.git")
		workspace := filepath.Join(parent, "nested")
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		code, calls, _ := runProof(t, opt, "")
		if code != ExitEnvironment || calls != 0 || head(t, parent) != parentHead || head(t, source) != first {
			t.Fatalf("code %d calls %d parent %s", code, calls, head(t, parent))
		}
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatal("nested workspace created")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		source, first, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		opt := proofOptions(t, link, "", source, pin, "prompt")
		code, calls, _ := runProof(t, opt, "")
		entries, err := os.ReadDir(real)
		if code != ExitEnvironment || calls != 0 || err != nil || len(entries) != 0 || head(t, source) != first {
			t.Fatalf("code %d calls %d entries %d", code, calls, len(entries))
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		source, first, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		workspace := filepath.Join(t.TempDir(), "workspace")
		bin := filepath.Join(t.TempDir(), "bin")
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		real, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = checkout ]; then\n    exec " + real + " checkout --detach " + first + "\n  fi\ndone\nexec " + real + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		code, calls, setup := runProof(t, opt, "")
		if code != ExitEnvironment || calls != 0 || setup != "" || head(t, workspace) != first {
			t.Fatalf("code %d calls %d setup %q head %s", code, calls, setup, head(t, workspace))
		}
	})
}

func TestLocalSourceContainment(t *testing.T) {
	t.Run("filter driver", func(t *testing.T) {
		source, _, pin := repoWithFilterAttribute(t)
		sentinel := filepath.Join(t.TempDir(), "sentinel")
		script := passThroughFilter(t, sentinel)
		gitCmd(t, source, "config", "filter.pwn.clean", script)
		gitCmd(t, source, "config", "filter.pwn.smudge", script)
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		code, calls, _ := runProof(t, opt, "")
		if _, err := os.Stat(sentinel); !os.IsNotExist(err) || code != ExitEnvironment || calls != 0 {
			t.Fatalf("code %d calls %d err %v", code, calls, err)
		}
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatal("unsafe source was copied")
		}
	})
	t.Run("hook", func(t *testing.T) {
		source, _, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		mark := filepath.Join(t.TempDir(), "hook")
		writeHook(t, source, mark)
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		code, calls, _ := runProof(t, opt, pin)
		if _, err := os.Stat(mark); !os.IsNotExist(err) || code != ExitOK || calls != 1 {
			t.Fatalf("code %d calls %d err %v", code, calls, err)
		}
	})
	t.Run("worktree", func(t *testing.T) {
		source, first, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		wt := filepath.Join(t.TempDir(), "wt")
		gitCmd(t, source, "worktree", "add", "--detach", wt, first)
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", wt, pin, "prompt")
		code, calls, _ := runProof(t, opt, "")
		if code != ExitEnvironment || calls != 0 {
			t.Fatalf("code %d calls %d", code, calls)
		}
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatal("worktree copied")
		}
	})
	t.Run("symlink escape", func(t *testing.T) {
		source, _, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		outside := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(outside, []byte("secret-bytes\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(source, "leak")); err != nil {
			t.Fatal(err)
		}
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		code, calls, _ := runProof(t, opt, "")
		if code != ExitEnvironment || calls != 0 {
			t.Fatalf("code %d calls %d", code, calls)
		}
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatal("escaped symlink copied")
		}
		body, err := os.ReadFile(outside)
		if err != nil || string(body) != "secret-bytes\n" {
			t.Fatalf("outside %q %v", body, err)
		}
	})
	t.Run("credential origin", func(t *testing.T) {
		source, _, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
		gitCmd(t, source, "remote", "set-url", "origin", "https://x-access-token:supersecret@github.com/example/fixture.git")
		workspace := filepath.Join(t.TempDir(), "workspace")
		opt := proofOptions(t, workspace, "", source, pin, "prompt")
		inv := ParseArgs(opt.Args)
		_, err := MaterializeAssigned(context.Background(), opt, inv)
		if err == nil || strings.Contains(err.Error(), "supersecret") {
			t.Fatalf("err %v", err)
		}
		if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
			t.Fatal("credential remote copied")
		}
	})
}

func TestLegacySetupStillRunsWithoutProofFlag(t *testing.T) {
	dir, _, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
	code, calls, setup := runWithOverride(t, dir, overrideJSON(t, "example", "fixture", pin), pin)
	if code != ExitOK || calls != 1 || strings.TrimSpace(setup) != pin {
		t.Fatalf("code %d calls %d setup %q", code, calls, setup)
	}
}

func proofOptions(t *testing.T, workspace, runtime, source, pin, prompt string) Options {
	t.Helper()
	if runtime == "" {
		runtime = t.TempDir()
	}
	args := []string{"agent", "run", "--task-id", "proof", "--model", "fake-model", "--idle-on-complete", "0s"}
	if pin != "" {
		args = append(args, "--repository-head-override-json", overrideJSON(t, "example", "fixture", pin))
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		EnvPreHarnessSetup + "=" + PreHarnessSetupDisabled,
		EnvBundleMode + "=role_files",
		"FACTORY_TASK_PROMPT=" + prompt,
	}
	if source != "" {
		env = append(env, EnvLocalSource+"="+source)
	}
	return Options{Args: args, Env: env, Workspace: workspace, RuntimeDir: runtime}
}

func runProof(t *testing.T, opt Options, wantHead string) (int, int, string) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	opt.Env = append(opt.Env, "OPENAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test")
	var stderr strings.Builder
	opt.Stderr = &stderr
	code := Run(context.Background(), opt)
	if wantHead != "" && head(t, opt.Workspace) != wantHead {
		t.Fatalf("code %d head %s want %s stderr %s", code, head(t, opt.Workspace), wantHead, stderr.String())
	}
	return code, int(calls.Load()), ""
}

func wrapGit(t *testing.T, log string) func() {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+old)
	return func() {}
}
