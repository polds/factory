package definition

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTriggerUnknownKeyAndAlias(t *testing.T) {
	prog := CheckTrigger(Trigger{
		Provider: "github", Event: "push",
		Filter: map[string]any{"nope": []any{"x"}},
	}, nil)
	if prog.OK || !triggerDiag(prog.Diags, `unknown filter key "nope" for provider "github"`) {
		t.Fatalf("%+v", prog.Diags)
	}

	both := CheckTrigger(Trigger{
		Provider: "linear", Event: "issue_created",
		Filter: map[string]any{"teams": []any{"Core"}, "team_ids": []any{"id"}},
	}, nil)
	if both.OK || !triggerDiag(both.Diags, "mutually exclusive") {
		t.Fatalf("%+v", both.Diags)
	}

	teams := CheckTrigger(Trigger{
		Provider: "teams", Event: "message_posted",
		Filter: map[string]any{"channels": []any{"general"}},
	}, nil)
	if teams.OK || !triggerDiag(teams.Diags, "unknown filter key") {
		t.Fatalf("%+v", teams.Diags)
	}
}

func TestTriggerOverlapAndAuth(t *testing.T) {
	autos := map[string]Automation{
		"a": {Name: "a", Enabled: true, Triggers: []Trigger{{
			Provider: "github", Event: "issue_labeled",
			Filter: map[string]any{"repos": []any{"acme/pay"}, "labels": []any{"bug"}},
		}}},
		"b": {Name: "b", Enabled: true, Triggers: []Trigger{{
			Provider: "github", Event: "issue_labeled",
			Filter: map[string]any{"repos": []any{"acme/other"}},
		}}},
		"c": {Name: "c", Enabled: true, Triggers: []Trigger{{
			Provider: "github", Event: "issue_labeled",
			Filter: map[string]any{"labels": []any{"bug"}},
		}}},
		"d": {Name: "d", Enabled: false, Triggers: []Trigger{{
			Provider: "github", Event: "issue_labeled",
			Filter: map[string]any{"labels": []any{"docs"}},
		}}},
		"e": {Name: "e", Enabled: true, Triggers: []Trigger{{
			Provider: "slack", Event: "message_posted",
			Filter: map[string]any{"channels": []any{"C1"}},
		}}},
	}
	diags := CheckAutomations(autos, nil)
	if triggerDiag(diags, `automation "b" overlaps`) {
		t.Fatalf("disjoint repos warned:\n%s", formatDiags(diags))
	}
	if !triggerDiag(diags, `automation "c" overlaps automation "a" on github/issue_labeled`) {
		t.Fatalf("missing overlap:\n%s", formatDiags(diags))
	}
	if strings.Contains(formatDiags(diags), `"d"`) {
		t.Fatalf("disabled automation overlapped:\n%s", formatDiags(diags))
	}
	if !triggerDiag(diags, FilterNotAuthorizationWarning) {
		t.Fatal("missing authorization warning")
	}
	for _, d := range diags {
		if d.Severity == SeverityError {
			t.Fatalf("unexpected error %s", d.Message)
		}
	}
}

func TestTriggerPayloadDisjoint(t *testing.T) {
	autos := map[string]Automation{
		"open": {Name: "open", Enabled: true, Triggers: []Trigger{{
			Provider: "webhook", Event: "received",
			Filter: map[string]any{
				"webhook_ids": []any{"whk_a"},
				"payload":     map[string]any{"action": []any{"opened"}},
			},
		}}},
		"close": {Name: "close", Enabled: true, Triggers: []Trigger{{
			Provider: "webhook", Event: "received",
			Filter: map[string]any{
				"webhook_ids": []any{"whk_b"},
				"payload":     map[string]any{"action": []any{"closed"}},
			},
		}}},
	}
	diags := CheckAutomations(autos, nil)
	if triggerDiag(diags, "overlaps") {
		t.Fatalf("provably disjoint payloads warned:\n%s", formatDiags(diags))
	}
	if triggerDiag(diags, "more than one trigger") {
		t.Fatalf("distinct webhook ids collided:\n%s", formatDiags(diags))
	}
}

func TestWebhookUsedOnce(t *testing.T) {
	autos := map[string]Automation{
		"one": {Name: "one", Enabled: true, Triggers: []Trigger{{
			Provider: "webhook", Event: "received",
			Filter: map[string]any{"webhook_ids": []any{"whk_same"}},
		}}},
		"two": {Name: "two", Enabled: true, Triggers: []Trigger{{
			Provider: "webhook", Event: "received",
			Filter: map[string]any{"webhook_ids": map[string]any{"in": []any{"whk_same"}}},
		}}},
	}
	diags := CheckAutomations(autos, nil)
	if !triggerDiag(diags, `webhook "whk_same" is used by more than one trigger`) {
		t.Fatalf("%s", formatDiags(diags))
	}
}

func TestLoadDirTriggerDiagnostics(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "automations", "intake", "automation.md"), `---
enabled: true
agent: impl
triggers:
  - provider: github
    event: push
    filter:
      nope: [x]
---

Go.
`)
	_, diags := LoadDir(dir)
	if !diagHas(diags, `unknown filter key "nope"`) {
		t.Fatalf("%s", formatDiags(diags))
	}
	if !diagHas(diags, "not an authorization boundary") {
		t.Fatalf("%s", formatDiags(diags))
	}
}

func triggerDiag(ds []Diagnostic, sub string) bool {
	for _, d := range ds {
		if strings.Contains(d.Message, sub) {
			return true
		}
	}
	return false
}
