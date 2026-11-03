package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/polds/factory/internal/protocol"
)

func TestE2EDirectPromptAndNoWorkProduct(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{definition: writeDirectDefinition})
	w := dialWorker(t, env, "local-1")
	defer w.close()

	const task = "Add a line to README.md and open a pull request against the run base ref."
	secretTask := task + " " + plantedGH + " " + plantedOpenAI

	runID := env.startNamed(t, "m7", "implementer", secretTask)
	asg := w.readAssignment(t)
	prompt := assignmentPrompt(t, asg)
	assertDirectPrompt(t, prompt, "check out m7-run/direct/main", task)
	var stored string
	if err := env.raw.QueryRow(context.Background(), `SELECT prompt FROM runs WHERE id = $1`, runID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != prompt {
		t.Fatal("stored prompt differs from the assignment")
	}
	if strings.Contains(stored, plantedGH) || strings.Contains(stored, plantedOpenAI) || strings.Contains(stored, plantedNPM) {
		t.Fatal("stored prompt contains a secret")
	}
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: runID, WorkerID: "local-1"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: runID, ExecutionID: asg.ExecutionID, Message: "done"})
	env.waitStatus(t, runID, "failed")
	detail := env.runDetail(t, runID)
	if detail.FailureReason != "no_work_product" {
		t.Fatalf("failure_reason %q", detail.FailureReason)
	}
	if detail.Failure.Class != "agent" || detail.Failure.Code != "no_work_product" {
		t.Fatalf("class %s code %s", detail.Failure.Class, detail.Failure.Code)
	}
	env.requireOneTerminal(t, runID, "failed")
	reason, summary := env.terminalFields(t, runID)
	if reason != "no_work_product" {
		t.Fatalf("terminal failure_reason %q", reason)
	}
	if summary != "agent finished without opening a pull request" {
		t.Fatalf("terminal summary %q", summary)
	}

	withPR := env.startNamed(t, "m7", "implementer", task)
	prAsg := w.readAssignment(t)
	postEvents(t, prAsg.EnvVars["FACTORY_EVENTS_URL"], prAsg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"tool.result","prUrls":["https://pr.invalid/m7/factory-m7-target/pull/1"]}`,
	})
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: withPR, WorkerID: "local-1"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: withPR, ExecutionID: prAsg.ExecutionID, Message: "done"})
	env.waitStatus(t, withPR, "succeeded")
	if reason := env.runDetail(t, withPR).FailureReason; reason != "" {
		t.Fatalf("pull request run failure_reason %q", reason)
	}
	env.requireOneTerminal(t, withPR, "succeeded")
	openedReason, _ := env.terminalFields(t, withPR)
	if openedReason != "" {
		t.Fatalf("pull request run failure_reason %q", openedReason)
	}
	if !strings.Contains(env.terminalPayload(t, withPR), "https://pr.invalid/m7/factory-m7-target/pull/1") {
		t.Fatal("pull request URL was not recorded")
	}

	plain := env.startNamed(t, "m7", "plain", task)
	plainAsg := w.readAssignment(t)
	plainPrompt := assignmentPrompt(t, plainAsg)
	if !strings.Contains(plainPrompt, "# Instructions\n") || !strings.Contains(plainPrompt, "Do the task.") {
		t.Fatal("plain agent instructions were not delivered")
	}
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: plain, WorkerID: "local-1"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: plain, ExecutionID: plainAsg.ExecutionID, Message: "done"})
	env.waitStatus(t, plain, "succeeded")
	if reason := env.runDetail(t, plain).FailureReason; reason != "" {
		t.Fatalf("plain agent failure_reason %q", reason)
	}
	env.requireOneTerminal(t, plain, "succeeded")
}

func writeDirectDefinition(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, root+"/factory.yaml", `
schemaVersion: v1alpha1
name: m7
secrets:
  - GH_TOKEN
agentDefaults:
  model: auto
  runner: local
  pool: standard
`)
	mustWrite(t, root+"/runners/local.yaml", `
description: Local docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-m1-task:dev
`)
	mustWrite(t, root+"/agents/implementer/agent.md", `
---
description: Opens one pull request.
agentType: IMPLEMENT
model: test-model
secrets:
  - NPM_TOKEN
maxTurns: 8
requires:
  - pullRequest
---

check out m7-run/direct/main.
The token is `+plantedGH+` and must not be repeated.
`)
	mustWrite(t, root+"/agents/plain/agent.md", `
---
description: Does the task without a pull request.
agentType: IMPLEMENT
model: test-model
---

Do the task.
`)
	return root
}

func (e *env) startNamed(t *testing.T, factory, agent, prompt string) string {
	t.Helper()
	body := `{"factory":` + jsonString(factory) + `,"agent":` + jsonString(agent) + `,"prompt":` + jsonString(prompt) + `,"priority":"interactive"}`
	req, err := http.NewRequest(http.MethodPost, e.base+"/api/v1/agent/run", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("start %d %s", res.StatusCode, raw)
	}
	var run struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" {
		t.Fatalf("start body %s", raw)
	}
	return run.ID
}

func assignmentPrompt(t *testing.T, asg protocol.TaskAssignment) string {
	t.Helper()
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.BasePrompt == nil {
		t.Fatal("assignment prompt missing")
	}
	envPrompt := asg.EnvVars["FACTORY_TASK_PROMPT"]
	defPrompt := asg.Task.Definition.Prompt
	base := *asg.Task.AgentConfigSnapshot.BasePrompt
	if envPrompt == "" || envPrompt != defPrompt || envPrompt != base {
		t.Fatal("assignment prompts differ")
	}
	return envPrompt
}

func assertDirectPrompt(t *testing.T, prompt, instruction, task string) {
	t.Helper()
	if !strings.Contains(prompt, "# Instructions\n") || !strings.Contains(prompt, instruction) {
		t.Fatal("assignment missed the agent instructions")
	}
	if !strings.Contains(prompt, "# Task\n") || !strings.Contains(prompt, task) {
		t.Fatal("assignment missed the run prompt")
	}
	for _, secret := range []string{plantedGH, plantedNPM, plantedOpenAI} {
		if strings.Contains(prompt, secret) {
			t.Fatal("assignment prompt contains a secret")
		}
	}
	if !strings.Contains(prompt, "[REDACTED]") {
		t.Fatal("assignment prompt did not redact the secret")
	}
}

func (e *env) terminalPayload(t *testing.T, runID string) string {
	t.Helper()
	var payload string
	err := e.raw.QueryRow(context.Background(), `SELECT payload_json::text FROM run_events WHERE run_id = $1 AND type = 'run.terminal' ORDER BY seq`, runID).Scan(&payload)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func (e *env) terminalFields(t *testing.T, runID string) (reason, summary string) {
	t.Helper()
	var body struct {
		FailureReason string `json:"failure_reason"`
		Summary       string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(e.terminalPayload(t, runID)), &body); err != nil {
		t.Fatal(err)
	}
	return body.FailureReason, body.Summary
}
