package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/defsync"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

// TestE2EM6 is the M6 factoryd loop. Subtest names are the brief's subcases.
// Subcase 1 runs judge prompts through the real harness. The other subcases
// post the same verdict JSON the harness would emit, so repeats stay inside
// the race budget.
func TestE2EM6(t *testing.T) {
	skipNoDB(t)
	t.Run("1", testE2EM6ScoreAndPR)
	t.Run("2", testE2EM6RunNowConflict)
	t.Run("3", testE2EM6GuardRails)
	t.Run("4", testE2EM6CloseReviews)
	t.Run("5", testE2EM6Benchmark)
	t.Run("6", testE2EM6MeasurementNotSampled)
	t.Run("7", testE2EM6IneligibleNotSampled)
	t.Run("8", testE2EM6SecretWalk)
	t.Run("9", testE2EM6Restart)
	t.Run("10", testE2EM6OneLeader)
}

func TestM6LocalImprovementSkips(t *testing.T) {
	skipNoDB(t)
	env := bootM6(t, m6Boot{threshold: 1})
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/improvement:run-now", "{}")
	if code != http.StatusAccepted {
		t.Fatalf("run-now %d %s", code, raw)
	}
	if !strings.Contains(env.logs.String(), "self-improvement skipped: factory has no definition git url") {
		t.Fatalf("log %s", env.logs.String())
	}
	var body api.ImprovementRunNow
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.CheckedAgents) != 0 || len(body.Batches) != 0 {
		t.Fatalf("body %+v", body)
	}
}

const (
	m6FailMarker  = "M6_FAIL_MARKER"
	m6SkillMarker = "SKILL_BODY_MARKER"
	m6GitURL      = "https://github.com/acme/factory.git"
	m6Minted      = "ghs_m6_installation_token"
)

func shaN(b byte) string { return strings.Repeat(string(b), 40) }

type m6TreeKind int

const (
	m6TreeProd m6TreeKind = iota
	m6TreeSkillHead
	m6TreeWeaken
	m6TreeEscalate
	m6TreeCandidate
)

type m6Boot struct {
	threshold int
	cloner    defsync.Cloner
	gh        *m6GitHub
	git       bool
	slots     int
}

func bootM6(t *testing.T, opt m6Boot) *env {
	t.Helper()
	if opt.threshold <= 0 {
		opt.threshold = 2
	}
	var key *rsa.PrivateKey
	if opt.git {
		var err error
		key, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		if opt.gh == nil {
			opt.gh = newM6GitHub(t)
		}
		if opt.cloner == nil {
			opt.cloner = &m6Cloner{threshold: opt.threshold, kindFor: func(int) m6TreeKind { return m6TreeSkillHead }}
		}
		if row, ok := opt.cloner.(*m6Cloner); ok {
			row.threshold = opt.threshold
		}
	}
	return boot(t, bootOpts{
		definition: func(t *testing.T) string {
			t.Helper()
			return writeM6Definition(t, opt.threshold)
		},
		gitURL:        map[bool]string{true: m6GitURL}[opt.git],
		gitRef:        "main",
		gitCloner:     opt.cloner,
		maxConcurrent: opt.slots,
		configure: func(cfg *Config) {
			if !opt.git {
				return
			}
			cfg.GitHubAppID = 99
			cfg.GitHubAppKey = key
			cfg.GitHubTokenMode = tokenModeApp
			cfg.GitHubAPIBase = opt.gh.URL
			cfg.ReviewerAdmins = []string{"ada"}
			cfg.DefinitionGitRef = "main"
		},
	})
}

func writeM6Definition(t *testing.T, threshold int) string {
	t.Helper()
	root := t.TempDir()
	if err := writeM6Tree(root, m6TreeProd, threshold); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeM6Tree(dir string, kind m6TreeKind, threshold int) error {
	if threshold <= 0 {
		threshold = 2
	}
	required := "true"
	if kind == m6TreeWeaken {
		required = "false"
	}
	prompt := "BASELINE_TASK"
	head := shaN('d')
	if kind == m6TreeCandidate {
		prompt = "CANDIDATE_TASK"
		head = shaN('e')
	}
	skill := m6SkillMarker + " original"
	if kind == m6TreeSkillHead {
		skill = m6SkillMarker + " improved"
	}
	files := map[string]string{
		"factory.yaml": fmt.Sprintf(`
schemaVersion: v1alpha1
name: m2
agentDefaults:
  model: auto
  runner: local
  pool: standard
scorerDefaults:
  runner: local
  model: test-model
selfImprovement:
  failedRunThreshold: %d
  staleFailureDays: 7
  reviewerType: admins
pipeline:
  stages: [building]
  agents:
    building: implementer
  approvals:
    spec:
      required: %s
      approvers: ada
`, threshold, required),
		"runners/local.yaml": `
description: Local docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-m1-task:dev
`,
		"agents/implementer/agent.md": `
---
description: Implements the requested change.
agentType: IMPLEMENT
model: test-model
secrets:
  - NPM_TOKEN
maxTurns: 7
---

Do the task.
`,
		"agents/implementer/skills/review/SKILL.md": skill,
		"scorers/review/scorer.md": `
---
description: Did the change miss the marker?
agents:
  - implementer
output: classification
labels:
  - value: pass
    description: The transcript has no failure marker.
    score: 1
  - value: fail
    description: The transcript contains the failure marker.
    score: 0
passingScore: 1
samplingRate: 100
model: test-model
selfImprovement: true
---

Return fail when the transcript contains the marker.
`,
		"benchmarks/pr-review/suite.yaml": `
name: pr-review
agent: implementer
configurations:
  - name: Baseline
    agents:
      - agent: implementer
        model: test-model
  - name: Candidate
    definitionRef: candidate
    agents:
      - agent: implementer
        model: test-model
tasks:
  - doc
`,
		"benchmarks/pr-review/tasks/doc.yaml": fmt.Sprintf(`
title: Doc
prompt: %s
successCriteria: The change matches the run outcome.
startingRepoRefs:
  - github.com:acme/widget@%s
`, prompt, head),
	}
	for path, body := range files {
		if err := writeTreeFile(filepath.Join(dir, path), body); err != nil {
			return err
		}
	}
	if kind == m6TreeSkillHead {
		for _, name := range []string{"a.md", "b.md", "c.md"} {
			if err := writeTreeFile(filepath.Join(dir, "notes", name), "changed "+name); err != nil {
				return err
			}
		}
	}
	if kind == m6TreeEscalate {
		if err := writeTreeFile(filepath.Join(dir, "scorers/extra/scorer.md"), `
---
description: A new self-improvement scorer.
agents:
  - implementer
output: classification
labels:
  - value: pass
    description: Pass.
    score: 1
  - value: fail
    description: Fail.
    score: 0
passingScore: 1
samplingRate: 0
model: test-model
selfImprovement: true
---

Extra.
`); err != nil {
			return err
		}
	}
	return nil
}

func writeTreeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644)
}

