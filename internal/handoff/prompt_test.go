package handoff

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
)

func TestBuildPromptGoldenTwoStage(t *testing.T) {
	t.Parallel()
	got := BuildPrompt(goldenAgent(), goldenContext())
	again := BuildPrompt(goldenAgent(), goldenContext())
	if got != again {
		t.Fatal("BuildPrompt is not deterministic")
	}
	want, err := os.ReadFile("testdata/golden_prompt.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	triage := strings.Index(got, "Scope is the payments docs refund link.")
	planning := strings.Index(got, "Update docs/refunds.md anchor and add a link check.")
	specURI := strings.Index(got, "s3://factory/wi_01HQAND0FF0000000000000000/spec.md")
	planURI := strings.Index(got, "s3://factory/wi_01HQAND0FF0000000000000000/plan.md")
	if triage < 0 || planning < 0 || triage > planning {
		t.Fatalf("prior summaries out of order: triage=%d planning=%d", triage, planning)
	}
	if specURI < 0 || planURI < 0 || specURI > planURI {
		t.Fatalf("artifact URIs out of order: spec=%d plan=%d", specURI, planURI)
	}
	if !strings.Contains(got, "Fix the broken documentation link") || !strings.Contains(got, "Point them at /v2/refunds.") {
		t.Fatal("golden prompt missing title or body")
	}
}

func TestBuildPromptEmptyPriorStages(t *testing.T) {
	t.Parallel()
	agent := definition.Agent{Name: "triage", Body: "Sort the intake."}
	ctx := Context{Title: "Hello", Body: "World"}
	got := BuildPrompt(agent, ctx)
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") || !strings.Contains(got, "Sort the intake.") {
		t.Fatalf("empty prior prompt missing work item or instructions:\n%s", got)
	}
	if !strings.Contains(got, "# Prior stage outputs\n(none)\n") {
		t.Fatalf("empty prior stages not marked:\n%s", got)
	}
	ctx.PriorStages = []StageOutput{}
	if BuildPrompt(agent, ctx) != got {
		t.Fatal("nil and empty prior stages differ")
	}
}

func TestBuildPromptOmitsSecretValues(t *testing.T) {
	t.Parallel()
	const (
		planted   = "ghp_Planted/SecretValue99"
		onlyMCP   = "sk-OnlyInMCPEnvironmentValue"
		headerTok = "ghp_HeaderOnlyPlantedToken"
	)
	agent := definition.Agent{
		Name:    "implementer",
		Body:    "Use {{GH_TOKEN}} from the environment. Never print it.",
		Secrets: []string{"GH_TOKEN", planted},
		MCPServers: map[string]definition.MCPServer{
			"github": {
				Env: map[string]string{
					"GH_TOKEN":    onlyMCP,
					"REF_ONLY":    "{{GH_TOKEN}}",
					"AUTH_HEADER": "Bearer {{GH_TOKEN}}",
				},
				Headers: map[string]string{
					"Authorization": "Bearer " + headerTok,
				},
			},
		},
	}
	ctx := Context{
		Title: "Ship the fix",
		Body:  "issue text mentions " + planted,
		Env: map[string]string{
			"GH_TOKEN":        planted,
			"OPENAI_API_KEY":  "sk-plantedopenaikeyvalue",
			"SHORT":           "xy",
			"TEMPLATE":        "{{GH_TOKEN}}",
			"TEMPLATE_BEARER": "Bearer {{NPM_TOKEN}}",
		},
		PriorStages: []StageOutput{{
			Stage:       "triage",
			Agent:       "triage",
			RunID:       "run_01",
			SummaryText: "saw " + planted + " and " + url.QueryEscape(planted) + " and " + headerTok,
		}},
	}
	got := BuildPrompt(agent, ctx)
	forbidden := []string{
		planted,
		url.QueryEscape(planted),
		onlyMCP,
		headerTok,
		"sk-plantedopenaikeyvalue",
		"xy",
	}
	for _, secret := range forbidden {
		if strings.Contains(got, secret) {
			t.Errorf("prompt contains secret %q\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "issue text mentions [REDACTED]") {
		t.Fatalf("body secret was not redacted:\n%s", got)
	}
	if !strings.Contains(got, "saw [REDACTED] and [REDACTED] and [REDACTED]") {
		t.Fatalf("summary secrets were not redacted:\n%s", got)
	}
	if !strings.Contains(got, "{{GH_TOKEN}}") {
		t.Fatalf("secret reference was stripped:\n%s", got)
	}
	if !strings.Contains(got, "Ship the fix") {
		t.Fatal("title missing")
	}
}

func TestBuildPromptHarnessOmitsAuthMaterial(t *testing.T) {
	t.Parallel()
	agent := definition.Agent{
		Name: "reviewer",
		Body: "Review the diff.",
		Harness: &definition.Harness{
			Type:  "claude",
			Model: "claude-5-sonnet-high",
			Auth:  &definition.HarnessAuth{Source: "secret", SecretName: "ANTHROPIC_API_KEY"},
		},
	}
	got := BuildPrompt(agent, Context{Title: "Review", Body: "Look at the PR."})
	if !strings.Contains(got, "Harness: claude\n") || !strings.Contains(got, "Harness model: claude-5-sonnet-high\n") {
		t.Fatalf("harness identity missing:\n%s", got)
	}
	if strings.Contains(got, "ANTHROPIC_API_KEY") || strings.Contains(got, "SecretName") {
		t.Fatalf("harness auth leaked:\n%s", got)
	}
}

func goldenAgent() definition.Agent {
	return definition.Agent{
		Name:        "implementer",
		Description: "Makes the change and opens a pull request.",
		Model:       "claude-5-opus-high",
		Body:        "# Implement\n\nYou make the change described by the work item and the spec.",
		Secrets:     []string{"NPM_TOKEN"},
	}
}

func goldenContext() Context {
	return Context{
		WorkItemID:         "wi_01HQAND0FF0000000000000000",
		ConversationID:     "conv_01HQAND0FF0000000000000000",
		Source:             "issue",
		SourceRef:          "acme/payments-docs#17",
		Title:              "Fix the broken documentation link",
		Body:               "The payments docs link to /v1/refunds, which 404s. Point them at /v2/refunds.",
		AcceptanceCriteria: "The doc link returns HTTP 200 and the PR describes the validation.",
		PriorStages: []StageOutput{
			{
				Stage:        "triage",
				Agent:        "triage",
				RunID:        "run_01HQTR1AGE0000000000000000",
				SummaryText:  "Scope is the payments docs refund link. No code change outside docs/.",
				ArtifactURIs: []string{"s3://factory/wi_01HQAND0FF0000000000000000/triage.txt"},
			},
			{
				Stage:       "planning",
				Agent:       "spec",
				RunID:       "run_01HQPLAN000000000000000000",
				SummaryText: "Update docs/refunds.md anchor and add a link check.",
				ArtifactURIs: []string{
					"s3://factory/wi_01HQAND0FF0000000000000000/spec.md",
					"s3://factory/wi_01HQAND0FF0000000000000000/plan.md",
				},
			},
		},
	}
}
