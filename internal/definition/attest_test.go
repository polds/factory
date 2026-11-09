package definition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttestRoleSourceMatchesLocalCommit(t *testing.T) {
	dir := t.TempDir()
	body := []byte("ROLEBODY-7f3a\n")
	roster := []byte("roster: synthetic\n")
	writeRole(t, dir, "alpha", body)
	if err := os.WriteFile(filepath.Join(dir, "org", "roster.yaml"), roster, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	git(t, dir, "add", "org")
	git(t, dir, "commit", "-m", "roles")
	commit := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	blobBody := gitOut(t, dir, "cat-file", "blob", commit+":org/agents/alpha/AGENTS.md")
	if blobBody != string(body) {
		t.Fatalf("worktree blob len %d body len %d", len(blobBody), len(body))
	}
	sum := sha256.Sum256(roster)
	tree := NativeTree{
		SourceCommit:  commit,
		RosterSHA256:  hex.EncodeToString(sum[:]),
		RosterGitBlob: gitBlobID(roster),
		Personas: []NativePersona{{
			Name: "alpha",
			Files: []RoleFile{{
				Name: "AGENTS.md", Bytes: body,
			}},
		}},
	}
	before := repoPorcelain(t)
	log := filepath.Join(t.TempDir(), "git.log")
	oldPath := os.Getenv("PATH")
	wrapAttestGit(t, log)
	t.Setenv("GIT_CONFIG", filepath.Join(t.TempDir(), "evil"))
	if err := AttestRoleSource(context.Background(), dir, commit, tree); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(recorded)
	if strings.Contains(text, "\nfetch ") || strings.Contains(text, " fetch") || !strings.Contains(text, "core.hooksPath=") {
		t.Fatalf("git log %s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "GIT_CONFIG=") {
			t.Fatalf("parent GIT_CONFIG was copied: %s", line)
		}
	}
	t.Setenv("PATH", oldPath)
	if after := repoPorcelain(t); after != before {
		t.Fatalf("factory status changed\n%s", after)
	}

	bad := tree
	bad.Personas = append([]NativePersona{}, tree.Personas...)
	bad.Personas[0].Files = append([]RoleFile{}, tree.Personas[0].Files...)
	bad.Personas[0].Files[0].Bytes = append([]byte{}, body...)
	bad.Personas[0].Files[0].Bytes[0] = 'Q'
	err = AttestRoleSource(context.Background(), dir, commit, bad)
	if err == nil || !strings.Contains(err.Error(), "org/agents/alpha/AGENTS.md") || !strings.Contains(err.Error(), "byte mismatch") || strings.Contains(err.Error(), "ROLEBODY-7f3a") {
		t.Fatal(err)
	}
	missing := tree
	missing.Personas = append([]NativePersona{}, tree.Personas...)
	missing.Personas[0].Files = append([]RoleFile{}, tree.Personas[0].Files...)
	missing.Personas[0].Files = append(missing.Personas[0].Files, RoleFile{Name: "MISSING.md", Bytes: []byte("nope")})
	err = AttestRoleSource(context.Background(), dir, commit, missing)
	if err == nil || !strings.Contains(err.Error(), "org/agents/alpha/MISSING.md") || !strings.Contains(err.Error(), "absent object") || strings.Contains(err.Error(), "ROLEBODY-7f3a") {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD:org/roster.yaml"))
	err = AttestRoleSource(context.Background(), dir, blob, tree)
	if err == nil || !strings.Contains(err.Error(), blob) || !strings.Contains(err.Error(), "not a commit") {
		t.Fatal(err)
	}
	err = AttestRoleSource(context.Background(), t.TempDir(), commit, tree)
	if err == nil || !strings.Contains(err.Error(), "not a commit") {
		t.Fatal(err)
	}
}

func writeRole(t *testing.T, root, slug string, body []byte) {
	t.Helper()
	dir := filepath.Join(root, "org", "agents", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if strings.TrimSpace(gitOut(t, dir, args...)) == "" && args[0] == "init" {
		return
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v %s", args, err, out)
	}
	return string(out)
}

func wrapAttestGit(t *testing.T, log string) func() {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(log) + "\nenv >> " + shellQuote(log) + "\nexec " + shellQuote(real) + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+old)
	return func() {}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func repoPorcelain(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod")
		}
		dir = parent
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
