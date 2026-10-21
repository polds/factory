package benchmark

import (
	"fmt"

	"github.com/polds/factory/internal/api"
)

// DefaultMinRepetitions is the D5 floor. A lower count refuses a winner.
const DefaultMinRepetitions = 5

// BaselineConfiguration is the configuration name treated as the baseline
// when it is present. Otherwise the first cell's configuration is the baseline.
const BaselineConfiguration = "Baseline"

// Cell is one configuration × task aggregate. N is completed trials.
// Errored trials are excluded from N and kept on Errored.
type Cell struct {
	Configuration string
	Task          string
	Pass          int
	N             int
	Errored       int
}

// Report is the comparison verdict. It is the API report type.
type Report = api.BenchmarkReport

// Compare names a winner or refuses. Any cell with N < minReps refuses, and
// the reason names that cell and its n. Errored trials are not part of N.
// Otherwise the D5 rule applies: a candidate wins when its 95% Wilson lower
// bound is strictly above the baseline's upper bound. Pass and N are summed
// across tasks for that comparison. minReps <= 0 uses DefaultMinRepetitions.
func Compare(cells []Cell, minReps int) Report {
	if minReps <= 0 {
		minReps = DefaultMinRepetitions
	}
	for _, cell := range cells {
		if cell.N < minReps {
			return Report{Reason: fmt.Sprintf("configuration %q task %q has %d repetitions; need %d", cell.Configuration, cell.Task, cell.N, minReps)}
		}
	}
	type agg struct {
		pass int
		n    int
	}
	var order []string
	by := map[string]*agg{}
	for _, cell := range cells {
		a, ok := by[cell.Configuration]
		if !ok {
			a = &agg{}
			by[cell.Configuration] = a
			order = append(order, cell.Configuration)
		}
		a.pass += cell.Pass
		a.n += cell.N
	}
	if len(order) == 0 {
		return Report{Reason: "no cells"}
	}
	baseName := order[0]
	for _, name := range order {
		if name == BaselineConfiguration {
			baseName = name
			break
		}
	}
	base := by[baseName]
	_, baseHi := Wilson(base.pass, base.n)
	type cand struct {
		name string
		lo   float64
	}
	var winners []cand
	for _, name := range order {
		if name == baseName {
			continue
		}
		lo, _ := Wilson(by[name].pass, by[name].n)
		if lo > baseHi {
			winners = append(winners, cand{name: name, lo: lo})
		}
	}
	if len(winners) == 0 {
		return Report{Reason: fmt.Sprintf("no configuration has a Wilson 95%% lower bound above baseline %s upper bound %.4f", baseName, baseHi)}
	}
	best := winners[0]
	for _, w := range winners[1:] {
		if w.lo > best.lo || (w.lo == best.lo && w.name < best.name) {
			best = w
		}
	}
	return Report{Winner: best.name, Reason: fmt.Sprintf("%s Wilson 95%% lower bound %.4f is above baseline %s upper bound %.4f", best.name, best.lo, baseName, baseHi)}
}
