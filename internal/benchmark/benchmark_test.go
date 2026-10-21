package benchmark

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/protocol"
)

func TestWilsonGolden(t *testing.T) {
	cases := []struct {
		pass, n int
		lo, hi  float64
	}{
		{0, 0, 0, 1},
		{0, 1, 0, 0.793450685623},
		{1, 1, 0.206549314377, 1},
		{0, 5, 0, 0.434482464783},
		{2, 5, 0.117620774233, 0.769275718724},
		{5, 5, 0.565517535217, 1},
		{0, 20, 0, 0.161125158053},
		{10, 20, 0.299298008198, 0.700701991802},
		{20, 20, 0.838874841947, 1},
		{0, 100, 0, 0.036993498207},
		{50, 100, 0.403831530366, 0.596168469634},
		{100, 100, 0.963006501793, 1},
	}
	for _, tc := range cases {
		lo, hi := Wilson(tc.pass, tc.n)
		if math.Abs(lo-tc.lo) > 1e-12 || math.Abs(hi-tc.hi) > 1e-12 {
			t.Errorf("n=%d pass=%d got %.12f %.12f", tc.n, tc.pass, lo, hi)
		}
	}
	lo, hi := Wilson(-1, 5)
	if lo != 0 || math.Abs(hi-0.434482464783) > 1e-12 {
		t.Fatalf("clamp %.12f %.12f", lo, hi)
	}
	lo, hi = Wilson(9, 5)
	if math.Abs(lo-0.565517535217) > 1e-12 || hi != 1 {
		t.Fatalf("clamp high %.12f %.12f", lo, hi)
	}
}

func TestCompare(t *testing.T) {
	refuse := Compare([]Cell{{Configuration: "Candidate", Task: "broken-doc-link", Pass: 4, N: 4}}, 0)
	if refuse.Winner != "" || !strings.Contains(refuse.Reason, `has 4 repetitions`) || !strings.Contains(refuse.Reason, "need 5") || !strings.Contains(refuse.Reason, "broken-doc-link") {
		t.Fatalf("%+v", refuse)
	}
	first := Compare([]Cell{
		{Configuration: "Baseline", Task: "a", N: 4, Pass: 4},
		{Configuration: "Candidate", Task: "a", N: 1, Pass: 0},
	}, 5)
	if first.Winner != "" || !strings.Contains(first.Reason, `"Baseline"`) || !strings.Contains(first.Reason, "has 4") {
		t.Fatalf("%+v", first)
	}
	win := Compare([]Cell{
		{Configuration: "Baseline", Task: "broken-doc-link", Pass: 0, N: 5, Errored: 3},
		{Configuration: "Candidate", Task: "broken-doc-link", Pass: 5, N: 5, Errored: 9},
	}, 5)
	if win.Winner != "Candidate" || !strings.Contains(win.Reason, "0.5655") || !strings.Contains(win.Reason, "0.4345") {
		t.Fatalf("%+v", win)
	}
	tie := Compare([]Cell{
		{Configuration: "Baseline", Task: "a", Pass: 0, N: 5},
		{Configuration: "m", Task: "a", Pass: 5, N: 5},
		{Configuration: "a", Task: "a", Pass: 5, N: 5},
	}, 5)
	if tie.Winner != "a" {
		t.Fatalf("tie %+v", tie)
	}
	none := Compare([]Cell{
		{Configuration: "Baseline", Task: "a", Pass: 5, N: 5},
		{Configuration: "Candidate", Task: "a", Pass: 5, N: 5},
	}, 5)
	if none.Winner != "" || !strings.Contains(none.Reason, "Baseline") {
		t.Fatalf("%+v", none)
	}
	if got := Compare(nil, 5); got.Winner != "" || got.Reason != "no cells" {
		t.Fatalf("%+v", got)
	}
	// One perfect trial does not clear a total miss. The count floor is not the only gate.
	close := Compare([]Cell{
		{Configuration: "Baseline", Task: "a", Pass: 0, N: 1},
		{Configuration: "Candidate", Task: "a", Pass: 1, N: 1},
	}, 1)
	if close.Winner != "" {
		t.Fatalf("n=1 winner %+v", close)
	}
	agg := Compare([]Cell{
		{Configuration: "Baseline", Task: "a", Pass: 0, N: 5},
		{Configuration: "Baseline", Task: "b", Pass: 0, N: 5},
		{Configuration: "Candidate", Task: "a", Pass: 5, N: 5},
		{Configuration: "Candidate", Task: "b", Pass: 5, N: 5},
	}, 5)
	if agg.Winner != "Candidate" {
		t.Fatalf("aggregate %+v", agg)
	}
	fallback := Compare([]Cell{
		{Configuration: "Old", Task: "a", Pass: 0, N: 5},
		{Configuration: "New", Task: "a", Pass: 5, N: 5},
	}, 5)
	if fallback.Winner != "New" || !strings.Contains(fallback.Reason, "Old") {
		t.Fatalf("fallback baseline %+v", fallback)
	}
}

