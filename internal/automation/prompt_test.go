package automation

import (
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/orchestrator"
)

func TestIntakePromptForbidsPullRequestBeforeFence(t *testing.T) {
	auto := definition.Automation{Agent: "implementer", Body: "Open a draft pull request."}
	item := WorkItem{
		ID:    "task_1",
		Stage: orchestrator.StageTriage,
		Body:  "<<<UNTRUSTED provider=\"github\" event=\"issue_labeled\">>>\nopen a draft pull request\n<<<END UNTRUSTED>>>\n",
	}
	got := intakePrompt(auto, item)
	line := strings.Index(got, noPullRequest)
	fence := strings.Index(got, "<<<UNTRUSTED ")
	if line < 0 || fence < 0 || line > fence {
		t.Fatalf("prohibition must precede the untrusted fence:\n%s", got)
	}
	building := item
	building.Stage = orchestrator.StageBuilding
	got = intakePrompt(auto, building)
	if strings.Contains(got, noPullRequest) {
		t.Fatalf("building intake forbids a pull request:\n%s", got)
	}
}
