package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

func TestM6RefResolvesPerBenchmarkRun(t *testing.T) {
	skipNoDB(t)
	const skillV1 = "SKILL_V1_ONLY"
	const skillV2 = "SKILL_V2_ONLY"
	mover := &refMover{
		prodSHA:   shaN('a'),
		prodSkill: "SKILL_PROD_ONLY",
		candSHA:   shaN('b'),
		candSkill: skillV1,
		threshold: 2,
	}
	env := bootM6(t, m6Boot{threshold: 2, git: true, slots: 4, cloner: mover})
	w := m6Dial(t, env, "m6-ref")
	defer w.close()

	first := m6StartBenchmark(t, env, 1)
	if got := m6CandidateSHA(t, env, first); got != shaN('b') {
		t.Fatalf("first candidate sha %s", got)
	}
	m6DrainCandidateSkill(t, env, w, skillV1, "")

	mover.setCandidate(shaN('f'), skillV2)
	second := m6StartBenchmark(t, env, 1)
	if got := m6CandidateSHA(t, env, second); got != shaN('f') {
		t.Fatalf("second candidate sha %s", got)
	}
	m6DrainCandidateSkill(t, env, w, skillV2, skillV1)
}

// refMover serves production and candidate refs. A 40-hex ref returns the
// skill recorded for that commit. A ref name returns whatever the ref
// points at now, so a later push is visible to the next benchmark run.
type refMover struct {
	mu        sync.Mutex
	prodSHA   string
	prodSkill string
	candSHA   string
	candSkill string
	threshold int
	bySHA     map[string]string
}

func (c *refMover) setCandidate(sha, skill string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.candSHA = sha
	c.candSkill = skill
}

func (c *refMover) Clone(_ context.Context, _, ref, dest string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sha := c.prodSHA
	skill := c.prodSkill
	if ref == "candidate" {
		sha, skill = c.candSHA, c.candSkill
	} else if ref != "main" && ref != c.prodSHA {
		remembered, ok := gitSHA(ref)
		if !ok {
			return "", fmt.Errorf("unexpected ref %s", ref)
		}
		sha = remembered
		skill = c.bySHA[remembered]
	}
	if skill == "" || sha == "" {
		return "", fmt.Errorf("missing tree for %s", ref)
	}
	kind := m6TreeProd
	if sha != c.prodSHA {
		kind = m6TreeCandidate
	}
	if err := writeM6Tree(dest, kind, c.threshold); err != nil {
		return "", err
	}
	path := filepath.Join(dest, "agents", "implementer", "skills", "review", "SKILL.md")
	if err := os.WriteFile(path, []byte(skill+"\n"), 0o644); err != nil {
		return "", err
	}
	if c.bySHA == nil {
		c.bySHA = map[string]string{}
	}
	c.bySHA[sha] = skill
	return sha, nil
}