type m6Cloner struct {
	mu          sync.Mutex
	threshold   int
	improve     int
	kindFor     func(n int) m6TreeKind
	lastImprove string
}

func (c *m6Cloner) Clone(_ context.Context, _, ref, dest string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kind := m6TreeProd
	sha := shaN('a')
	switch {
	case ref == "main" || ref == shaN('a'):
	case ref == "candidate" || ref == shaN('b'):
		kind, sha = m6TreeCandidate, shaN('b')
	case strings.HasPrefix(ref, "improve/"):
		c.improve++
		kind = m6TreeSkillHead
		if c.kindFor != nil {
			kind = c.kindFor(c.improve)
		}
		// The server caches a clone by the commit it returns. Two tree
		// kinds are different commits, so a later branch is not served
		// the first branch's files.
		sha = shaN('c')
		switch kind {
		case m6TreeWeaken:
			sha = shaN('8')
		case m6TreeEscalate:
			sha = shaN('9')
		}
		c.lastImprove = dest
	default:
		return "", fmt.Errorf("unexpected ref %s", ref)
	}
	if err := writeM6Tree(dest, kind, c.threshold); err != nil {
		return "", err
	}
	return sha, nil
}

func (c *m6Cloner) improveDir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastImprove
}

type m6GitHub struct {
	URL    string
	mu     sync.Mutex
	perms  []map[string]string
	pulls  []m6PR
	bad    int
	revs   []string
	labs   []string
	checks []m6Check
}

type m6PR struct {
	Number int
	URL    string
	Head   string
	Body   string
	Title  string
}

type m6Check struct {
	Name       string
	Conclusion string
	SHA        string
}

func newM6GitHub(t *testing.T) *m6GitHub {
	t.Helper()
	f := &m6GitHub{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *m6GitHub) serve(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/installation"):
		writeJSON(w, map[string]any{"id": 7})
	case r.Method == http.MethodPost && strings.Contains(path, "/access_tokens"):
		var req struct {
			Permissions map[string]string `json:"permissions"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.perms = append(f.perms, req.Permissions)
		f.mu.Unlock()
		writeJSON(w, map[string]any{
			"token":      m6Minted,
			"expires_at": time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339),
		})
	case r.Method == http.MethodPost && strings.Contains(path, "/requested_reviewers"):
		var req struct {
			Reviewers []string `json:"reviewers"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.revs = append(f.revs, req.Reviewers...)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && strings.Contains(path, "/labels"):
		var req struct {
			Labels []string `json:"labels"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.labs = append(f.labs, req.Labels...)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/check-runs"):
		var req struct {
			Name       string `json:"name"`
			HeadSHA    string `json:"head_sha"`
			Conclusion string `json:"conclusion"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.checks = append(f.checks, m6Check{Name: req.Name, Conclusion: req.Conclusion, SHA: req.HeadSHA})
		id := len(f.checks)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"id": id})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/pulls"):
		var req struct {
			Title string `json:"title"`
			Head  string `json:"head"`
			Body  string `json:"body"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		if len(f.pulls) > 0 {
			f.bad++
			f.mu.Unlock()
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"A pull request already exists"}`))
			return
		}
		pr := m6PR{Number: 41, URL: "https://github.com/acme/factory/pull/41", Head: req.Head, Body: req.Body, Title: req.Title}
		f.pulls = append(f.pulls, pr)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"number": pr.Number, "html_url": pr.URL})
	case r.Method == http.MethodGet && strings.Contains(path, "/pulls"):
		f.mu.Lock()
		rows := append([]m6PR(nil), f.pulls...)
		f.mu.Unlock()
		out := make([]map[string]any, 0, len(rows))
		for _, pr := range rows {
			out = append(out, map[string]any{"number": pr.Number, "html_url": pr.URL})
		}
		writeJSON(w, out)
	default:
		http.NotFound(w, r)
	}
}

