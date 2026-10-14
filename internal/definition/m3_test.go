package definition

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadDirM2Factory(t *testing.T) {
	tree, diags := LoadDir("../../deploy/m2-factory")
	if len(diags) != 0 {
		t.Fatalf("m2-factory diagnostics:\n%s", formatDiags(diags))
	}
	if tree.Factory.Pipeline != nil {
		t.Fatalf("m2 pipeline = %+v", tree.Factory.Pipeline)
	}
	if tree.Agents["implementer"].Model != "m2-offline-stub" {
		t.Fatalf("implementer = %+v", tree.Agents["implementer"])
	}
}

func TestPipelineMissingAgent(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
pipeline:
  stages: [building]
  agents:
    building: ghost
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, `agent "ghost" not found`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestPipelineStageWithoutAgent(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
pipeline:
  stages: [building, reviewing]
  agents:
    building: impl
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, `stage "reviewing" has no agent`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestPipelineUnknownKey(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
pipeline:
  surprise: true
  stages: [building]
  agents:
    building: impl
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, `unknown key "surprise"`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if diagHas(diags, "not applied in M2") {
		t.Fatalf("pipeline still warned:\n%s", formatDiags(diags))
	}
}

func TestApprovalRequiresApprovers(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
pipeline:
  stages: [building]
  agents:
    building: impl
  approvals:
    spec: { required: true }
    merge: { required: false }
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, `approval "spec" requires approvers`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if diagHas(diags, `approval "merge"`) {
		t.Fatalf("optional gate rejected:\n%s", formatDiags(diags))
	}
}

func TestTwoForemen(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "alpha", "agent.md"), agentMD("FOREMAN", "model: auto\n"))
	writeFile(t, filepath.Join(dir, "agents", "beta", "agent.md"), agentMD("FOREMAN", "model: auto\n"))
	_, diags := LoadDir(dir)
	n := 0
	for _, d := range diags {
		if d.Severity == SeverityError && strings.Contains(d.Message, "agentType FOREMAN") {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestRouterTargetAuto(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "routers", "by-task.yaml"), `
name: By task
type: prompt
default: auto
routing:
  - description: cheap
    model: claude-haiku
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, `router target "auto" is not a concrete model`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if diagHas(diags, "not applied in M2") {
		t.Fatalf("router still warned:\n%s", formatDiags(diags))
	}
}

func TestRouterTargetsRouter(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "routers", "cheap.yaml"), `
type: prompt
default: claude-haiku
`)
	writeFile(t, filepath.Join(dir, "routers", "by-task.yaml"), `
type: complexity
default: by-task
routing:
  easy: claude-haiku
  medium: cheap
  hard: claude-opus
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, `router target "cheap" is not a concrete model`) || !diagHas(diags, `router target "by-task" is not a concrete model`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestRoutersAndSkillsOnTree(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "routers", "by-task.yaml"), `
name: By task
type: prompt
default: claude-sonnet
routing:
  - description: cheap
    model: claude-haiku
`)
	writeFile(t, filepath.Join(dir, "skills", "repository-conventions", "SKILL.md"), "---\nname: repository-conventions\ndescription: How to change the repo.\n---\n\nRun pnpm lint.\n")
	writeFile(t, filepath.Join(dir, "agents", "impl", "skills", "local-only", "SKILL.md"), "---\ndescription: Agent local.\n---\n\nLocal steps.\n")
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	rt, ok := tree.Routers["by-task"]
	if !ok || rt.Type != "prompt" || rt.Default != "claude-sonnet" || len(rt.Routes) != 1 || rt.Routes[0].Model != "claude-haiku" {
		t.Fatalf("router = %+v ok=%v", rt, ok)
	}
	sk, ok := tree.Skills["repository-conventions"]
	if !ok || !strings.Contains(sk.Body, "pnpm lint") || sk.Description == "" {
		t.Fatalf("skill = %+v ok=%v", sk, ok)
	}
	agentSkills := tree.AgentSkills["impl"]
	if len(agentSkills) != 1 || agentSkills[0].Name != "local-only" || !strings.Contains(agentSkills[0].Body, "Local steps") {
		t.Fatalf("agent skills = %#v", agentSkills)
	}
}

func TestSkillMissingFile(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "skills", "broken", "README.md"), "no skill here\n")
	_, diags := LoadDir(dir)
	if !diagHas(diags, "missing SKILL.md") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestDigestChangesWhenSkillChanges(t *testing.T) {
	dir := baseTree(t)
	path := filepath.Join(dir, "skills", "repository-conventions", "SKILL.md")
	writeFile(t, path, "---\ndescription: first\n---\n\nOne.\n")
	first, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, []byte("Two.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	second, _ := LoadDir(dir)
	if first.Digest == "" || first.Digest == second.Digest {
		t.Fatalf("digest unchanged %q", first.Digest)
	}
}

func TestSameModelWarning(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "reviewer", "agent.md"), agentMD("REVIEW", "model: claude-x\n"))
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: claude-x\n"))
	_, diags := LoadDir(dir)
	var warns, errs int
	for _, d := range diags {
		if d.Severity == SeverityError {
			errs++
		}
		if d.Severity == SeverityWarning && strings.Contains(d.Message, "same model") && strings.Contains(d.Message, "reviewer") && strings.Contains(d.Message, "impl") {
			warns++
		}
	}
	if errs != 0 || warns != 1 {
		t.Fatalf("errs=%d warns=%d\n%s", errs, warns, formatDiags(diags))
	}
}