func TestExpand(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	suite := definition.BenchmarkSuite{
		Name:  "Pull request review",
		Agent: "reviewer",
		Configurations: []definition.BenchmarkConfiguration{
			{Name: "Baseline", Agents: []definition.BenchmarkBinding{{Agent: "reviewer", Model: "auto"}}},
			{Name: "Candidate", DefinitionRef: " benchmarks/pr-review/candidate ", Agents: []definition.BenchmarkBinding{{Agent: "reviewer", Model: "claude-5-opus-high"}}},
		},
		Tasks: []string{"broken-doc-link", "typo"},
	}
	tasks := map[string]definition.BenchmarkTask{
		"broken-doc-link": {
			Name: "broken-doc-link",
			StartingRepoRefs: []definition.RepoRef{
				{Host: "github.com", Owner: "acme", Repo: "payments-service", SHA: sha},
				{Host: "gitlab.com", Owner: "acme", Repo: "other", SHA: sha},
			},
		},
		"typo": {Name: "typo"},
	}
	subs := []definition.BenchmarkSubstitution{{
		Source: definition.Repository{CodeForge: "GITHUB", Owner: "acme", Name: "payments-service"},
		Target: definition.Repository{CodeForge: "GITHUB", Owner: "acme-bench", Name: "payments-service"},
	}}
	trials, err := Expand(suite, tasks, 3, subs)
	if err != nil {
		t.Fatal(err)
	}
	if len(trials) != 2*2*3 {
		t.Fatalf("len %d", len(trials))
	}
	if trials[0].Configuration != "Baseline" || trials[0].Task != "broken-doc-link" || trials[0].Repetition != 1 {
		t.Fatalf("first %+v", trials[0])
	}
	if trials[len(trials)-1].Configuration != "Candidate" || trials[len(trials)-1].Task != "typo" || trials[len(trials)-1].Repetition != 3 {
		t.Fatalf("last %+v", trials[len(trials)-1])
	}
	if trials[0].DefinitionRef != "" || trials[6].DefinitionRef != "benchmarks/pr-review/candidate" {
		t.Fatalf("refs %q %q", trials[0].DefinitionRef, trials[6].DefinitionRef)
	}
	if trials[0].Bindings["reviewer"].Model != "auto" || trials[6].Bindings["reviewer"].Model != "claude-5-opus-high" {
		t.Fatalf("bindings %+v %+v", trials[0].Bindings, trials[6].Bindings)
	}
	with := trials[0].Overrides
	if len(with) != 2 || with[1].CodeForge != "GITLAB" || with[1].CloneFrom != nil || with[1].PreserveOrigin {
		t.Fatalf("second override %+v", with[1])
	}
	got, err := json.Marshal(with[0])
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"code_forge":"GITHUB","repo_owner":"acme","repo_name":"payments-service","head":{"type":"COMMIT_SHA","value":"0123456789abcdef0123456789abcdef01234567"},"clone_from":{"code_forge":"GITHUB","owner":"acme-bench","repo":"payments-service"},"preserve_origin":true}`
	if string(got) != want {
		t.Fatalf("override %s", got)
	}
	trials[0].Overrides[0].Head.Value = "changed"
	trials[0].Bindings["reviewer"] = Binding{Model: "mutated"}
	if trials[1].Overrides[0].Head.Value == "changed" || trials[1].Bindings["reviewer"].Model == "mutated" {
		t.Fatal("repetitions share bindings or overrides")
	}
	plain, err := Expand(suite, tasks, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plain[0].Overrides[0])
	if err != nil {
		t.Fatal(err)
	}
	const bare = `{"code_forge":"GITHUB","repo_owner":"acme","repo_name":"payments-service","head":{"type":"COMMIT_SHA","value":"0123456789abcdef0123456789abcdef01234567"}}`
	if string(raw) != bare {
		t.Fatalf("bare %s", raw)
	}
	if plain[0].Overrides[0].Head.Type != protocol.RepositoryHeadTypeCommitSHA {
		t.Fatal(plain[0].Overrides[0].Head.Type)
	}
	if _, err := Expand(suite, tasks, 0, nil); err == nil {
		t.Fatal("reps 0")
	}
	if _, err := Expand(suite, map[string]definition.BenchmarkTask{}, 1, nil); err == nil {
		t.Fatal("missing task")
	}
	host := codeForgeForHost("bitbucket.org")
	if host != "BITBUCKET" || codeForgeForHost("Example.COM") != "EXAMPLE.COM" {
		t.Fatalf("hosts %s", host)
	}
}
