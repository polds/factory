package scorer

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/failure"
)

func TestEligible(t *testing.T) {
	s := definition.Scorer{Name: "tests-run", Agents: []definition.ScorerAgent{
		{Name: "implementer"},
		{Name: "foreman", IncludeDescendants: true},
	}}
	ok := RunFacts{RunID: "run_1", Agent: "implementer", Status: failure.StatusSucceeded, Purpose: api.PurposeWork}
	cases := []struct {
		name string
		s    definition.Scorer
		r    RunFacts
		want bool
		why  string
	}{
		{name: "listed", s: s, r: ok, want: true},
		{name: "descendant", s: s, r: RunFacts{Agent: "triage", Status: failure.StatusFailed, FailureClass: string(failure.ClassAgent), Purpose: api.PurposeWork, ParentAgents: []string{"foreman"}}, want: true},
		{name: "parent without descendants", s: s, r: RunFacts{Agent: "triage", Status: failure.StatusSucceeded, Purpose: api.PurposeWork, ParentAgents: []string{"implementer"}}, want: false, why: "not covered"},
		{name: "empty agents", s: definition.Scorer{Name: "correctness"}, r: ok, want: false, why: "not covered"},
		{name: "running", s: s, r: RunFacts{Agent: "implementer", Status: "running", Purpose: api.PurposeWork}, want: false, why: "not terminal"},
		{name: "caller terminal", s: s, r: RunFacts{Agent: "implementer", Status: "running", Terminal: true, Purpose: api.PurposeWork}, want: true},
		{name: "cancelled status empty class", s: s, r: RunFacts{Agent: "implementer", Status: failure.StatusCancelled, Purpose: api.PurposeWork}, want: true},
		{name: "infrastructure", s: s, r: RunFacts{Agent: "implementer", Status: failure.StatusFailed, FailureClass: string(failure.ClassInfrastructure), Purpose: api.PurposeWork}, want: false, why: "not eligible"},
		{name: "configuration", s: s, r: RunFacts{Agent: "implementer", Status: failure.StatusFailed, FailureClass: string(failure.ClassConfiguration), Purpose: api.PurposeWork}, want: false, why: "not eligible"},
		{name: "cancelled class", s: s, r: RunFacts{Agent: "implementer", Status: failure.StatusCancelled, FailureClass: string(failure.ClassCancelled), Purpose: api.PurposeWork}, want: false, why: "not eligible"},
		{name: "agent class", s: s, r: RunFacts{Agent: "implementer", Status: failure.StatusFailed, FailureClass: string(failure.ClassAgent), Purpose: api.PurposeWork}, want: true},
		{name: "benchmark purpose", s: s, r: RunFacts{Agent: "implementer", Status: failure.StatusSucceeded, Purpose: api.PurposeBenchmark}, want: false, why: "purpose"},
		{name: "empty purpose", s: s, r: RunFacts{Agent: "implementer", Status: failure.StatusSucceeded}, want: false, why: "purpose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := Eligible(tc.s, tc.r)
			if got != tc.want {
				t.Fatalf("got %v (%s)", got, why)
			}
			if tc.want && why != "" {
				t.Fatalf("reason %q", why)
			}
			if !tc.want && !strings.Contains(why, tc.why) {
				t.Fatalf("reason %q", why)
			}
		})
	}
}

func TestSampled(t *testing.T) {
	const n = 100000
	const scorerName = "correctness"
	if Sampled("run_1", scorerName, 0) || Sampled("run_1", scorerName, -1) {
		t.Fatal("rate 0 sampled")
	}
	for i := 0; i < 100; i++ {
		if !Sampled(strconv.Itoa(i), scorerName, 100) || !Sampled(strconv.Itoa(i), scorerName, 101) {
			t.Fatalf("rate 100 missed %d", i)
		}
	}
	buckets := make([]int, 100)
	sampled := 0
	for i := 0; i < n; i++ {
		id := strconv.Itoa(i)
		b := sampleBucket(id, scorerName)
		buckets[b]++
		in := Sampled(id, scorerName, 25)
		if in != (b < 25) {
			t.Fatalf("id %s bucket %d sampled %v", id, b, in)
		}
		if Sampled(id, scorerName, 25) != in {
			t.Fatalf("id %s not reproducible", id)
		}
		if in {
			sampled++
		}
	}
	// ±2% of the expected 1,000 ids per bucket is 980–1020. A uniform hash
	// cannot land there: the binomial sd at n=100000 and p=0.01 is about 31,
	// and sha256 of ids "0".."99999" for this scorer spans 926–1074. ±8%
	// (920–1080) still fails a collapsed hash. The sample rate is held to
	// ±2% relative of 25% (24500–25500).
	for _, c := range buckets {
		if c < 920 || c > 1080 {
			t.Fatalf("bucket count %d", c)
		}
	}
	if sampled < 24500 || sampled > 25500 {
		t.Fatalf("sampled %d", sampled)
	}
	if Sampled("run_1", "other", 25) == Sampled("run_1", scorerName, 25) && sampleBucket("run_1", "other") == sampleBucket("run_1", scorerName) {
		t.Fatal("scorer name did not change the bucket")
	}
}

