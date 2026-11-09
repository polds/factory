package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoTestJSONStatuses(t *testing.T) {
	if classifyGoTest("PASS\nok example 0.1s\n", 0) != EvidenceSkipOnly {
		t.Fatal("PASS line counted as success")
	}
	dir := t.TempDir()
	writeEvidenceModule(t, dir, map[string]string{
		"skip/skip_test.go": "package skip\nimport \"testing\"\nfunc TestSkip(t *testing.T) { t.Skip(\"later\") }\n",
	})
	opt := evidenceOpt(t, dir)
	status, body := runGoTest(context.Background(), opt, "./skip")
	if status != EvidenceSkipOnly || !strings.Contains(body, `"Action":"skip"`) {
		t.Fatalf("skip %s", status)
	}

	empty := t.TempDir()
	writeEvidenceModule(t, empty, map[string]string{
		"empty/empty.go": "package empty\nfunc Nop() {}\n",
	})
	status, body = runGoTest(context.Background(), evidenceOpt(t, empty), "./empty")
	if status != EvidenceSkipOnly || strings.Contains(body, `"Action":"run"`) {
		t.Fatalf("empty %s", status)
	}

	passDir := evidenceRepo(t)
	var buf bytes.Buffer
	log := NewLog(&buf, time.Now)
	task := TaskFile{EvidencePackage: "./oracle", Prompt: "go test ./stolen"}
	code := runEvidence(context.Background(), evidenceOpt(t, passDir), task, log, testFinish(log))
	events := parseEvidence(t, &buf)
	if code != ExitOK || !hasEvidence(events, EvidenceTestsPassed) || hasEvidence(events, EvidenceSkipOnly) {
		t.Fatalf("code %d events %s", code, buf.String())
	}
	text := evidenceText(events, EvidenceTestsPassed)
	if !strings.Contains(text, `"Action":"run"`) || !strings.Contains(text, `"Action":"pass"`) {
		t.Fatal("pass action missing from capped text")
	}
	if strings.Contains(buf.String(), `"type":"tool.result"`) {
		t.Fatal("transcript used tool.result")
	}

	missing := evidenceOpt(t, passDir)
	missing.Env = []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
	buf.Reset()
	log = NewLog(&buf, time.Now)
	code = runEvidence(context.Background(), missing, task, log, testFinish(log))
	events = parseEvidence(t, &buf)
	if code == ExitOK || !hasEvidence(events, EvidenceToolError) || hasEvidence(events, EvidenceTestsFailed) {
		t.Fatalf("missing go code %d %s", code, buf.String())
	}
}

func TestTranscriptCapRedactsAndDigests(t *testing.T) {
	secret := "CANARY-SECRET-VALUE"
	raw := strings.Repeat("A", EvidenceCap) + secret + strings.Repeat("B", 128)
	redacted := Redact(raw, RedactionNeedles([]string{secret}))
	text, digest := capTranscript(redacted)
	if len(text) > EvidenceCap || !strings.HasSuffix(text, EvidenceTruncated) || strings.Contains(text, secret) {
		t.Fatalf("cap %d marker %v", len(text), strings.HasSuffix(text, EvidenceTruncated))
	}
	if digest != Digest(redacted) || strings.Contains(digest, secret) {
		t.Fatal("digest is not the redacted bytes")
	}
	short, shortDigest := capTranscript("ok")
	if short != "ok" || shortDigest != Digest("ok") || strings.Contains(short, EvidenceTruncated) {
		t.Fatal("short transcript")
	}
}

func TestMutationRedRestoreGreen(t *testing.T) {
	dir := evidenceRepo(t)
	target := filepath.Join(dir, "oracle", "oracle.go")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := head(t, dir)
	logPath := filepath.Join(t.TempDir(), "git.log")
	wrapGit(t, logPath)
	var buf bytes.Buffer
	log := NewLog(&buf, time.Now)
	task := TaskFile{EvidencePackage: "./oracle", MutationPath: "oracle/oracle.go"}
	code := runEvidence(context.Background(), evidenceOpt(t, dir), task, log, testFinish(log))
	events := parseEvidence(t, &buf)
	if code != ExitOK || !hasEvidence(events, EvidenceTestsFailed) || !hasEvidence(events, EvidenceTestsPassed) {
		t.Fatalf("code %d %s", code, buf.String())
	}
	if head(t, dir) != headBefore {
		t.Fatal("HEAD changed")
	}
	if strings.TrimSpace(gitOut(t, dir, "status", "--porcelain")) != "" {
		t.Fatal("porcelain")
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("restore bytes")
	}
	recorded, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{" checkout", " clean", " reset"} {
		if strings.Contains(string(recorded), bad) {
			t.Fatalf("git %s", bad)
		}
	}
	if !strings.Contains(string(recorded), "cat-file") {
		t.Fatal("restore did not use cat-file")
	}
}