func (f *m6GitHub) sawPerms(want map[string]string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.perms {
		if samePerms(got, want) {
			return true
		}
	}
	return false
}

func (f *m6GitHub) snapshot() (pulls []m6PR, revs, labs []string, checks []m6Check, bad int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]m6PR(nil), f.pulls...), append([]string(nil), f.revs...), append([]string(nil), f.labs...), append([]m6Check(nil), f.checks...), f.bad
}

type m6Model struct {
	URL    string
	mu     sync.Mutex
	bodies []string
}

func startM6Model(t *testing.T) *m6Model {
	t.Helper()
	m := &m6Model{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.bodies = append(m.bodies, string(raw))
		m.mu.Unlock()
		text := m6ModelText(string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m6Chat(text)))
	}))
	t.Cleanup(srv.Close)
	m.URL = srv.URL
	return m
}

func (m *m6Model) saw(needle string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, body := range m.bodies {
		if strings.Contains(body, needle) {
			return true
		}
	}
	return false
}

func m6ModelText(body string) string {
	if strings.Contains(body, "You are the scorer") && strings.Contains(body, m6FailMarker) {
		return `{"label":"fail","reasoning":"the transcript contains the failure marker"}`
	}
	if strings.Contains(body, "You are the scorer") {
		return `{"label":"pass","reasoning":"criteria met"}`
	}
	return m6FailMarker
}

func m6Chat(content string) string {
	raw, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
		"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 4},
	})
	if err != nil {
		return `{"choices":[{"message":{"role":"assistant","content":"pass"}}]}`
	}
	return string(raw)
}