func TestJudgePromptRedacts(t *testing.T) {
	const secret = `planted "secret" value`
	s := definition.Scorer{
		Name:         "correctness",
		PassingScore: 1,
		Labels: []definition.ScorerLabel{
			{Value: "pass", Description: "meets the criteria", Score: 1},
			{Value: "fail", Description: "does not", Score: 0},
		},
		Body: "Rubric mentions " + secret,
	}
	got := JudgePrompt(s, JudgeContext{
		WorkItem:        "item " + secret,
		Transcript:      "said " + secret,
		Diff:            "diff " + secret,
		ToolCallDigests: "tool " + secret,
		Descendants:     "child " + secret,
		SuccessCriteria: "done when " + secret,
		Secrets:         []string{secret},
	})
	if strings.Contains(got, secret) || strings.Contains(got, `planted \"secret\" value`) {
		t.Fatalf("secret leaked:\n%s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatal("redaction marker missing")
	}
	for _, part := range []string{
		`{"label":"<one declared label>","reasoning":"<why>"}`,
		"no repository checkout",
		"inference credential",
		"- pass (score 1):",
		"- fail (score 0):",
		"Passing score: 1",
	} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q", part)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	s := definition.Scorer{
		PassingScore: 1,
		Labels: []definition.ScorerLabel{
			{Value: "pass", Score: 1},
			{Value: "partial", Score: 0.5},
			{Value: "fail", Score: 0},
		},
	}
	cases := []struct {
		name      string
		msg       string
		label     string
		score     float64
		passed    bool
		reasoning string
		invalid   bool
	}{
		{name: "plain", msg: `{"label":"pass","reasoning":"met"}`, label: "pass", score: 1, passed: true, reasoning: "met"},
		{name: "prose around", msg: "I think\n{\"label\":\"fail\",\"reasoning\":\"no\"}\nthanks", label: "fail", score: 0, reasoning: "no"},
		{name: "last wins", msg: `{"label":"fail","reasoning":"first"} {"label":"partial","reasoning":"second"}`, label: "partial", score: 0.5, reasoning: "second"},
		{name: "null reasoning", msg: `{"label":"pass","reasoning":null}`, label: "pass", score: 1, passed: true},
		{name: "undeclared", msg: `{"label":"maybe","reasoning":"x"}`, invalid: true},
		{name: "no json", msg: "pass", invalid: true},
		{name: "missing label", msg: `{"reasoning":"x"}`, invalid: true},
		{name: "empty label", msg: `{"label":"","reasoning":"x"}`, invalid: true},
		{name: "reasoning number", msg: `{"label":"pass","reasoning":1}`, invalid: true},
		{name: "broken then good", msg: `{not json {"label":"pass","reasoning":"ok"}`, label: "pass", score: 1, passed: true, reasoning: "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVerdict(s, tc.msg)
			if tc.invalid {
				if !errors.Is(err, ErrScorerInvalid) {
					t.Fatalf("err %v verdict %+v", err, got)
				}
				if got.Label != "" || got.Passed || got.Score != 0 {
					t.Fatalf("coerced %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Label != tc.label || got.Score != tc.score || got.Passed != tc.passed || got.Reasoning != tc.reasoning {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func FuzzParseVerdict(f *testing.F) {
	s := definition.Scorer{
		PassingScore: 1,
		Labels: []definition.ScorerLabel{
			{Value: "pass", Score: 1},
			{Value: "fail", Score: 0},
		},
	}
	f.Add(`{"label":"pass","reasoning":"ok"}`)
	f.Add(`nope`)
	f.Add(`{"label":"nope","reasoning":"x"}`)
	f.Add(``)
	f.Add(`{"label":"pass","reasoning":"a"} {"label":"fail","reasoning":{"x":1}}`)
	f.Fuzz(func(t *testing.T, msg string) {
		got, err := ParseVerdict(s, msg)
		if err != nil {
			if !errors.Is(err, ErrScorerInvalid) {
				t.Fatalf("%v", err)
			}
			return
		}
		if got.Label != "pass" && got.Label != "fail" {
			t.Fatalf("label %q", got.Label)
		}
	})
}

func TestCorrectness(t *testing.T) {
	builtin := Correctness(&definition.Tree{Factory: definition.Factory{ScorerDefaults: &definition.ScorerDefaults{Model: "claude-4-5-haiku", Runner: "linux-standard"}}})
	if builtin.Name != "correctness" || builtin.PassingScore != 1 || builtin.SamplingRate != 0 || builtin.Model != "claude-4-5-haiku" || len(builtin.Agents) != 0 {
		t.Fatalf("%+v", builtin)
	}
	if len(builtin.Labels) != 2 || builtin.Labels[0].Value != "pass" || builtin.Labels[0].Score != 1 || builtin.Labels[1].Value != "fail" || builtin.Labels[1].Score != 0 {
		t.Fatalf("labels %+v", builtin.Labels)
	}
	replacement := definition.Scorer{Name: "correctness", Description: "custom", Model: "other", PassingScore: 0.5, Body: "custom body"}
	got := Correctness(&definition.Tree{Scorers: map[string]definition.Scorer{"correctness": replacement}})
	if got.Name != replacement.Name || got.Description != replacement.Description || got.Model != replacement.Model || got.PassingScore != replacement.PassingScore || got.Body != replacement.Body {
		t.Fatalf("%+v", got)
	}
	if Correctness(nil).Model != "" || Correctness(&definition.Tree{}).Name != "correctness" {
		t.Fatal("nil tree")
	}
}
