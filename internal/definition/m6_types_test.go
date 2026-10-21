package definition

import (
	"strings"
	"testing"

	syaml "sigs.k8s.io/yaml"
)

func TestParseRepoRef(t *testing.T) {
	const token = "github.com:acme/payments-docs@0123456789abcdef0123456789abcdef01234567"
	got, err := ParseRepoRef(token)
	if err != nil {
		t.Fatal(err)
	}
	want := RepoRef{Host: "github.com", Owner: "acme", Repo: "payments-docs", SHA: "0123456789abcdef0123456789abcdef01234567"}
	if got != want {
		t.Fatalf("got %+v", got)
	}
	if got.String() != token {
		t.Fatalf("string %s", got.String())
	}
	for _, bad := range []string{
		"acme/payments-docs@0123456789abcdef0123456789abcdef01234567",
		"github.com:acme/payments-docs@main",
		"github.com:acme/payments-docs@0123456789abcdef",
		"github.com:acme/payments-docs@0123456789ABCDEF0123456789ABCDEF01234567",
		"github.com:acme/payments/docs@0123456789abcdef0123456789abcdef01234567",
		"",
	} {
		if _, err := ParseRepoRef(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestBenchmarkSuiteYAML(t *testing.T) {
	const raw = `
name: Pull request review
description: Compare configurations for the review agent.
agent: reviewer
configurations:
  - name: Baseline
    agents:
      - agent: reviewer
        model: auto
  - name: Candidate
    definitionRef: benchmarks/pr-review/candidate
    agents:
      - agent: reviewer
        harness:
          type: claude-code
          model: claude-5-opus-high
          auth:
            source: managedSecret
            secretName: ANTHROPIC_API_KEY
tasks: [broken-doc-link]
`
	var suite BenchmarkSuite
	if err := syaml.Unmarshal([]byte(raw), &suite); err != nil {
		t.Fatal(err)
	}
	if suite.Agent != "reviewer" || len(suite.Configurations) != 2 || len(suite.Tasks) != 1 {
		t.Fatalf("%+v", suite)
	}
	base := suite.Configurations[0]
	if base.DefinitionRef != "" || len(base.Agents) != 1 || base.Agents[0].Model != "auto" || base.Agents[0].Harness != nil {
		t.Fatalf("baseline %+v", base)
	}
	candidate := suite.Configurations[1]
	if candidate.DefinitionRef != "benchmarks/pr-review/candidate" {
		t.Fatalf("definitionRef %q", candidate.DefinitionRef)
	}
	h := candidate.Agents[0].Harness
	if h == nil || h.Type != "claude-code" || h.Model != "claude-5-opus-high" || h.Auth == nil || h.Auth.SecretName != "ANTHROPIC_API_KEY" {
		t.Fatalf("harness %+v", h)
	}
}

func TestBenchmarkTaskRepoRefYAML(t *testing.T) {
	const raw = `
title: Fix a broken documentation link
prompt: Find the broken internal documentation link and update it.
successCriteria: The destination resolves.
sourceRunId: run_1
tags: [docs]
startingRepoRefs:
  - github.com:acme/payments-docs@0123456789abcdef0123456789abcdef01234567
`
	var task BenchmarkTask
	if err := syaml.Unmarshal([]byte(raw), &task); err != nil {
		t.Fatal(err)
	}
	if task.SuccessCriteria == "" || task.SourceRunID != "run_1" || len(task.StartingRepoRefs) != 1 {
		t.Fatalf("%+v", task)
	}
	if task.StartingRepoRefs[0].SHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("%+v", task.StartingRepoRefs[0])
	}
}

func TestM6FuncSignatures(t *testing.T) {
	checks := []func(base, head *Tree) []Diagnostic{WeakensApprovals, GuardRails}
	if got := checks[0](nil, nil); got != nil {
		t.Fatalf("weaken %#v", got)
	}
	if got := checks[1](&Tree{}, &Tree{}); got != nil {
		t.Fatalf("rails %#v", got)
	}
	base := &Tree{Factory: Factory{Pipeline: &Pipeline{Approvals: map[string]ApprovalGate{
		"spec": {Required: true, Approvers: "role:editor"},
	}}}}
	head := &Tree{Factory: Factory{Pipeline: &Pipeline{Approvals: map[string]ApprovalGate{}}}}
	for i, fn := range checks {
		got := fn(base, head)
		found := false
		for _, d := range got {
			if d.File == "factory.yaml" && d.Severity == SeverityError && strings.Contains(d.Message, "spec") {
				found = true
			}
		}
		if !found {
			t.Fatalf("check %d: %#v", i, got)
		}
	}
}