func startM6Harness(t *testing.T, asg protocol.TaskAssignment, modelURL string) *harnessCmd {
	t.Helper()
	bin := ensureHarness(t)
	rt := t.TempDir()
	ws := t.TempDir()
	prompt := ""
	if asg.Task != nil {
		prompt = asg.Task.Definition.Prompt
	}
	raw, err := json.Marshal(map[string]string{"prompt": prompt, "model_id": "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"FACTORY_RUNTIME_DIR=" + rt,
		"OPENAI_BASE_URL=" + modelURL,
	}
	for key, value := range asg.EnvVars {
		if key == "OPENAI_BASE_URL" {
			continue
		}
		env = append(env, key+"="+value)
	}
	cmd := exec.Command(bin, "--task-id", asg.TaskID, "--model", "test-model", "--idle-on-complete", "0s")
	cmd.Dir = ws
	cmd.Env = env
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	out := &harnessCmd{cmd: cmd, stderr: stderr, done: make(chan error, 1)}
	go func() { out.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return out
}

func testE2EM6ScoreAndPR(t *testing.T) {
	t.Helper()
	gh := newM6GitHub(t)
	cloner := &m6Cloner{kindFor: func(int) m6TreeKind { return m6TreeSkillHead }}
	env := bootM6(t, m6Boot{threshold: 2, git: true, gh: gh, cloner: cloner})
	model := startM6Model(t)
	w := m6Dial(t, env, "m6-1")
	defer w.close()
	ids := m6FailingRuns(t, env, w, model, 2, true)
	m6Tick(t, env.srv.SampleTick)
	m6FinishPending(t, env, w, model, true)
	m6WaitScores(t, env, ids, false)
	m6Tick(t, env.srv.ImproveTick)
	m6FinishPending(t, env, w, model, true)
	batch := m6WaitBatch(t, env, api.BatchPROpen)
	pulls, revs, labs, checks, bad := gh.snapshot()
	if len(pulls) != 1 || bad != 0 {
		t.Fatalf("pulls %+v bad %d", pulls, bad)
	}
	if !strings.Contains(pulls[0].Body, "## Regressions addressed") {
		t.Fatalf("body %s", pulls[0].Body)
	}
	for _, id := range ids {
		if !strings.Contains(pulls[0].Body, id) {
			t.Fatalf("body missing %s", id)
		}
	}
	if len(revs) == 0 || len(revs) > 3 || revs[0] != "ada" {
		t.Fatalf("reviewers %v", revs)
	}
	if !containsString(labs, "needs-human-design") {
		t.Fatalf("labels %v", labs)
	}
	if len(checks) != 1 || checks[0].Name != "factory/config" || checks[0].Conclusion != "success" || checks[0].SHA != shaN('c') {
		t.Fatalf("checks %+v", checks)
	}
	if !gh.sawPerms(improvementTaskPermissions()) || !gh.sawPerms(improvementPRPermissions()) {
		t.Fatal("permission maps were not both minted")
	}
	skill := filepath.Join(cloner.improveDir(), "agents", "implementer", "skills", "review", "SKILL.md")
	body, err := os.ReadFile(skill)
	if err != nil || !strings.Contains(string(body), "improved") {
		t.Fatalf("skill head %s %v", skill, err)
	}
	if !model.saw("You are the scorer") || !model.saw(m6FailMarker) {
		t.Fatal("harness model did not see the judge prompt")
	}
	code, raw := env.authed(t, http.MethodGet, "/api/v1/factory/m2/inbox", "")
	if code != http.StatusOK {
		t.Fatalf("inbox %d %s", code, raw)
	}
	var items []api.InboxItem
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range items {
		if item.Kind == api.InboxKindImprovementPR && item.Detail == pulls[0].URL && item.RunID == batch.runID && item.ID == batch.id {
			found = true
		}
	}
	if !found {
		t.Fatalf("inbox %+v", items)
	}
	if strings.Contains(env.logs.String(), m6Minted) {
		t.Fatal("log contains the installation token")
	}
	forbidPlanted(t, "pr", pulls[0].Body, "")
}

func testE2EM6RunNowConflict(t *testing.T) {
	t.Helper()
	env := bootM6(t, m6Boot{threshold: 2, git: true})
	w := m6Dial(t, env, "m6-2")
	defer w.close()
	_ = m6FailingRuns(t, env, w, nil, 1, false)
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/improvement:run-now", "{}")
	if code != http.StatusAccepted {
		t.Fatalf("first run-now %d %s", code, raw)
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/factory/m2/improvement:run-now", "{}")
	if code != http.StatusConflict || !strings.Contains(string(raw), "already in flight") {
		t.Fatalf("second run-now %d %s", code, raw)
	}
}

func testE2EM6GuardRails(t *testing.T) {
	t.Helper()
	gh := newM6GitHub(t)
	cloner := &m6Cloner{kindFor: func(n int) m6TreeKind {
		if n == 1 {
			return m6TreeWeaken
		}
		return m6TreeEscalate
	}}
	env := bootM6(t, m6Boot{threshold: 1, git: true, gh: gh, cloner: cloner})
	w := m6Dial(t, env, "m6-3")
	defer w.close()
	_ = m6FailingRuns(t, env, w, nil, 1, false)
	m6RunNow(t, env, http.StatusAccepted)
	m6FinishPending(t, env, w, nil, false)
	failed := m6WaitBatch(t, env, api.BatchFailed)
	if !strings.Contains(failed.note, "weakened") {
		t.Fatalf("note %s", failed.note)
	}
	if pulls, _, _, _, _ := gh.snapshot(); len(pulls) != 0 {
		t.Fatalf("pulls %+v", pulls)
	}
	m6RunNow(t, env, http.StatusAccepted)
	m6FinishPending(t, env, w, nil, false)
	_ = m6WaitBatch(t, env, api.BatchPROpen)
	_, _, labs, _, _ := gh.snapshot()
	if !containsString(labs, "privilege-escalation") {
		t.Fatalf("labels %v", labs)
	}
	pulls, _, _, _, _ := gh.snapshot()
	if len(pulls) != 1 || !strings.Contains(pulls[0].Body, "Privilege escalation") {
		t.Fatalf("body %+v", pulls)
	}
}

func testE2EM6CloseReviews(t *testing.T) {
	t.Helper()
	env := bootM6(t, m6Boot{threshold: 1, git: true})
	w := m6Dial(t, env, "m6-4")
	defer w.close()
	ids := m6FailingRuns(t, env, w, nil, 1, false)
	m6RunNow(t, env, http.StatusAccepted)
	m6FinishPending(t, env, w, nil, false)
	batch := m6WaitBatch(t, env, api.BatchPROpen)
	branch := "improve/" + batch.id
	env.srv.notePRFact(context.Background(), event.Event{
		Provider:   event.ProviderGitHub,
		Event:      "pull_request_closed",
		OccurredAt: time.Now().UTC(),
		Actor:      event.Actor{Handle: "ada"},
		Subject: event.Subject{
			Number: batch.number,
			URL:    batch.prURL,
			Text:   "not this design",
			Repo:   event.Repo{Owner: "acme", Name: "factory"},
			Branch: &branch,
		},
	})
	closed := m6WaitBatch(t, env, api.BatchClosed)
	if closed.note != "not this design" {
		t.Fatalf("note %s", closed.note)
	}
	var open int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM scorer_results WHERE run_id = $1 AND reviewed_at IS NULL`, ids[0]).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatal("failure stayed unreviewed")
	}
}

func testE2EM6Benchmark(t *testing.T) {
	t.Helper()
	env := bootM6(t, m6Boot{threshold: 2, git: true, slots: 4})
	w := m6Dial(t, env, "m6-5")
	defer w.close()
	id := m6StartBenchmark(t, env, 5)
	seen := map[string]string{}
	m6Drain(t, env, w, nil, false, func() bool { return m6BenchDone(t, env, id) }, seen)
	m6AssertBench(t, env, id, seen, 5, "Candidate")
	four := m6StartBenchmark(t, env, 4)
	m6Drain(t, env, w, nil, false, func() bool { return m6BenchDone(t, env, four) }, nil)
	view := m6Trials(t, env, four)
	if view.Report.Winner != "" || !strings.Contains(view.Report.Reason, "has 4 repetitions") {
		t.Fatalf("report %+v", view.Report)
	}
}

func testE2EM6MeasurementNotSampled(t *testing.T) {
	t.Helper()
	env := bootM6(t, m6Boot{threshold: 2})
	fac, agent := env.factoryAgent(t)
	ctx := context.Background()
	work, err := env.srv.store.CreateRun(ctx, store.NewRun{
		FactoryID: fac, AgentID: agent, Pool: "standard", Priority: store.PriorityBatch,
		Prompt: "work", Title: "work", Purpose: api.PurposeWork,
	})
	if err != nil {
		t.Fatal(err)
	}
	var others []string
	for _, purpose := range []string{api.PurposeScorer, api.PurposeBenchmark, api.PurposeImprovement} {
		run, err := env.srv.store.CreateRun(ctx, store.NewRun{
			FactoryID: fac, Pool: "standard", Priority: store.PriorityBatch,
			Prompt: "measure", Title: "measure:" + purpose, Purpose: purpose,
		})
		if err != nil {
			t.Fatal(err)
		}
		others = append(others, run.ID)
	}
	m6ForceTerminal(t, env, append(others, work.ID), "succeeded", "")
	m6Tick(t, env.srv.SampleTick)
	if n := m6ResultCount(t, env, work.ID); n != 1 {
		t.Fatalf("work results %d", n)
	}
	for _, id := range others {
		if n := m6ResultCount(t, env, id); n != 0 {
			t.Fatalf("%s sampled %d", id, n)
		}
		if n := m6ChildCount(t, env, id); n != 0 {
			t.Fatalf("%s children %d", id, n)
		}
	}
}

func testE2EM6IneligibleNotSampled(t *testing.T) {
	t.Helper()
	env := bootM6(t, m6Boot{threshold: 2})
	w := m6Dial(t, env, "m6-7")
	okID := m6Start(t, env, "eligible", "")
	okAsg := w.readAssignment(t)
	m6PostText(t, w, okAsg, "clean")
	env.waitStatus(t, okID, "succeeded")
	infra := m6Start(t, env, "killed", "")
	infraAsg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: infra, ExecutionID: infraAsg.ExecutionID, Message: "killed",
		FailureReason: "container_exit", ExitCode: 137,
	})
	env.waitStatus(t, infra, "failed")
	if env.runDetail(t, infra).Failure.Class != "infrastructure" {
		t.Fatalf("class %+v", env.runDetail(t, infra).Failure)
	}
	w.close()
	cancelled := m6Start(t, env, "stop", "")
	env.waitStatus(t, cancelled, "queued")
	env.cancel(t, cancelled)
	env.waitStatus(t, cancelled, "cancelled")
	m6Tick(t, env.srv.SampleTick)
	if n := m6ResultCount(t, env, okID); n != 1 {
		t.Fatalf("eligible results %d", n)
	}
	if n := m6ResultCount(t, env, infra); n != 0 {
		t.Fatalf("infra sampled %d", n)
	}
	if n := m6ResultCount(t, env, cancelled); n != 0 {
		t.Fatalf("cancelled sampled %d", n)
	}
}

func testE2EM6SecretWalk(t *testing.T) {
	t.Helper()
	gh := newM6GitHub(t)
	env := bootM6(t, m6Boot{threshold: 1, git: true, gh: gh, slots: 4})
	w := m6Dial(t, env, "m6-8")
	defer w.close()
	repo := "github.com:acme/widget@" + shaN('d')
	withHead := m6Start(t, env, "capture me", repo)
	asg := w.readAssignment(t)
	m6PostText(t, w, asg, m6FailMarker)
	env.waitStatus(t, withHead, "succeeded")
	bare := m6Start(t, env, "no head", "")
	asg = w.readAssignment(t)
	m6PostText(t, w, asg, "done")
	env.waitStatus(t, bare, "succeeded")
	m6Tick(t, env.srv.SampleTick)
	m6FinishPending(t, env, w, nil, false)
	m6WaitScores(t, env, []string{withHead}, false)
	m6RunNow(t, env, http.StatusAccepted)
	m6FinishPending(t, env, w, nil, false)
	_ = m6WaitBatch(t, env, api.BatchPROpen)
	benchID := m6StartBenchmark(t, env, 1)
	m6Drain(t, env, w, nil, false, func() bool { return m6BenchDone(t, env, benchID) }, nil)
	code, raw := env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+withHead+":capture-benchmark-task", `{"suite":"pr-review","name":"doc"}`)
	if code != http.StatusOK {
		t.Fatalf("capture %d %s", code, raw)
	}
	forbidPlanted(t, "capture", string(raw), "")
	var captured api.CapturedBenchmarkTask
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Path != "benchmarks/pr-review/tasks/doc.yaml" || !strings.Contains(captured.Content, withHead) || len(captured.HeadRefs) != 1 || captured.HeadRefs[0].SHA != shaN('d') {
		t.Fatalf("captured %+v", captured)
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+bare+":capture-benchmark-task", `{"suite":"pr-review","name":"doc"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "no frozen 40-hex head") {
		t.Fatalf("capture 422 %d %s", code, raw)
	}
	forbidPlanted(t, "capture-422", string(raw), "")
	paths := []string{
		"/api/v1/factory/m2/scorers",
		"/api/v1/factory/m2/scorers/review/results",
		"/api/v1/factory/m2/benchmarks",
		"/api/v1/factory/m2/benchmarks/pr-review/trials?benchmark_run_id=" + benchID,
		"/api/v1/factory/m2/improvement-batches",
		"/api/v1/agent/runs/" + withHead + "/scores",
		"/api/v1/factory/m2/metrics?from=2020-01-01T00:00:00Z&to=2030-01-01T00:00:00Z",
		"/api/v1/factory/m2/inbox",
	}
	for _, path := range paths {
		code, raw = env.authed(t, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("%s %d %s", path, code, raw)
		}
		forbidPlanted(t, path, string(raw), "")
	}
	pulls, _, _, _, _ := gh.snapshot()
	if len(pulls) != 1 {
		t.Fatal("missing pr")
	}
	forbidPlanted(t, "pr", pulls[0].Body, "")
	if strings.Contains(env.logs.String(), m6Minted) {
		t.Fatal("log contains the installation token")
	}
}

func testE2EM6Restart(t *testing.T) {
	t.Helper()
	gh := newM6GitHub(t)
	env := bootM6(t, m6Boot{threshold: 1, git: true, gh: gh})
	w := m6Dial(t, env, "m6-9")
	id := m6Start(t, env, "restart", "")
	asg := w.readAssignment(t)
	m6PostText(t, w, asg, m6FailMarker)
	env.waitStatus(t, id, "succeeded")
	w.close()
	m6Tick(t, env.srv.SampleTick)
	if n := m6ChildCount(t, env, id); n != 1 {
		t.Fatalf("judges %d", n)
	}
	env.restart(t)
	m6ExpireLeases(t, env)
	m6Tick(t, env.srv.SampleTick)
	if n := m6ChildCount(t, env, id); n != 1 {
		t.Fatalf("judges after restart %d", n)
	}
	w = m6Dial(t, env, "m6-9b")
	m6FinishPending(t, env, w, nil, false)
	m6WaitScores(t, env, []string{id}, false)
	var skip atomic.Bool
	skip.Store(true)
	env.srv.m6SkipSetBatchPR = func() bool { return skip.CompareAndSwap(true, false) }
	m6RunNow(t, env, http.StatusAccepted)
	m6FinishPending(t, env, w, nil, false)
	w.close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pulls, _, _, _, _ := gh.snapshot()
		row := latestM6Batch(t, env)
		if len(pulls) == 1 && row.status == api.BatchRunning && row.prURL == "" {
			break
		}
		if row.status == api.BatchPROpen {
			t.Fatal("SetBatchPR ran in the crash window")
		}
		time.Sleep(30 * time.Millisecond)
	}
	row := latestM6Batch(t, env)
	pulls, _, _, _, _ := gh.snapshot()
	if len(pulls) != 1 || row.status != api.BatchRunning || row.prURL != "" {
		t.Fatalf("before restart status %s pulls %d", row.status, len(pulls))
	}
	env.restart(t)
	m6ExpireLeases(t, env)
	m6Tick(t, env.srv.ImproveTick)
	opened := m6WaitBatch(t, env, api.BatchPROpen)
	pulls, _, _, _, bad := gh.snapshot()
	if bad < 1 || len(pulls) != 1 || opened.prURL != pulls[0].URL {
		t.Fatalf("lookup pulls %d bad %d url %s", len(pulls), bad, opened.prURL)
	}
}

func testE2EM6OneLeader(t *testing.T) {
	t.Helper()
	env := bootM6(t, m6Boot{threshold: 2})
	w := m6Dial(t, env, "m6-10")
	id := m6Start(t, env, "leader", "")
	asg := w.readAssignment(t)
	m6PostText(t, w, asg, "clean")
	env.waitStatus(t, id, "succeeded")
	w.close()
	peer := openPeer(t, env)
	led, err := env.srv.SampleTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other, err := peer.srv.SampleTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !led || other {
		t.Fatalf("leader %v peer %v", led, other)
	}
	if n := m6ChildCount(t, env, id); n != 1 {
		t.Fatalf("judges %d", n)
	}
}

func m6FailingRuns(t *testing.T, env *env, w *m6Conn, model *m6Model, n int, harness bool) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		id := m6Start(t, env, "fix the gate", "")
		asg := w.readAssignment(t)
		m6AssertWorkSkill(t, asg)
		m6PostText(t, w, asg, m6FailMarker)
		env.waitStatus(t, id, "succeeded")
		ids = append(ids, id)
	}
	m6Tick(t, env.srv.SampleTick)
	m6FinishPending(t, env, w, model, harness)
	m6WaitScores(t, env, ids, false)
	return ids
}

