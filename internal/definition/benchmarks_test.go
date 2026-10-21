package definition

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestValidDefinitionRef(t *testing.T) {
	good := []string{
		"benchmarks/pr-review/candidate",
		"HEAD",
		"refs/heads/main",
		"0123456789abcdef0123456789abcdef01234567",
		"0123456789ABCDEF0123456789ABCDEF01234567",
	}
	bad := []string{
		"",
		"@",
		"/heads/main",
		"-main",
		"foo..bar",
		"a@{b",
		"main.lock",
		"refs/heads/main.lock",
		"a//b",
		`a\b`,
		"has space",
		"has\nnewline",
		".hidden",
		"refs/.hidden",
		"refs/heads/main.",
	}
	for _, ref := range good {
		if !validDefinitionRef(ref) {
			t.Errorf("rejected %q", ref)
		}
	}
	for _, ref := range bad {
		if validDefinitionRef(ref) {
			t.Errorf("accepted %q", ref)
		}
	}
}

func TestLoadBenchmarks(t *testing.T) {
	dir := benchFixture(t, validSuite(), validTask())
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("%s", formatDiags(diags))
	}
	suite, ok := tree.Benchmarks["pr-review"]
	if !ok || suite.Name != "Pull request review" || suite.Agent != "implementer" {
		t.Fatalf("suite %+v ok=%v", suite, ok)
	}
	if suite.Configurations[1].DefinitionRef != "benchmarks/pr-review/candidate" {
		t.Fatalf("definitionRef %+v", suite.Configurations[1])
	}
	h := suite.Configurations[1].Agents[0].Harness
	if h == nil || h.Type != "claude-code" || h.Auth == nil || h.Auth.SecretName != "ANTHROPIC_API_KEY" {
		t.Fatalf("harness %+v", h)
	}
	tasks := tree.BenchmarkTasks["pr-review"]
	task, ok := tasks["broken-doc-link"]
	if !ok || task.Name != "broken-doc-link" || task.StartingRepoRefs[0].Repo != "payments-docs" {
		t.Fatalf("task %+v", task)
	}
	if _, ok := tasks["extra-unlisted"]; !ok {
		t.Fatal("unlisted task file was dropped")
	}
	if len(suite.Tasks) != 1 {
		t.Fatalf("tasks %v", suite.Tasks)
	}
}

func TestLoadBenchmarkErrors(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name              string
		suite, task, want string
	}{
		{name: "unknown key", suite: validSuite() + "surprise: 1\n", task: validTask(), want: `unknown key "surprise"`},
		{name: "zero configs", suite: strings.Replace(validSuite(), twoConfigBlock(), "configurations: []\n", 1), task: validTask(), want: "configurations must be from 1 to 6"},
		{name: "seven configs", suite: manyConfigs(7), task: validTask(), want: "configurations must be from 1 to 6"},
		{name: "unknown suite agent", suite: strings.Replace(validSuite(), "agent: implementer", "agent: missing", 1), task: validTask(), want: `agent "missing" not found`},
		{name: "unknown binding agent", suite: strings.Replace(validSuite(), "agent: implementer\n        model: auto", "agent: missing\n        model: auto", 1), task: validTask(), want: `agent "missing" not found`},
		{name: "both bindings", suite: strings.Replace(validSuite(), "model: auto", "model: auto\n        harness:\n          type: claude-code\n          model: claude-5-opus-high", 1), task: validTask(), want: "sets both model and harness"},
		{name: "neither binding", suite: strings.Replace(validSuite(), "model: auto", "model: \"\"", 1), task: validTask(), want: "requires model or harness"},
		{name: "missing task", suite: strings.Replace(validSuite(), "tasks: [broken-doc-link]", "tasks: [missing]", 1), task: validTask(), want: `task "missing" not found`},
		{name: "bad ref", suite: validSuite(), task: strings.Replace(validTask(), "github.com:acme/payments-docs@"+sha, "github.com:acme/payments-docs@main", 1), want: "sha is not 40 hex characters"},
		{name: "bad definitionRef", suite: strings.Replace(validSuite(), "definitionRef: benchmarks/pr-review/candidate", "definitionRef: ../candidate", 1), task: validTask(), want: "not a git ref or a 40-hex SHA"},
		{name: "empty name", suite: strings.Replace(validSuite(), "name: Pull request review", "name: \"\"", 1), task: validTask(), want: "name is required"},
		{name: "duplicate config", suite: strings.Replace(validSuite(), "name: Candidate", "name: Baseline", 1), task: validTask(), want: `duplicate configuration "Baseline"`},
		{name: "duplicate task", suite: strings.Replace(validSuite(), "tasks: [broken-doc-link]", "tasks: [broken-doc-link, broken-doc-link]", 1), task: validTask(), want: `duplicate task "broken-doc-link"`},
		{name: "empty title", suite: validSuite(), task: strings.Replace(validTask(), "title: Fix a broken documentation link", "title: \"\"", 1), want: "title is required"},
		{name: "refs not a list", suite: validSuite(), task: strings.Replace(validTask(), "startingRepoRefs:\n  - github.com:acme/payments-docs@"+sha+"\n", "startingRepoRefs: github.com:acme/payments-docs@"+sha+"\n", 1), want: "startingRepoRefs must be a list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := LoadDir(benchFixture(t, tc.suite, tc.task))
			if !strings.Contains(formatDiags(diags), tc.want) {
				t.Fatalf("missing %q in\n%s", tc.want, formatDiags(diags))
			}
		})
	}
	if _, diags := LoadDir(benchFixture(t, manyConfigs(6), validTask())); len(diags) != 0 {
		t.Fatalf("six configurations:\n%s", formatDiags(diags))
	}
	shaSuite := strings.Replace(validSuite(), "definitionRef: benchmarks/pr-review/candidate", "definitionRef: "+sha, 1)
	if _, diags := LoadDir(benchFixture(t, shaSuite, validTask())); len(diags) != 0 {
		t.Fatalf("sha definitionRef:\n%s", formatDiags(diags))
	}
}