func TestMutationRefusesEscapeWithoutClean(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := evidenceRepo(t)
		outside := filepath.Join(t.TempDir(), "outside.go")
		if err := os.WriteFile(outside, []byte("package oracle\nfunc Marker() string { return \"factoryEvidencePass\" }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "oracle", "oracle.go")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "oracle", "oracle.go")); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(t.TempDir(), "git.log")
		wrapGit(t, logPath)
		code, _ := runMutationEvidence(t, dir, "oracle/oracle.go")
		body, err := os.ReadFile(outside)
		if err != nil || !strings.Contains(string(body), evidenceKeep) || code == ExitOK {
			t.Fatalf("code %d outside changed", code)
		}
		assertNoClean(t, logPath)
	})
	t.Run("dotdot", func(t *testing.T) {
		dir := evidenceRepo(t)
		outside := filepath.Join(filepath.Dir(dir), "outside.go")
		if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(t.TempDir(), "git.log")
		wrapGit(t, logPath)
		code, _ := runMutationEvidence(t, dir, "../outside.go")
		body, err := os.ReadFile(outside)
		if err != nil || string(body) != "keep\n" || code == ExitOK {
			t.Fatalf("code %d outside %q %v", code, body, err)
		}
		assertNoClean(t, logPath)
	})
	t.Run("second dirty", func(t *testing.T) {
		dir := evidenceRepo(t)
		extra := filepath.Join(dir, "note.txt")
		if err := os.WriteFile(extra, []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "oracle", "oracle.go")
		before, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(t.TempDir(), "git.log")
		wrapGit(t, logPath)
		code, _ := runMutationEvidence(t, dir, "oracle/oracle.go")
		after, err := os.ReadFile(target)
		extraBody, extraErr := os.ReadFile(extra)
		if err != nil || extraErr != nil || code == ExitOK || !bytes.Equal(before, after) || string(extraBody) != "dirty\n" {
			t.Fatalf("code %d", code)
		}
		assertNoClean(t, logPath)
	})
}