func m6FinishPending(t *testing.T, env *env, w *m6Conn, model *m6Model, harness bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if m6Active(t, env) == 0 {
			return
		}
		asg, ok := w.next(t, 50*time.Millisecond)
		if !ok {
			continue
		}
		m6FinishOne(t, w, asg, model, harness)
	}
	t.Fatalf("pending runs remain (%d)\n%s", m6Active(t, env), env.logs.String())
}

func m6Drain(t *testing.T, env *env, w *m6Conn, model *m6Model, harness bool, done func() bool, overrides map[string]string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if done() && m6Active(t, env) == 0 {
			return
		}
		asg, ok := w.next(t, 50*time.Millisecond)
		if !ok {
			continue
		}
		if overrides != nil {
			if value := m6Override(asg); value != "" {
				overrides[asg.TaskID] = value
			}
		}
		m6FinishOne(t, w, asg, model, harness)
	}
	t.Fatalf("drain timed out\n%s", env.logs.String())
}

func m6FinishOne(t *testing.T, w *m6Conn, asg protocol.TaskAssignment, model *m6Model, harness bool) {
	t.Helper()
	prompt := ""
	title := ""
	if asg.Task != nil {
		prompt = asg.Task.Definition.Prompt
		title = asg.Task.Title
	}
	forbidPlanted(t, "prompt", prompt, "")
	if strings.HasPrefix(title, "score:") {
		if asg.EnvVars["FACTORY_SKILL_TEXT"] != "" || asg.EnvVars["NPM_TOKEN"] != "" || asg.EnvVars["GH_TOKEN"] != "" {
			t.Fatal("judge assignment carried skills or execution secrets")
		}
		if asg.EnvVars["OPENAI_API_KEY"] == "" {
			t.Fatal("judge assignment has no inference credential")
		}
		if harness && model != nil {
			cmd := startM6Harness(t, asg, model.URL)
			waitHarness(t, cmd, asg.EnvVars["FACTORY_RUN_TOKEN"], 0)
			w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: "done"})
			return
		}
		pass := !strings.Contains(prompt, m6FailMarker)
		if strings.Contains(prompt, "CANDIDATE_TASK") {
			pass = true
		} else if strings.Contains(prompt, "BASELINE_TASK") {
			pass = false
		}
		label := "fail"
		if pass {
			label = "pass"
		}
		m6PostText(t, w, asg, `{"label":"`+label+`","reasoning":"recorded by the judge"}`)
		return
	}
	if strings.HasPrefix(title, "improve:") {
		if asg.EnvVars["GH_TOKEN"] != m6Minted || strings.Contains(asg.EnvVars["GH_TOKEN"], plantedGH) {
			t.Fatal("improvement task token was not the scoped installation token")
		}
		m6PostText(t, w, asg, "done")
		return
	}
	if value := m6Override(asg); value != "" {
		marker := "BASELINE_TASK"
		if value == shaN('e') {
			marker = "CANDIDATE_TASK"
		}
		m6PostText(t, w, asg, marker)
		return
	}
	m6PostText(t, w, asg, m6FailMarker)
}

