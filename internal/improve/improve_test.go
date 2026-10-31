package improve

import (
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/definition"
)

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	week := now.Add(-7 * 24 * time.Hour)
	just := week.Add(time.Nanosecond)
	cases := []struct {
		name     string
		cfg      definition.SelfImprovement
		failures []Failure
		flight   map[string]bool
		want     string
	}{
		{
			name:     "threshold",
			failures: repeat("reviewer", 25, now),
			want:     "reviewer",
		},
		{
			name:     "below default threshold",
			failures: repeat("reviewer", 24, now),
		},
		{
			name:     "explicit threshold",
			cfg:      definition.SelfImprovement{FailedRunThreshold: 2},
			failures: repeat("reviewer", 2, now),
			want:     "reviewer",
		},
		{
			name:     "stale",
			failures: []Failure{{Agent: "reviewer", At: week}},
			want:     "reviewer",
		},
		{
			name:     "not quite stale",
			failures: []Failure{{Agent: "reviewer", At: just}},
		},
		{
			name:     "zero time counts but is not stale",
			cfg:      definition.SelfImprovement{FailedRunThreshold: 2, StaleFailureDays: 7},
			failures: []Failure{{Agent: "reviewer"}, {Agent: "reviewer"}},
			want:     "reviewer",
		},
		{
			name:     "zero time alone",
			failures: []Failure{{Agent: "reviewer"}},
		},
		{
			name:     "in flight",
			failures: repeat("reviewer", 25, now),
			flight:   map[string]bool{"reviewer": true},
		},
		{
			name:     "empty agent",
			failures: []Failure{{Agent: "", At: week}, {Agent: "spec", At: week}},
			want:     "spec",
		},
		{
			name: "sorted",
			failures: []Failure{
				{Agent: "b", At: week},
				{Agent: "a", At: week},
			},
			want: "a,b",
		},
		{
			name:     "negative uses defaults",
			cfg:      definition.SelfImprovement{FailedRunThreshold: -1, StaleFailureDays: -1},
			failures: repeat("reviewer", 24, now),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Due(now, tc.failures, tc.cfg, tc.flight), ",")
			if got != tc.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestRunPromptAndPRBody(t *testing.T) {
	const secret = "planted-secret-value"
	when := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	failures := []Failure{{
		ResultID:  "sr_1",
		RunID:     "run_1",
		Agent:     "reviewer",
		Scorer:    "correctness",
		Label:     "fail",
		Reasoning: "saw " + secret,
		At:        when,
	}}
	prompt := RunPrompt(failures, "factory.yaml\n", nil, []string{secret}, Evidence{})
	if strings.Contains(prompt, secret) {
		t.Fatalf("prompt leaked\n%s", prompt)
	}
	for _, part := range []string{
		"run run_1",
		"correctness",
		"factory.yaml",
		"Agent didn't know a repo convention",
		"a skill",
		"mcpServers",
		"agent instructions",
		"application code",
		"None.",
		"[REDACTED]",
		"A prose reply is not success",
		"commit, and push the branch",
		"explicit, imperative instruction",
		"Remove or rewrite any existing instruction",
		"bare appended marker line",
		"## Scorer rubrics",
		"## Failing examples",
	} {
		if !strings.Contains(prompt, part) {
			t.Fatalf("prompt missing %q\n%s", part, prompt)
		}
	}
	withPrior := RunPrompt(nil, "tree", []string{"declined the skill change"}, nil, Evidence{})
	if strings.Contains(withPrior, "None.") || !strings.Contains(withPrior, "declined the skill change") {
		t.Fatalf("%s", withPrior)
	}

	const want = `## Regressions addressed

- run run_1 (https://factory.example/runs/run_1), scorer result sr_1 (https://factory.example/results/sr_1), scorer correctness, label fail
- run run_2 (run_2), scorer result sr_2 (sr_2), scorer tests-run, label miss

Layer changed: skill

Opened by the factory self-improvement loop. Merging marks these failures reviewed. Closing without merge marks them reviewed and records the rejection so the next improvement run can see it.
`
	exact := PRBody(Batch{Layer: "skill"}, []Failure{
		{ResultID: "sr_1", RunID: "run_1", Scorer: "correctness", Label: "fail"},
		{ResultID: "sr_2", RunID: "run_2", Scorer: "tests-run", Label: "miss"},
	}, Links{
		Run: func(id string) string {
			if id == "run_1" {
				return "https://factory.example/runs/run_1"
			}
			return ""
		},
		Result: func(id string) string {
			if id == "sr_1" {
				return "https://factory.example/results/sr_1"
			}
			return ""
		},
	})
	if exact != want {
		t.Fatalf("golden\n%s", exact)
	}
	redacted := PRBody(Batch{Layer: "skill " + secret, Secrets: []string{secret}}, failures[:1], Links{
		Run:    func(string) string { return "https://factory.example/runs/run_1" },
		Result: func(string) string { return "https://factory.example/results/sr_1" },
	})
	if strings.Contains(redacted, secret) || !strings.Contains(redacted, "Layer changed: skill [REDACTED]") {
		t.Fatalf("layer\n%s", redacted)
	}
	if !strings.HasPrefix(redacted, "## Regressions addressed\n") {
		t.Fatal(redacted)
	}
}

func TestRunPromptEvidence(t *testing.T) {
	src, err := os.ReadFile("prompt.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		"cite-sources",
		"CONVENTION:",
		"Do not invent a required final line",
	} {
		if strings.Contains(string(src), banned) {
			t.Fatalf("prompt.go hardcodes %q", banned)
		}
	}

	const secret = "planted-secret-value"
	head := "HEADMARKER"
	tail := "TAILMARKER"
	output := head + strings.Repeat("x", exampleCap) + secret + tail
	reason := "REASONHEAD" + strings.Repeat("y", exampleCap) + "REASONTAIL"
	longRubric := "RUBRICHEAD" + strings.Repeat("z", rubricCap) + "RUBRICTAIL" + secret
	prompt := RunPrompt(
		[]Failure{{RunID: "run_1", Scorer: "review", Label: "fail", Reasoning: "short reason"}},
		"factory.yaml",
		nil,
		[]string{secret},
		Evidence{
			Rubrics: []Rubric{
				{
					Name:         "review",
					Description:  "Did the change miss the marker?",
					Body:         "kept rubric " + secret,
					PassingScore: 1,
					Labels: []RubricLabel{{
						Value:       "fail",
						Description: "missed the marker",
						Score:       0,
					}},
				},
				{Name: "other", Body: longRubric, PassingScore: 0},
			},
			Examples: []Example{
				{RunID: "run_1", Scorer: "review", Label: "fail", Reasoning: reason, Output: output},
				{RunID: "run_2", Scorer: "review", Label: "fail", Reasoning: "second reason", Output: "second out"},
				{RunID: "run_3", Scorer: "review", Label: "fail", Reasoning: "third reason", Output: "third out"},
				{RunID: "run_4", Scorer: "review", Label: "fail", Reasoning: "fourth reason", Output: "fourth out"},
			},
		},
	)
	if strings.Contains(prompt, secret) {
		t.Fatalf("prompt leaked\n%s", prompt)
	}
	for _, banned := range []string{"cite-sources", "CONVENTION:", "Do not invent a required final line"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("prompt hardcodes %q\n%s", banned, prompt)
		}
	}
	for _, dropped := range []string{"HEADMARKER", "REASONTAIL", "RUBRICTAIL", "run_4", "fourth out", "fourth reason"} {
		if strings.Contains(prompt, dropped) {
			t.Fatalf("uncapped %q\n%s", dropped, prompt)
		}
	}
	for _, want := range []string{
		"explicit, imperative instruction",
		"Remove or rewrite any existing instruction",
		"contradicts",
		"bare appended marker line",
		"- scorer review",
		"description: Did the change miss the marker?",
		"label fail (score 0): missed the marker",
		"passing score: 1",
		"kept rubric [REDACTED]",
		"RUBRICHEAD",
		"REASONHEAD",
		"TAILMARKER",
		"[REDACTED]",
		"assistant output: ",
		"run_3",
		"third out",
		"reasoning: short reason",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	if strings.Count(prompt, "assistant output:") != exampleLimit {
		t.Fatalf("example count\n%s", prompt)
	}
}

func TestRoute(t *testing.T) {
	pools := Pools{
		Admins:       []string{"ada", "bea", "cam"},
		Team:         []string{"dee", "eli"},
		LoginByEmail: map[string]string{"a@acme.example": "gus", "b@acme.example": "hal", "c@acme.example": "gus"},
	}
	fresh := func() *rand.Rand { return rand.New(rand.NewSource(42)) }
	cases := []struct {
		name    string
		cfg     definition.SelfImprovement
		changed int
		blast   int
		rng     *rand.Rand
		want    Routing
	}{
		{name: "admins", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerAdmins}, blast: 3, rng: fresh(), want: Routing{Reviewer: "cam", Pool: "admins"}},
		{name: "default type", blast: 3, rng: fresh(), want: Routing{Reviewer: "cam", Pool: "admins"}},
		{name: "team", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerTeam}, blast: 3, rng: fresh(), want: Routing{Reviewer: "eli", Pool: "team"}},
		{name: "custom", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerCustom, ReviewerEmails: []string{"a@acme.example", "missing@acme.example", "b@acme.example", "c@acme.example"}}, blast: 3, rng: fresh(), want: Routing{Reviewer: "hal", Pool: "custom"}},
		{name: "none", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerNone}, blast: 3, rng: fresh(), want: Routing{Pool: "none"}},
		{name: "blast", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerNone}, changed: 4, blast: 3, rng: fresh(), want: Routing{Reviewer: "cam", Pool: "admins", Labels: []string{"needs-human-design"}}},
		{name: "blast default", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerTeam}, changed: 4, rng: fresh(), want: Routing{Reviewer: "cam", Pool: "admins", Labels: []string{"needs-human-design"}}},
		{name: "at limit", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerTeam}, changed: 3, blast: 3, rng: fresh(), want: Routing{Reviewer: "eli", Pool: "team"}},
		{name: "nil rng", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerAdmins}, blast: 3, want: Routing{Reviewer: "ada", Pool: "admins"}},
		{name: "unknown", cfg: definition.SelfImprovement{ReviewerType: "rotating"}, blast: 3, rng: fresh(), want: Routing{Pool: "rotating"}},
		{name: "empty pool", cfg: definition.SelfImprovement{ReviewerType: definition.ReviewerTeam}, blast: 3, rng: fresh(), want: Routing{Pool: "team"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := pools
			if tc.name == "empty pool" {
				p.Team = nil
			}
			got := Route(tc.cfg, p, tc.changed, tc.blast, tc.rng)
			if got.Reviewer != tc.want.Reviewer || got.Pool != tc.want.Pool || strings.Join(got.Labels, ",") != strings.Join(tc.want.Labels, ",") {
				t.Fatalf("%+v", got)
			}
		})
	}
	if DefaultBlastLimit != 3 {
		t.Fatal(DefaultBlastLimit)
	}
}

func repeat(agent string, n int, at time.Time) []Failure {
	out := make([]Failure, n)
	for i := range out {
		out[i] = Failure{Agent: agent, At: at, RunID: "run"}
	}
	return out
}