func TestAcmeBenchmark(t *testing.T) {
	tree, diags := LoadDir(filepath.Join("..", "..", "examples", "acme-factory"))
	for _, d := range diags {
		if d.Severity == SeverityError || d.Message != FilterNotAuthorizationWarning {
			t.Fatalf("%s", formatDiags(diags))
		}
	}
	suite := tree.Benchmarks["pr-review"]
	if suite.Name != "Pull request review" || len(suite.Configurations) != 2 || suite.Tasks[0] != "broken-doc-link" {
		t.Fatalf("%+v", suite)
	}
	if suite.Configurations[0].Name != "Baseline" || suite.Configurations[0].Agents[0].Model != "auto" {
		t.Fatalf("baseline %+v", suite.Configurations[0])
	}
	if suite.Configurations[1].DefinitionRef != "benchmarks/pr-review/candidate" || suite.Configurations[1].Agents[0].Model != "claude-5-opus-high" {
		t.Fatalf("candidate %+v", suite.Configurations[1])
	}
	task := tree.BenchmarkTasks["pr-review"]["broken-doc-link"]
	if task.Title == "" || task.StartingRepoRefs[0].String() != "github.com:acme/payments-docs@0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("%+v", task)
	}
}

func TestSelfImprovementValidation(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  string
		value int
	}{
		{name: "omitted", want: ""},
		{name: "threshold zero", block: "selfImprovement:\n  failedRunThreshold: 0\n", want: "failedRunThreshold must be from 1 to 50"},
		{name: "threshold 51", block: "selfImprovement:\n  failedRunThreshold: 51\n", want: "failedRunThreshold must be from 1 to 50"},
		{name: "threshold 1", block: "selfImprovement:\n  failedRunThreshold: 1\n", value: 1},
		{name: "threshold 50", block: "selfImprovement:\n  failedRunThreshold: 50\n", value: 50},
		{name: "stale zero", block: "selfImprovement:\n  staleFailureDays: 0\n", want: "staleFailureDays must be >= 1"},
		{name: "stale 1", block: "selfImprovement:\n  staleFailureDays: 1\n", value: 1},
		{name: "bad reviewer", block: "selfImprovement:\n  reviewerType: bosses\n", want: `reviewerType "bosses"`},
		{name: "custom missing", block: "selfImprovement:\n  reviewerType: custom\n", want: "reviewerEmails is required when reviewerType is custom"},
		{name: "custom blank", block: "selfImprovement:\n  reviewerType: custom\n  reviewerEmails: [\" \"]\n", want: "reviewerEmails is required when reviewerType is custom"},
		{name: "custom ok", block: "selfImprovement:\n  reviewerType: custom\n  reviewerEmails: [a@acme.example]\n"},
		{name: "admins with emails", block: "selfImprovement:\n  reviewerType: admins\n  reviewerEmails: [a@acme.example]\n"},
		{name: "none", block: "selfImprovement:\n  reviewerType: none\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			copyDir(t, filepath.Join("testdata", "valid", "m2"), dir)
			body := readWant(t, filepath.Join(dir, "factory.yaml"))
			if tc.block != "" {
				body = strings.TrimRight(body, "\n") + "\n" + tc.block
			}
			writeFile(t, filepath.Join(dir, "factory.yaml"), body)
			tree, diags := LoadDir(dir)
			blob := formatDiags(diags)
			if tc.want == "" && blob != "" {
				t.Fatalf("%s", blob)
			}
			if tc.want != "" && !strings.Contains(blob, tc.want) {
				t.Fatalf("missing %q in\n%s", tc.want, blob)
			}
			if tc.name == "omitted" {
				if tree.Factory.SelfImprovement != nil {
					t.Fatalf("%+v", tree.Factory.SelfImprovement)
				}
				return
			}
			si := tree.Factory.SelfImprovement
			if si == nil {
				t.Fatal("nil selfImprovement")
			}
			switch {
			case strings.Contains(tc.block, "failedRunThreshold") && tc.want == "":
				if si.FailedRunThreshold != tc.value {
					t.Fatalf("threshold %d", si.FailedRunThreshold)
				}
			case strings.Contains(tc.block, "staleFailureDays") && tc.want == "":
				if si.StaleFailureDays != tc.value {
					t.Fatalf("days %d", si.StaleFailureDays)
				}
			case tc.name == "admins with emails":
				if len(si.ReviewerEmails) != 1 {
					t.Fatalf("emails %#v", si.ReviewerEmails)
				}
			}
		})
	}
}