type m6Conn struct {
	env *env
	w   *wsWorker
	ch  chan protocol.TaskAssignment
	err error
}

// m6Dial reads assignments on a side goroutine. A short read deadline on the
// socket itself is permanent, so the drain waits on this channel instead.
func m6Dial(t *testing.T, env *env, id string) *m6Conn {
	t.Helper()
	c := &m6Conn{env: env, w: dialWorker(t, env, id), ch: make(chan protocol.TaskAssignment, 32)}
	go c.pump()
	return c
}

func (c *m6Conn) pump() {
	defer close(c.ch)
	for {
		c.w.mu.Lock()
		conn := c.w.conn
		c.w.mu.Unlock()
		if conn == nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			c.err = err
			return
		}
		var msg protocol.Envelope
		if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != protocol.TypeTaskAssignment {
			continue
		}
		var asg protocol.TaskAssignment
		if err := json.Unmarshal(msg.Data, &asg); err != nil {
			continue
		}
		c.ch <- asg
	}
}

func (c *m6Conn) close() { c.w.close() }

func (c *m6Conn) send(t *testing.T, typ string, payload any) { c.w.send(t, typ, payload) }

func (c *m6Conn) readAssignment(t *testing.T) protocol.TaskAssignment {
	t.Helper()
	asg, ok := c.next(t, 8*time.Second)
	if !ok {
		t.Fatalf("no assignment\n%s", c.env.logs.String())
	}
	return asg
}