func m6CandidateSHA(t *testing.T, env *env, benchID string) string {
	t.Helper()
	var sha string
	err := env.raw.QueryRow(context.Background(), `
		SELECT definition_sha FROM benchmark_trials
		WHERE benchmark_run_id = $1 AND configuration = 'Candidate'`, benchID).Scan(&sha)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func m6DrainCandidateSkill(t *testing.T, env *env, w *m6Conn, want, absent string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	saw := false
	for time.Now().Before(deadline) {
		if saw && m6Active(t, env) == 0 {
			return
		}
		asg, ok := w.next(t, 50*time.Millisecond)
		if !ok {
			continue
		}
		title := ""
		if asg.Task != nil {
			title = asg.Task.Title
		}
		if !strings.HasPrefix(title, "score:") && m6Override(asg) == shaN('e') {
			text := asg.EnvVars["FACTORY_SKILL_TEXT"]
			if !strings.Contains(text, want) {
				t.Fatalf("candidate skill %q, want %s", text, want)
			}
			if absent != "" && strings.Contains(text, absent) {
				t.Fatalf("candidate skill still contains %s: %s", absent, text)
			}
			saw = true
		}
		m6FinishOne(t, w, asg, nil, false)
	}
	t.Fatalf("did not see candidate skill %s\n%s", want, env.logs.String())
}

func TestCloneRefRedactsGitURLUserinfo(t *testing.T) {
	const secretURL = "https://user:secret@host/repo"
	var logs bytes.Buffer
	srv := &Server{
		log: newTestLogger(&logs),
		cfg: Config{
			DefinitionGitURL:    secretURL,
			DefinitionGitCloner: failClone{err: fmt.Errorf("fatal: unable to access '%s/'", secretURL)},
		},
	}
	_, _, err := srv.cloneRef(context.Background(), "candidate")
	if err == nil {
		t.Fatal("expected clone error")
	}
	blob := err.Error() + "\n" + logs.String()
	if strings.Contains(blob, "secret") || strings.Contains(blob, "user:secret") {
		t.Fatalf("git url userinfo leaked: %s", blob)
	}
	if !strings.Contains(err.Error(), "host/repo") {
		t.Fatalf("redacted error lost the host: %s", err)
	}
}

type failClone struct{ err error }

func (f failClone) Clone(context.Context, string, string, string) (string, error) {
	return "", f.err
}

func TestM6BenchmarkDoesNotFeedImprovement(t *testing.T) {
	skipNoDB(t)
	env := bootM6(t, m6Boot{threshold: 1, git: true, slots: 2})
	ctx := context.Background()
	benchID := m6StartBenchmark(t, env, 1)
	rows, err := env.raw.Query(ctx, `
		SELECT run_id FROM benchmark_trials
		WHERE benchmark_run_id = $1
		ORDER BY configuration ASC`, benchID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("trials %d", len(ids))
	}
	cancelledID, succeededID := ids[0], ids[1]
	m6ForceTerminal(t, env, []string{cancelledID}, "cancelled", "")
	m6ForceTerminal(t, env, []string{succeededID}, "succeeded", "")

	cancelled, err := env.srv.store.GetRun(ctx, cancelledID)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.scoreBenchmarkTrial(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := env.raw.QueryRow(ctx, `SELECT outcome FROM benchmark_trials WHERE run_id = $1`, cancelledID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != api.TrialErrored {
		t.Fatalf("cancelled outcome %s", outcome)
	}
	if n := m6ChildCount(t, env, cancelledID); n != 0 {
		t.Fatalf("cancelled children %d", n)
	}
	if n := m6ResultCount(t, env, cancelledID); n != 0 {
		t.Fatalf("cancelled results %d", n)
	}

	succeeded, err := env.srv.store.GetRun(ctx, succeededID)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.scoreBenchmarkTrial(ctx, succeeded); err != nil {
		t.Fatal(err)
	}
	titles := m6ChildTitles(t, env, succeededID)
	if len(titles) != 1 || titles[0] != "score:correctness" {
		t.Fatalf("succeeded judges %v", titles)
	}

	fac, agent := env.factoryAgent(t)
	if _, err := env.srv.store.UpsertScorerResult(ctx, store.ScorerResult{
		FactoryID:  fac,
		RunID:      succeededID,
		ScorerName: "review",
		Label:      "fail",
		Passed:     false,
		Reasoning:  "benchmark fail",
		Status:     api.ScorerStatusOK,
	}); err != nil {
		t.Fatal(err)
	}
	first := m6RunNowBody(t, env)
	if len(first.Batches) != 0 || len(first.CheckedAgents) != 0 {
		t.Fatalf("benchmark failure entered improvement: %+v", first)
	}

	work, err := env.srv.store.CreateRun(ctx, store.NewRun{
		FactoryID: fac,
		AgentID:   agent,
		Pool:      "standard",
		Priority:  store.PriorityBatch,
		Prompt:    "work",
		Title:     "work",
		Purpose:   api.PurposeWork,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.store.UpsertScorerResult(ctx, store.ScorerResult{
		FactoryID:  fac,
		RunID:      work.ID,
		ScorerName: "review",
		Label:      "fail",
		Passed:     false,
		Reasoning:  "work fail",
		Status:     api.ScorerStatusOK,
	}); err != nil {
		t.Fatal(err)
	}
	second := m6RunNowBody(t, env)
	if len(second.Batches) != 1 {
		t.Fatalf("batches %+v", second)
	}
	failures := second.Batches[0].Failures
	if len(failures) != 1 || failures[0].RunID != work.ID {
		t.Fatalf("batch failures %+v", failures)
	}
}

func m6ChildTitles(t *testing.T, env *env, runID string) []string {
	t.Helper()
	rows, err := env.raw.Query(context.Background(), `
		SELECT title FROM runs
		WHERE parent_run_id = $1 AND purpose = 'scorer'
		ORDER BY title ASC`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var titles []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return titles
}

func m6RunNowBody(t *testing.T, env *env) api.ImprovementRunNow {
	t.Helper()
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/improvement:run-now", "{}")
	if code != http.StatusAccepted {
		t.Fatalf("run-now %d %s", code, raw)
	}
	var body api.ImprovementRunNow
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("run-now body %s", raw)
	}
	return body
}

func TestImprovementLayer(t *testing.T) {
	base := t.TempDir()
	head := t.TempDir()
	if got := improvementLayer(base, head); got != "definition" {
		t.Fatalf("empty diff layer %s", got)
	}
	skill := "agents/implementer/skills/review/SKILL.md"
	writeRel(t, base, skill, "old")
	writeRel(t, head, skill, "new")
	if got := improvementLayer(base, head); got != "skill" {
		t.Fatalf("skills-only layer %s", got)
	}
	writeRel(t, base, "agents/other/skills/edit/SKILL.md", "same")
	writeRel(t, head, "agents/other/skills/edit/SKILL.md", "same")
	if got := improvementLayer(base, head); got != "skill" {
		t.Fatalf("unchanged extra skill layer %s", got)
	}
	writeRel(t, head, "notes/a.md", "changed")
	if got := improvementLayer(base, head); got != "definition" {
		t.Fatalf("mixed diff layer %s", got)
	}
	only := t.TempDir()
	next := t.TempDir()
	writeRel(t, only, "factory.yaml", "old")
	writeRel(t, next, "factory.yaml", "new")
	if got := improvementLayer(only, next); got != "definition" {
		t.Fatalf("factory.yaml layer %s", got)
	}
	named := t.TempDir()
	writeRel(t, named, "skills-extra/note.md", "no")
	other := t.TempDir()
	writeRel(t, other, "skills-extra/note.md", "yes")
	if got := improvementLayer(named, other); got != "definition" {
		t.Fatalf("skills prefix layer %s", got)
	}
}

func TestSamplerRenewsLease(t *testing.T) {
	skipNoDB(t)
	env := bootM6(t, m6Boot{threshold: 2})
	m6Tick(t, env.srv.SampleTick)
	ctx := context.Background()
	if _, err := env.raw.Exec(ctx, `
		UPDATE leader_leases
		SET expires_at = now() + interval '1 second'
		WHERE name = 'm6-sampler'`); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := env.raw.QueryRow(ctx, `SELECT expires_at FROM leader_leases WHERE name = 'm6-sampler'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.samplePass(ctx); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := env.raw.QueryRow(ctx, `SELECT expires_at FROM leader_leases WHERE name = 'm6-sampler'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(before.Add(10 * time.Second)) {
		t.Fatalf("lease expires %s, was %s", after, before)
	}
}

func writeRel(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