func TestEvidenceIgnoresPromptAndSkipsModel(t *testing.T) {
	dir := evidenceRepo(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	opt := evidenceOpt(t, dir)
	opt.Env = append(opt.Env, "OPENAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test-key")
	opt.Args = []string{"agent", "run", "--task-id", "proof", "--model", "fake-model", "--idle-on-complete", "0s"}
	raw, err := json.Marshal(TaskFile{Prompt: "go test ./stolen", EvidencePackage: "./oracle"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opt.RuntimeDir, "task.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	code := Run(context.Background(), opt)
	if code != ExitOK || calls.Load() != 0 {
		t.Fatalf("code %d calls %d", code, calls.Load())
	}
	body, err := os.ReadFile(filepath.Join(opt.RuntimeDir, "events.ndjson"))
	if err != nil || !strings.Contains(string(body), `"type":"evidence.test"`) || strings.Contains(string(body), `"type":"tool.result"`) {
		t.Fatalf("events %s %v", body, err)
	}

	plain := evidenceOpt(t, dir)
	plain.Env = append(plain.Env, "OPENAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test-key", "FACTORY_TASK_PROMPT=go test ./stolen")
	plain.Args = opt.Args
	if err := os.WriteFile(filepath.Join(plain.RuntimeDir, "task.json"), []byte("{\"prompt\":\"go test ./stolen\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	code = Run(context.Background(), plain)
	events, err := os.ReadFile(filepath.Join(plain.RuntimeDir, "events.ndjson"))
	if err != nil || code != ExitOK || calls.Load() == before || strings.Contains(string(events), `"type":"evidence.test"`) {
		t.Fatalf("code %d calls %d events %s", code, calls.Load(), events)
	}
}

func TestMaterializeEvidenceFieldsComeFromEnv(t *testing.T) {
	source, _, pin := twoCommitRepo(t, "https://github.com/example/fixture.git")
	workspace := filepath.Join(t.TempDir(), "workspace")
	opt := proofOptions(t, workspace, "", source, pin, "go test ./stolen")
	opt.Env = append(opt.Env,
		EnvEvidencePackage+"=./oracle",
		EnvEvidenceMutation+"=oracle/oracle.go",
	)
	task, err := MaterializeAssigned(context.Background(), opt, ParseArgs(opt.Args))
	if err != nil {
		t.Fatal(err)
	}
	if task.EvidencePackage != "./oracle" || task.MutationPath != "oracle/oracle.go" || task.Prompt != "go test ./stolen" {
		t.Fatalf("task %+v", task)
	}
	opt.Env = append(opt.Env, EnvEvidenceMutation+"=../secret")
	if _, err := MaterializeAssigned(context.Background(), opt, ParseArgs(opt.Args)); err == nil {
		t.Fatal("dotdot path written")
	}
}

func TestCIPostgresImageUnchanged(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("image: postgres:16-alpine")) || bytes.Contains(body, []byte("pgvector/pgvector")) {
		t.Fatal("ci postgres image changed")
	}
}

func TestCheckoutPrecedesEvidence(t *testing.T) {
	dir := evidenceRepo(t)
	target := filepath.Join(dir, "oracle", "oracle.go")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pin := head(t, dir)
	opt := evidenceOpt(t, dir)
	opt.Args = []string{"agent", "run", "--task-id", "proof", "--model", "fake-model", "--idle-on-complete", "0s", "--repository-head-override-json", overrideJSON(t, "example", "fixture", pin)}
	raw, err := json.Marshal(TaskFile{EvidencePackage: "./oracle", MutationPath: "oracle/oracle.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opt.RuntimeDir, "task.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(srv.Close)
	opt.Env = append(opt.Env, "OPENAI_BASE_URL="+srv.URL)
	code := Run(context.Background(), opt)
	after, err := os.ReadFile(target)
	events, evErr := os.ReadFile(filepath.Join(opt.RuntimeDir, "events.ndjson"))
	if err != nil || evErr != nil || code == ExitOK || calls.Load() != 0 || !bytes.Equal(before, after) || strings.Contains(string(events), `"type":"evidence.test"`) {
		t.Fatalf("code %d calls %d", code, calls.Load())
	}
}

func evidenceOpt(t *testing.T, dir string) Options {
	t.Helper()
	return Options{
		Env:        []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GOTOOLCHAIN=local"},
		Workspace:  dir,
		RuntimeDir: t.TempDir(),
	}
}

func testFinish(log *Log) func(int, string, string) int {
	return func(code int, status, summary string) int {
		_ = log.Emit(Event{Type: "terminal", Status: status, Summary: summary})
		return code
	}
}

func parseEvidence(t *testing.T, buf *bytes.Buffer) []Event {
	t.Helper()
	var out []Event
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func hasEvidence(events []Event, status string) bool {
	for _, ev := range events {
		if ev.Type == EventEvidenceTest && ev.Status == status {
			return true
		}
	}
	return false
}

func evidenceText(events []Event, status string) string {
	for _, ev := range events {
		if ev.Type == EventEvidenceTest && ev.Status == status {
			return ev.Text
		}
	}
	return ""
}

func runMutationEvidence(t *testing.T, dir, rel string) (int, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	log := NewLog(&buf, time.Now)
	code := runEvidence(context.Background(), evidenceOpt(t, dir), TaskFile{EvidencePackage: "./oracle", MutationPath: rel}, log, testFinish(log))
	return code, &buf
}

func assertNoClean(t *testing.T, logPath string) {
	t.Helper()
	recorded, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	text := string(recorded)
	for _, bad := range []string{" checkout", " clean", " reset"} {
		if strings.Contains(text, bad) {
			t.Fatalf("git %s", bad)
		}
	}
}

func writeEvidenceModule(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/evidence\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func evidenceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEvidenceModule(t, dir, map[string]string{
		"oracle/oracle.go":      "package oracle\n\nfunc Marker() string { return \"factoryEvidencePass\" }\n",
		"oracle/oracle_test.go": "package oracle\n\nimport \"testing\"\n\nfunc TestMarker(t *testing.T) {\n\tif Marker() != \"factoryEvidencePass\" {\n\t\tt.Fatal(\"marker\")\n\t}\n}\n",
	})
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "t@example.com")
	gitCmd(t, dir, "config", "user.name", "t")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "oracle")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/fixture.git")
	gitCmd(t, dir, "checkout", "--detach", "HEAD")
	return dir
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
			t.Fatal("go.mod")
		}
		dir = parent
	}
}