func (c *m6Conn) next(t *testing.T, d time.Duration) (protocol.TaskAssignment, bool) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case asg, ok := <-c.ch:
		if !ok {
			t.Fatalf("worker read: %v\n%s", c.err, c.env.logs.String())
		}
		return asg, true
	case <-timer.C:
		return protocol.TaskAssignment{}, false
	}
}

func m6PostText(t *testing.T, w *m6Conn, asg protocol.TaskAssignment, text string) {
	t.Helper()
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"message.delta","role":"assistant","text":` + jsonString(text) + `}`,
	})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: "done"})
}

func m6AssertWorkSkill(t *testing.T, asg protocol.TaskAssignment) {
	t.Helper()
	if !strings.Contains(asg.EnvVars["FACTORY_SKILL_TEXT"], m6SkillMarker) {
		t.Fatal("work assignment is missing skill text")
	}
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.SkillSpec == nil || *asg.Task.AgentConfigSnapshot.SkillSpec != "review" {
		t.Fatalf("skill spec %+v", asg.Task)
	}
}

func m6Override(asg protocol.TaskAssignment) string {
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || len(asg.Task.AgentConfigSnapshot.RepositoryHeadOverrides) == 0 {
		return ""
	}
	return asg.Task.AgentConfigSnapshot.RepositoryHeadOverrides[0].Head.Value
}

func m6Start(t *testing.T, env *env, prompt, repo string) string {
	t.Helper()
	body := `{"factory":"m2","agent":"implementer","prompt":` + jsonString(prompt) + `,"priority":"interactive"`
	if repo != "" {
		body += `,"repo":` + jsonString(repo)
	}
	body += "}"
	code, raw := env.authed(t, http.MethodPost, "/api/v1/agent/run", body)
	if code != http.StatusCreated {
		t.Fatalf("start %d %s", code, raw)
	}
	var run struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" {
		t.Fatalf("start body %s", raw)
	}
	return run.ID
}

func m6StartBenchmark(t *testing.T, env *env, reps int) string {
	t.Helper()
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/benchmarks/pr-review:run", fmt.Sprintf(`{"repetitions":%d}`, reps))
	if code != http.StatusAccepted {
		t.Fatalf("benchmark %d %s", code, raw)
	}
	var accepted api.BenchmarkAccepted
	if err := json.Unmarshal(raw, &accepted); err != nil || accepted.BenchmarkRunID == "" {
		t.Fatalf("benchmark body %s", raw)
	}
	return accepted.BenchmarkRunID
}

func m6Trials(t *testing.T, env *env, id string) api.BenchmarkTrials {
	t.Helper()
	code, raw := env.authed(t, http.MethodGet, "/api/v1/factory/m2/benchmarks/pr-review/trials?benchmark_run_id="+id, "")
	if code != http.StatusOK {
		t.Fatalf("trials %d %s", code, raw)
	}
	var view api.BenchmarkTrials
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func m6AssertBench(t *testing.T, env *env, id string, overrides map[string]string, reps int, winner string) {
	t.Helper()
	view := m6Trials(t, env, id)
	if view.Report.Winner != winner {
		t.Fatalf("winner %q reason %s", view.Report.Winner, view.Report.Reason)
	}
	if len(view.Cells) != 2 {
		t.Fatalf("cells %+v", view.Cells)
	}
	for _, cell := range view.Cells {
		if cell.N != reps || cell.CILow > cell.CIHigh {
			t.Fatalf("cell %+v", cell)
		}
	}
	rows, err := env.raw.Query(context.Background(), `SELECT run_id, configuration, definition_sha FROM benchmark_trials WHERE benchmark_run_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var runID, cfg, sha string
		if err := rows.Scan(&runID, &cfg, &sha); err != nil {
			t.Fatal(err)
		}
		n++
		wantSHA := shaN('a')
		wantHead := shaN('d')
		if cfg == "Candidate" {
			wantSHA = shaN('b')
			wantHead = shaN('e')
		}
		if sha != wantSHA {
			t.Fatalf("%s sha %s", cfg, sha)
		}
		if overrides[runID] != wantHead {
			t.Fatalf("%s override %s", cfg, overrides[runID])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 2*reps {
		t.Fatalf("trials %d", n)
	}
}

func m6BenchDone(t *testing.T, env *env, id string) bool {
	t.Helper()
	var pending int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM benchmark_trials WHERE benchmark_run_id = $1 AND outcome = 'pending'`, id).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	return pending == 0
}

type m6Batch struct {
	id     string
	status string
	prURL  string
	runID  string
	note   string
	number int
}

func latestM6Batch(t *testing.T, env *env) m6Batch {
	t.Helper()
	var row m6Batch
	err := env.raw.QueryRow(context.Background(), `
		SELECT id, status, pr_url, COALESCE(improvement_run_id, ''), rejection_note, COALESCE(pr_number, 0)
		FROM improvement_batches
		ORDER BY created_at DESC
		LIMIT 1`).Scan(&row.id, &row.status, &row.prURL, &row.runID, &row.note, &row.number)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func m6WaitBatch(t *testing.T, env *env, status string) m6Batch {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var row m6Batch
	for time.Now().Before(deadline) {
		row = latestM6Batch(t, env)
		if row.status == status {
			return row
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("batch %s, want %s (%s)", row.status, status, row.note)
	return row
}

func m6RunNow(t *testing.T, env *env, want int) {
	t.Helper()
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/improvement:run-now", "{}")
	if code != want {
		t.Fatalf("run-now %d %s", code, raw)
	}
}

func m6Tick(t *testing.T, tick func(context.Context) (bool, error)) {
	t.Helper()
	ok, err := tick(context.Background())
	if err != nil || !ok {
		t.Fatalf("tick ok=%v err=%v", ok, err)
	}
}

func m6WaitScores(t *testing.T, env *env, ids []string, passed bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		ready := true
		for _, id := range ids {
			var n int
			err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM scorer_results WHERE run_id = $1 AND status = 'ok' AND passed = $2`, id, passed).Scan(&n)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				ready = false
			}
		}
		if ready {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("scores were not recorded")
}

func m6Active(t *testing.T, env *env) int {
	t.Helper()
	var n int
	err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE status IN ('queued', 'assigned', 'running')`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func m6ResultCount(t *testing.T, env *env, runID string) int {
	t.Helper()
	var n int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM scorer_results WHERE run_id = $1`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func m6ChildCount(t *testing.T, env *env, runID string) int {
	t.Helper()
	var n int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE parent_run_id = $1 AND purpose = 'scorer'`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func m6ForceTerminal(t *testing.T, env *env, ids []string, status, class string) {
	t.Helper()
	for _, id := range ids {
		if _, err := env.raw.Exec(context.Background(), `UPDATE runs SET status = $2, failure_class = NULLIF($3, '') WHERE id = $1`, id, status, class); err != nil {
			t.Fatal(err)
		}
		if _, err := env.raw.Exec(context.Background(), `DELETE FROM run_queue WHERE run_id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
}

func m6ExpireLeases(t *testing.T, env *env) {
	t.Helper()
	if _, err := env.raw.Exec(context.Background(), `UPDATE leader_leases SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
