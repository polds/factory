package scorer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
)

// ErrScorerInvalid is the verdict failure for a missing JSON object or a
// label the scorer did not declare. Callers record scorer_invalid. The
// label is never coerced to a failing score.
var ErrScorerInvalid = errors.New("scorer_invalid")

// JudgeContext is the redacted material the caller already assembled.
// JudgePrompt redacts it again with harness.Redact. There is no checkout.
type JudgeContext struct {
	WorkItem        string
	Transcript      string
	Diff            string
	ToolCallDigests string
	Descendants     string
	SuccessCriteria string
	Secrets         []string
}

// Verdict is a parsed judge answer. Score and Passed come from the scorer's
// declared labels and passingScore, not from the model's JSON.
type Verdict struct {
	Label     string
	Score     float64
	Passed    bool
	Reasoning string
}

// JudgePrompt builds the judge prompt. The final assistant message must be
// one JSON object {"label","reasoning"}. Context is inlined. The judge has
// no repository checkout and only the inference credential.
func JudgePrompt(s definition.Scorer, c JudgeContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the scorer %q. Judge the run from the context below. You have no repository checkout and no execution credentials. Only the inference credential is available to this run.\n\n", s.Name)
	b.WriteString("Reply with one JSON object and no other text:\n")
	b.WriteString("{\"label\":\"<one declared label>\",\"reasoning\":\"<why>\"}\n\n")
	b.WriteString("Declared labels:\n")
	if len(s.Labels) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, label := range s.Labels {
		fmt.Fprintf(&b, "- %s (score %g): %s\n", label.Value, label.Score, label.Description)
	}
	fmt.Fprintf(&b, "A label not in this list is invalid. Do not invent a label.\nPassing score: %g\n", s.PassingScore)
	writeSection(&b, "Rubric", s.Body)
	writeSection(&b, "Success criteria", c.SuccessCriteria)
	writeSection(&b, "Work item", c.WorkItem)
	writeSection(&b, "Transcript", c.Transcript)
	writeSection(&b, "Diff", c.Diff)
	writeSection(&b, "Tool calls", c.ToolCallDigests)
	writeSection(&b, "Descendants", c.Descendants)
	return harness.Redact(b.String(), c.Secrets)
}

func writeSection(b *strings.Builder, title, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n## %s\n%s\n", title, body)
}

// ParseVerdict reads the last JSON object in finalMessage.
// An undeclared label or no JSON object returns ErrScorerInvalid.
func ParseVerdict(s definition.Scorer, finalMessage string) (Verdict, error) {
	raw, ok := lastJSONObject(finalMessage)
	if !ok {
		return Verdict{}, fmt.Errorf("%w: no JSON object", ErrScorerInvalid)
	}
	var obj struct {
		Label     *string         `json:"label"`
		Reasoning json.RawMessage `json:"reasoning"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Verdict{}, fmt.Errorf("%w: %w", ErrScorerInvalid, err)
	}
	if obj.Label == nil || *obj.Label == "" {
		return Verdict{}, fmt.Errorf("%w: label missing", ErrScorerInvalid)
	}
	var reasoning string
	if len(obj.Reasoning) > 0 && string(obj.Reasoning) != "null" {
		if err := json.Unmarshal(obj.Reasoning, &reasoning); err != nil {
			return Verdict{}, fmt.Errorf("%w: reasoning is not a string", ErrScorerInvalid)
		}
	}
	score, known := labelScore(s, *obj.Label)
	if !known {
		return Verdict{}, fmt.Errorf("%w: undeclared label %q", ErrScorerInvalid, *obj.Label)
	}
	return Verdict{
		Label:     *obj.Label,
		Score:     score,
		Passed:    score >= s.PassingScore,
		Reasoning: reasoning,
	}, nil
}

func labelScore(s definition.Scorer, label string) (float64, bool) {
	for _, item := range s.Labels {
		if item.Value == label {
			return item.Score, true
		}
	}
	return 0, false
}

func lastJSONObject(s string) (json.RawMessage, bool) {
	var last json.RawMessage
	found := false
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			continue
		}
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		last = append(json.RawMessage(nil), raw...)
		found = true
	}
	return last, found
}

// Correctness is the D1 built-in judge. Labels are pass (1) and fail (0).
// Model comes from scorerDefaults.model. The runner is
// tree.Factory.ScorerDefaults.Runner; a scorer file cannot set runner, so
// this Scorer does not carry one. A scorers/correctness/scorer.md in the
// tree replaces the built-in entirely.
func Correctness(tree *definition.Tree) definition.Scorer {
	if tree != nil {
		if s, ok := tree.Scorers[correctnessName]; ok {
			return s
		}
	}
	model := ""
	if tree != nil && tree.Factory.ScorerDefaults != nil {
		model = strings.TrimSpace(tree.Factory.ScorerDefaults.Model)
	}
	return definition.Scorer{
		Name:        correctnessName,
		Description: "Did the trial meet the task success criteria?",
		Output:      "classification",
		Labels: []definition.ScorerLabel{
			{Value: "pass", Description: "The trial meets the task success criteria.", Score: 1},
			{Value: "fail", Description: "The trial does not meet the task success criteria.", Score: 0},
		},
		PassingScore: 1,
		SamplingRate: 0,
		Model:        model,
		Body:         "Compare the run with the task success criteria. Return pass only when every criterion is met. Return fail otherwise.",
	}
}

const correctnessName = "correctness"
