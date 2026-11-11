package dreaminglive

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidateRefusesTamperedBundle(t *testing.T) {
	dir, head, parent := makeBundle(t, map[string]string{"oracle/tenant_test.go": "package oracle\n"})
	t.Setenv("DREAMING_PIN", parent)
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "oracle")
	t.Setenv("FACTORY_PIN_DIR", "")
	writeBundleManifest(t, dir, head, parent)
	bundle := filepath.Join(dir, "head.bundle")
	raw, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(bundle, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ValidateChainArtifact(dir)
	if err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered bundle: %v", err)
	}
}

func TestValidateRefusesWrongParent(t *testing.T) {
	dir, head, _ := makeBundle(t, map[string]string{"oracle/tenant_test.go": "package oracle\n"})
	t.Setenv("DREAMING_PIN", "")
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "oracle")
	t.Setenv("FACTORY_PIN_DIR", "")
	// The manifest claims the hard-coded pin. The commit's parent is not that pin.
	writeBundleManifest(t, dir, head, DreamingPin)
	_, err := ValidateChainArtifact(dir)
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("wrong parent: %v", err)
	}
}

func TestValidateRefusesOutOfScopePath(t *testing.T) {
	dir, head, parent := makeBundle(t, map[string]string{"EVIL.md": "not reviewed\n"})
	t.Setenv("DREAMING_PIN", parent)
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "oracle")
	t.Setenv("FACTORY_PIN_DIR", "")
	writeBundleManifest(t, dir, head, parent)
	_, err := ValidateChainArtifact(dir)
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("out of scope: %v", err)
	}
}

func TestValidateAcceptsDerivedParent(t *testing.T) {
	dir, head, parent := makeBundle(t, map[string]string{"oracle/tenant_test.go": "package oracle\n"})
	t.Setenv("DREAMING_PIN", parent)
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "oracle")
	t.Setenv("FACTORY_PIN_DIR", "")
	// The manifest's parent field is a lie. The derived parent is the pin.
	writeBundleManifest(t, dir, head, DreamingPin)
	if _, err := ValidateChainArtifact(dir); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialHelperFeedsGitPush(t *testing.T) {
	token := "ghs_helper_push_not_a_secret"
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	project := t.TempDir()
	repo := filepath.Join(project, "northstar-ai-labs", "dreaming.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, project, "init", "--bare", "-b", "main", repo)
	gitCmd(t, repo, "config", "http.receivepack", "true")
	var mu sync.Mutex
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			mu.Lock()
			seen = auth
			mu.Unlock()
		}
		if auth != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		serveGitHTTP(w, r, project)
	}))
	t.Cleanup(srv.Close)
	helper, err := writeCredentialHelper(token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(helper) })
	info, err := os.Stat(helper)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("helper mode %o", info.Mode().Perm())
	}
	local := t.TempDir()
	gitCmd(t, local, "init", "-b", "main")
	gitCmd(t, local, "config", "user.email", "dreaming@example.com")
	gitCmd(t, local, "config", "user.name", "dreaming")
	if err := os.WriteFile(filepath.Join(local, "f"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, local, "add", "f")
	gitCmd(t, local, "commit", "-m", "x")
	remote := srv.URL + "/northstar-ai-labs/dreaming.git"
	push := exec.Command("git", "-c", "credential.helper=", "-c", "credential.helper=!"+helper, "push", remote, "HEAD:refs/heads/demo")
	push.Dir = local
	push.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + local,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	mu.Lock()
	got := seen
	mu.Unlock()
	if got != want {
		t.Fatalf("git did not obtain the helper credential: %q", got)
	}
	if revParse(repo, "refs/heads/demo") == "" {
		t.Fatal("push did not update the remote")
	}
}

func makeBundle(t *testing.T, files map[string]string) (dir, head, parent string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + repo,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=dreaming", "GIT_AUTHOR_EMAIL=dreaming@example.com",
			"GIT_COMMITTER_NAME=dreaming", "GIT_COMMITTER_EMAIL=dreaming@example.com",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("pin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README")
	git("commit", "-m", "pin")
	parent = git("rev-parse", "HEAD")
	for rel, body := range files {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "--", rel)
	}
	git("commit", "-m", "change")
	head = git("rev-parse", "HEAD")
	dir = t.TempDir()
	git("bundle", "create", filepath.Join(dir, "head.bundle"), "HEAD")
	return dir, head, parent
}

func writeBundleManifest(t *testing.T, dir, head, parent string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "head.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := ChainManifest{
		Pin: parent, Head: head, Parent: parent,
		BundleSHA256: hex.EncodeToString(sum[:]),
		Package:      "oracle", MutationPath: "oracle/tenant_test.go",
		EvidenceRun: true, SpendCents: 1, CeilingCents: 2500,
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func serveGitHTTP(w http.ResponseWriter, r *http.Request, project string) {
	backend := "/usr/lib/git-core/git-http-backend"
	cmd := exec.Command(backend)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_PROJECT_ROOT=" + project,
		"GIT_HTTP_EXPORT_ALL=1",
		"REQUEST_METHOD=" + r.Method,
		"PATH_INFO=" + r.URL.Path,
		"QUERY_STRING=" + r.URL.RawQuery,
		"CONTENT_TYPE=" + r.Header.Get("Content-Type"),
		"REMOTE_USER=git",
		"REMOTE_ADDR=127.0.0.1",
	}
	cmd.Stdin = r.Body
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		http.Error(w, "git backend", http.StatusBadGateway)
		return
	}
	raw := stdout.Bytes()
	head, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		head, body, ok = bytes.Cut(raw, []byte("\n\n"))
	}
	if !ok {
		http.Error(w, "git cgi", http.StatusBadGateway)
		return
	}
	status := http.StatusOK
	for _, line := range bytes.Split(head, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		name, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		if strings.EqualFold(string(name), "Status") {
			continue
		}
		w.Header().Add(string(bytes.TrimSpace(name)), strings.TrimSpace(string(value)))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