func benchFixture(t *testing.T, suite, task string) string {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, filepath.Join("testdata", "valid", "m2"), dir)
	writeFile(t, filepath.Join(dir, "benchmarks", "pr-review", "suite.yaml"), suite)
	writeFile(t, filepath.Join(dir, "benchmarks", "pr-review", "tasks", "broken-doc-link.yaml"), task)
	writeFile(t, filepath.Join(dir, "benchmarks", "pr-review", "tasks", "extra-unlisted.yaml"), validTask())
	return dir
}

func validSuite() string {
	return `name: Pull request review
description: Compare configurations.
agent: implementer
configurations:
  - name: Baseline
    agents:
      - agent: implementer
        model: auto
  - name: Candidate
    definitionRef: benchmarks/pr-review/candidate
    agents:
      - agent: implementer
        harness:
          type: claude-code
          model: claude-5-opus-high
          auth:
            source: managedSecret
            secretName: ANTHROPIC_API_KEY
tasks: [broken-doc-link]
`
}

func twoConfigBlock() string {
	return `configurations:
  - name: Baseline
    agents:
      - agent: implementer
        model: auto
  - name: Candidate
    definitionRef: benchmarks/pr-review/candidate
    agents:
      - agent: implementer
        harness:
          type: claude-code
          model: claude-5-opus-high
          auth:
            source: managedSecret
            secretName: ANTHROPIC_API_KEY
`
}

func validTask() string {
	return `title: Fix a broken documentation link
prompt: Find the broken internal documentation link and update it.
successCriteria: The destination resolves.
sourceRunId: run_1
tags: [docs]
startingRepoRefs:
  - github.com:acme/payments-docs@0123456789abcdef0123456789abcdef01234567
`
}

func manyConfigs(n int) string {
	var b strings.Builder
	b.WriteString("name: Pull request review\nagent: implementer\nconfigurations:\n")
	for i := 0; i < n; i++ {
		b.WriteString("  - name: c" + strconv.Itoa(i) + "\n    agents:\n      - { agent: implementer, model: auto }\n")
	}
	b.WriteString("tasks: [broken-doc-link]\n")
	return b.String()
}
