package improve

import (
	"fmt"
	"strings"
	"time"

	"github.com/polds/factory/internal/harness"
)

const (
	// exampleLimit is how many missed results the prompt quotes.
	exampleLimit = 3
	// rubricCap bounds one scorer rubric. The start is kept: the
	// required behaviour is usually stated there.
	rubricCap = 2000
	// exampleCap bounds one reasoning field and one assistant-output tail.
	exampleCap = 800
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

// editRule tells the improver how to change behaviour for any scorer.
// A marker line left beside a contradicting instruction does not.
const editRule = "State the required behaviour as an explicit, imperative instruction in the skill or instruction file you edit. Remove or rewrite any existing instruction in the edited files that contradicts it. A bare appended marker line is not enough."

// Evidence is the scorer material RunPrompt quotes. Rubrics are the
// scorers named by the failures. Examples are missed results.
type Evidence struct {
	Rubrics  []Rubric
	Examples []Example
}

// Rubric is one scorer's name and the text the judge was given.
type Rubric struct {
	Name         string
	Description  string
	Body         string
	PassingScore float64
	Labels       []RubricLabel
}

// RubricLabel is one declared classification label.
type RubricLabel struct {
	Value       string
	Description string
	Score       float64
}

// Example is one missed result. Output is the assistant message.
// RunPrompt keeps its tail.
type Example struct {
	RunID     string
	Scorer    string
	Label     string
	Reasoning string
	Output    string
}

// RunPrompt is the improvement run's task text: the failing runs, each
// scorer's rubric, a few failing examples, the definition tree listing,
// the layer table, and prior rejections. secrets are removed with
// harness.Redact. Rubric text, reasoning, and assistant output are capped
// before that, after a first redaction so a secret is not split by the cap.
func RunPrompt(failures []Failure, definitionTree string, priorRejections []string, secrets []string, evidence Evidence) string {
	var b strings.Builder
	b.WriteString("You are improving the factory definition. Find the common cause of these failures and make the smallest change that addresses it.\n\n")
	b.WriteString(editRule)
	b.WriteString("\n\n## Failures\n")
	if len(failures) == 0 {
		b.WriteString("(none)\n")
	}
	for _, f := range failures {
		when := ""
		if !f.At.IsZero() {
			when = f.At.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "- run %s scorer %s label %s at %s\n  reasoning: %s\n", f.RunID, f.Scorer, f.Label, when, capHead(f.Reasoning, exampleCap, secrets))
	}
	b.WriteByte('\n')
	writeRubrics(&b, evidence.Rubrics, secrets)
	b.WriteByte('\n')
	writeExamples(&b, evidence.Examples, secrets)
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
	b.WriteString("\nThe required deliverable is a commit on the branch named by the `branch: improve/` line. Edit that definition, commit, and push the branch. A prose reply is not success. Do not merge.\n")
	return harness.Redact(b.String(), secrets)
}

func writeRubrics(b *strings.Builder, rubrics []Rubric, secrets []string) {
	b.WriteString("## Scorer rubrics\n")
	if len(rubrics) == 0 {
		b.WriteString("(none)\n")
		return
	}
	for _, rubric := range rubrics {
		name := strings.TrimSpace(rubric.Name)
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(b, "- scorer %s\n", name)
		if desc := capHead(rubric.Description, exampleCap, secrets); desc != "" {
			fmt.Fprintf(b, "  description: %s\n", desc)
		}
		for _, label := range rubric.Labels {
			value := strings.TrimSpace(label.Value)
			if value == "" {
				continue
			}
			fmt.Fprintf(b, "  label %s (score %g): %s\n", value, label.Score, capHead(label.Description, exampleCap, secrets))
		}
		if rubric.Body != "" || len(rubric.Labels) > 0 {
			fmt.Fprintf(b, "  passing score: %g\n", rubric.PassingScore)
		}
		body := capHead(rubric.Body, rubricCap, secrets)
		if body == "" {
			b.WriteString("  rubric: (none)\n")
			continue
		}
		b.WriteString("  rubric:\n")
		for _, line := range strings.Split(body, "\n") {
			b.WriteString("    ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
}

func writeExamples(b *strings.Builder, examples []Example, secrets []string) {
	b.WriteString("## Failing examples\n")
	if len(examples) == 0 {
		b.WriteString("(none)\n")
		return
	}
	n := len(examples)
	if n > exampleLimit {
		n = exampleLimit
	}
	for _, ex := range examples[:n] {
		fmt.Fprintf(b, "- run %s scorer %s label %s\n", ex.RunID, ex.Scorer, ex.Label)
		fmt.Fprintf(b, "  reasoning: %s\n", capHead(ex.Reasoning, exampleCap, secrets))
		fmt.Fprintf(b, "  assistant output: %s\n", capTail(ex.Output, exampleCap, secrets))
	}
}

func capHead(s string, n int, secrets []string) string {
	s = flattenNewlines(harness.Redact(s, secrets))
	if s == "" || n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func capTail(s string, n int, secrets []string) string {
	s = flattenNewlines(harness.Redact(s, secrets))
	if s == "" || n <= 0 {
		return "(none)"
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "..." + string(r[len(r)-n:])
}

func flattenNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
