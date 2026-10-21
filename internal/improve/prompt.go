package improve

import (
	"fmt"
	"strings"
	"time"

	"github.com/polds/factory/internal/harness"
)

// layerTable is the docs/08 "which layer" table, inlined so the improvement
// run does not need the docs tree.
const layerTable = `| Failure pattern | Right layer |
| --- | --- |
| Agent didn't know a repo convention | a skill |
| Agent lacked a tool or repo access | definition (mcpServers, secrets, required_repos) |
| Agent's role was ambiguous | agent instructions |
| Model too weak for the stage | definition (model, harness, or router) |
| The code is genuinely broken | application code |`

// RunPrompt is the improvement run's task text: the failing runs, the
// definition tree listing, the layer table, and prior rejections.
// secrets are removed with harness.Redact.
func RunPrompt(failures []Failure, definitionTree string, priorRejections []string, secrets []string) string {
	var b strings.Builder
	b.WriteString("You are improving the factory definition. Find the common cause of these failures and propose the smallest change that addresses it. Open a pull request. Do not merge it.\n\n")
	b.WriteString("## Failures\n")
	if len(failures) == 0 {
		b.WriteString("(none)\n")
	}
	for _, f := range failures {
		when := ""
		if !f.At.IsZero() {
			when = f.At.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "- run %s scorer %s label %s at %s\n  reasoning: %s\n", f.RunID, f.Scorer, f.Label, when, f.Reasoning)
	}
	b.WriteString("\n## Definition tree\n")
	b.WriteString(strings.TrimSpace(definitionTree))
	b.WriteString("\n\n## Which layer to change\n")
	b.WriteString(layerTable)
	b.WriteString("\n\n## Prior rejections\n")
	if len(priorRejections) == 0 {
		b.WriteString("None.\n")
	}
	for _, note := range priorRejections {
		fmt.Fprintf(&b, "- %s\n", note)
	}
	b.WriteString("\nReply with a pull request that changes one layer.\n")
	return harness.Redact(b.String(), secrets)
}
