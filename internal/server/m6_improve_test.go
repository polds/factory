package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/store"
)

const (
	t81AgentMarker = "T81_AGENT_INSTRUCTIONS"
	t81SecretURL   = "https://t81-secret-definition-url.example/acme/definitions.git"
	t81BaseRef     = "m6-run/t81-base"
	t81Reasoning   = "the skill omitted the required convention"
)

func TestImprovementPromptCarriesInstructions(t *testing.T) {
	skipNoDB(t)
	env := bootM6(t, m6Boot{
		threshold:     1,
		git:           true,
		agentBody:     t81AgentMarker + "\n",
		gitURL:        t81SecretURL,
		productionRef: t81BaseRef,
		secrets:       map[string]string{"DEFINITION_GIT_URL": t81SecretURL},
	})

	_, batch := m6SeedImprovement(t, env, t81Reasoning)
	if batch.ImprovementRunID == "" {
		t.Fatal("improvement run was not started")
	}
	if len(batch.Failures) != 1 || batch.Failures[0].Reasoning != t81Reasoning || batch.Failures[0].At.IsZero() {
		t.Fatalf("batch failure reasoning %q at zero %v", failureReasoning(batch), failureAtZero(batch))
	}
	run, err := env.srv.store.GetRun(context.Background(), batch.ImprovementRunID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := run.Prompt
	m6AssertNoSecret(t, prompt)
	branch := improveBranchPrefix + batch.ID
	when := batch.Failures[0].At.UTC().Format(time.RFC3339)
	for _, want := range []string{
		t81AgentMarker,
		"# Instructions",
		"Agent: implementer",
		"DEFINITION_GIT_URL",
		"branch: " + branch,
		"Push the branch " + branch,
		"Base ref: " + t81BaseRef,
		"GH_TOKEN",
		"reasoning: " + t81Reasoning,
		when,
		"/workspace starts empty.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestImprovementFailureStopsRetry(t *testing.T) {
	skipNoDB(t)
	t.Run("failed run", func(t *testing.T) {
		cloner := &scriptedCloner{}
		env := bootM6(t, m6Boot{threshold: 1, git: true, cloner: cloner})
		_, batch := m6SeedImprovement(t, env, t81Reasoning)
		m6ForceTerminal(t, env, []string{batch.ImprovementRunID}, failure.StatusFailed, "agent")
		if _, err := env.raw.Exec(context.Background(), `UPDATE runs SET failure_reason = $2 WHERE id = $1`, batch.ImprovementRunID, "workspace was empty"); err != nil {
			t.Fatal(err)
		}
		run, err := env.srv.store.GetRun(context.Background(), batch.ImprovementRunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.srv.settleImprovementRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		got := m6LoadedBatch(t, env, batch.ID)
		if got.Status != api.BatchFailed {
			t.Fatalf("status %s", got.Status)
		}
		if !strings.Contains(got.RejectionNote, "improvement run failed") || !strings.Contains(got.RejectionNote, "workspace was empty") {
			t.Fatalf("note %q", got.RejectionNote)
		}
		if n := cloner.improveCount(); n != 0 {
			t.Fatalf("improve clones %d", n)
		}
		if err := env.srv.settleImprovementRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if err := env.srv.sweepImprovementBatches(context.Background(), batch.FactoryID); err != nil {
			t.Fatal(err)
		}
		if n := cloner.improveCount(); n != 0 {
			t.Fatalf("improve clones after retry %d", n)
		}
		m6AssertNextBatch(t, env, batch.ID)
	})

	t.Run("missing branch", func(t *testing.T) {
		cloner := &scriptedCloner{missing: true}
		env := bootM6(t, m6Boot{threshold: 1, git: true, cloner: cloner})
		_, batch := m6SeedImprovement(t, env, t81Reasoning)
		m6ForceTerminal(t, env, []string{batch.ImprovementRunID}, failure.StatusSucceeded, "")
		run, err := env.srv.store.GetRun(context.Background(), batch.ImprovementRunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.srv.settleImprovementRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		got := m6LoadedBatch(t, env, batch.ID)
		if got.Status != api.BatchFailed {
			t.Fatalf("status %s", got.Status)
		}
		branch := improveBranchPrefix + batch.ID
		if !strings.Contains(got.RejectionNote, "improvement branch missing") || !strings.Contains(got.RejectionNote, branch) {
			t.Fatalf("note %q", got.RejectionNote)
		}
		if n := cloner.improveCount(); n != 1 {
			t.Fatalf("improve clones %d", n)
		}
		if err := env.srv.settleImprovementRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if err := env.srv.sweepImprovementBatches(context.Background(), batch.FactoryID); err != nil {
			t.Fatal(err)
		}
		if n := cloner.improveCount(); n != 1 {
			t.Fatalf("improve clones after retry %d", n)
		}
		m6AssertNextBatch(t, env, batch.ID)
	})
}

func TestImprovementOpenedPRKeepsRegressions(t *testing.T) {
	skipNoDB(t)
	cloner := &scriptedCloner{}
	gh := newM6GitHub(t)
	env := bootM6(t, m6Boot{
		threshold: 1,
		git:       true,
		cloner:    cloner,
		gh:        gh,
		secrets:   map[string]string{"DEFINITION_GIT_URL": t81SecretURL},
	})
	_, batch := m6SeedImprovement(t, env, t81Reasoning)
	gh.mu.Lock()
	gh.existingPR = true
	gh.mu.Unlock()
	m6ForceTerminal(t, env, []string{batch.ImprovementRunID}, failure.StatusSucceeded, "")
	run, err := env.srv.store.GetRun(context.Background(), batch.ImprovementRunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.settleImprovementRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	got := m6LoadedBatch(t, env, batch.ID)
	if got.Status != api.BatchPROpen || got.PRNumber != 41 {
		t.Fatalf("status %s pr %d note %q", got.Status, got.PRNumber, got.RejectionNote)
	}
	gh.mu.Lock()
	patched := append([]string(nil), gh.patched...)
	gh.mu.Unlock()
	if len(patched) != 1 || !strings.Contains(patched[0], "## Regressions addressed") {
		t.Fatalf("patched bodies %d", len(patched))
	}
	m6AssertNoSecret(t, patched[0])
}

type scriptedCloner struct {
	mu      sync.Mutex
	improve int
	missing bool
	body    string
}

func (c *scriptedCloner) Clone(_ context.Context, _, ref, dest string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.HasPrefix(ref, improveBranchPrefix) {
		c.improve++
		if c.missing {
			return "", fmt.Errorf("fatal: couldn't find remote ref %s", ref)
		}
		if err := writeM6Tree(dest, m6TreeSkillHead, 1, c.body); err != nil {
			return "", err
		}
		return shaN('c'), nil
	}
	if err := writeM6Tree(dest, m6TreeProd, 1, c.body); err != nil {
		return "", err
	}
	return shaN('a'), nil
}

func (c *scriptedCloner) improveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.improve
}

func m6SeedImprovement(t *testing.T, env *env, reasoning string) (string, store.ImprovementBatch) {
	t.Helper()
	ctx := context.Background()
	fac, agent := env.factoryAgent(t)
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
		Reasoning:  reasoning,
		Status:     api.ScorerStatusOK,
	}); err != nil {
		t.Fatal(err)
	}
	now := m6RunNowBody(t, env)
	if len(now.Batches) != 1 {
		t.Fatalf("batches %d", len(now.Batches))
	}
	return work.ID, m6LoadedBatch(t, env, now.Batches[0].ID)
}

func m6LoadedBatch(t *testing.T, env *env, id string) store.ImprovementBatch {
	t.Helper()
	fac, _ := env.factoryAgent(t)
	rows, err := env.srv.store.ImprovementBatches(context.Background(), fac)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("batch %s missing", id)
	return store.ImprovementBatch{}
}

func m6AssertNextBatch(t *testing.T, env *env, previous string) {
	t.Helper()
	now := m6RunNowBody(t, env)
	if len(now.Batches) != 1 || now.Batches[0].ID == previous {
		t.Fatalf("next batches %d", len(now.Batches))
	}
	if now.Batches[0].Status != api.BatchRunning && now.Batches[0].Status != api.BatchQueued {
		t.Fatalf("next status %s", now.Batches[0].Status)
	}
}

func m6AssertNoSecret(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{t81SecretURL, "t81-secret-definition-url", plantedGH} {
		if strings.Contains(text, secret) {
			t.Fatal("text contains a secret value")
		}
	}
}

func failureReasoning(batch store.ImprovementBatch) string {
	if len(batch.Failures) == 0 {
		return ""
	}
	return batch.Failures[0].Reasoning
}

func failureAtZero(batch store.ImprovementBatch) bool {
	return len(batch.Failures) == 0 || batch.Failures[0].At.IsZero()
}