func TestSameModelAutoAndWarpHarness(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: auto\n"))
	writeFile(t, filepath.Join(dir, "agents", "reviewer", "agent.md"), agentMD("REVIEW", "harness:\n  type: warp-agent\n  model: auto\n"))
	_, diags := LoadDir(dir)
	if !diagHas(diags, "same model") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestScorerAgentObjectForm(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "foreman", "agent.md"), agentMD("FOREMAN", "model: auto\n"))
	writeFile(t, filepath.Join(dir, "scorers", "scope", "scorer.md"), `---
description: scope
agents:
  - name: foreman
    includeDescendants: true
  - impl
output: classification
labels:
  - { value: ok, description: ok, score: 1 }
  - { value: no, description: no, score: 0 }
passingScore: 1
---

Judge.
`)
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	agents := tree.Scorers["scope"].Agents
	if len(agents) != 2 || agents[0].Name != "foreman" || !agents[0].IncludeDescendants || agents[1].Name != "impl" || agents[1].IncludeDescendants {
		t.Fatalf("agents = %#v", agents)
	}
}

func TestScorerPassingScore(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "scorers", "always", "scorer.md"), `---
description: cannot fail
agents:
  - impl
output: classification
labels:
  - { value: ok, description: ok, score: 1 }
passingScore: 1
---

Judge.
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, "passingScore") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestAutomationRequiredReposExtend(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", `model: auto
required_repos:
  - { codeForge: GITHUB, owner: acme, name: payments-service }
`))
	writeFile(t, filepath.Join(dir, "automations", "intake", "automation.md"), `---
enabled: true
agent: impl
model: claude-x
required_repos:
  - { codeForge: GITHUB, owner: acme, name: payments-docs }
---

Take it.
`)
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	got := tree.Automations["intake"].RequiredRepos
	want := []Repository{
		{CodeForge: "GITHUB", Owner: "acme", Name: "payments-service"},
		{CodeForge: "GITHUB", Owner: "acme", Name: "payments-docs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repos = %#v", got)
	}
	if tree.Automations["intake"].Model != "claude-x" {
		t.Fatalf("model = %+v", tree.Automations["intake"])
	}
}

func TestWebhookSignatureScheme(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "webhooks", "ci.yaml"), `
authMode: signature
secretName: CI_WEBHOOK_SECRET
enabled: true
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, "signatureScheme is required") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}

	dir = baseTree(t)
	writeFile(t, filepath.Join(dir, "webhooks", "ci.yaml"), `
authMode: token
signatureScheme: github
secretName: CI_WEBHOOK_SECRET
enabled: true
`)
	_, diags = LoadDir(dir)
	if !diagHas(diags, "signatureScheme is only valid") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestAutomationUnknownAgent(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "automations", "intake", "automation.md"), "---\nenabled: true\nagent: missing\nmodel: auto\n---\n\nGo.\n")
	_, diags := LoadDir(dir)
	if !diagHas(diags, `agent "missing" not found`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func baseTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory())
	writeFile(t, filepath.Join(dir, "runners", "local.yaml"), "description: local\nplatform:\n  os: linux\n  linux:\n    dockerImage: factory-m1-task:dev\n")
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: claude-x\n"))
	return dir
}

func baseFactory() string {
	return `schemaVersion: v1alpha1
name: t
agentDefaults:
  model: auto
  runner: local
`
}

func agentMD(agentType, extra string) string {
	return "---\ndescription: agent\nagentType: " + agentType + "\n" + extra + "---\n\n# Agent\n"
}
