package server

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/benchmark"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

// measurePrep is the assignment overlay for one run. Work runs only gain
// skills. Benchmark trials also gain the tree at the trial's definition SHA
// and that tree's repository head overrides.
type measurePrep struct {
	bound     boundAgent
	skillSpec string
	skillText string
	overrides []protocol.RepositoryHeadOverride
}

func (s *Server) prepareMeasure(ctx context.Context, run store.Run, bound boundAgent) (measurePrep, error) {
	prep := measurePrep{bound: bound}
	tree := s.snapshot().tree
	switch run.Purpose {
	case api.PurposeScorer:
		prep.bound = s.measurementFallback(s.snapshot())
		return prep, nil
	case api.PurposeBenchmark:
		overlaid, overs, err := s.benchmarkOverlay(ctx, run, prep.bound)
		if err != nil {
			return prep, err
		}
		prep.bound = overlaid
		prep.overrides = overs
		if shaTree := s.benchmarkTree(ctx, run); shaTree != nil {
			tree = shaTree
		}
	}
	spec, text := s.skillsFor(tree, prep.bound.name)
	prep.skillSpec = spec
	prep.skillText = text
	return prep, nil
}

func (s *Server) benchmarkTree(ctx context.Context, run store.Run) *definition.Tree {
	if s.store == nil {
		return nil
	}
	trial, err := s.store.BenchmarkTrialByRun(ctx, run.ID)
	if err != nil || strings.TrimSpace(trial.DefinitionSHA) == "" {
		return nil
	}
	tree, _, err := s.treeForSHA(ctx, trial.DefinitionSHA)
	if err != nil || tree == nil {
		return nil
	}
	return tree
}

func (s *Server) benchmarkOverlay(ctx context.Context, run store.Run, bound boundAgent) (boundAgent, []protocol.RepositoryHeadOverride, error) {
	if s.store == nil {
		return bound, nil, nil
	}
	trial, err := s.store.BenchmarkTrialByRun(ctx, run.ID)
	if errors.Is(err, store.ErrNotFound) {
		return bound, nil, nil
	}
	if err != nil {
		return bound, nil, err
	}
	tree := s.snapshot().tree
	if strings.TrimSpace(trial.DefinitionSHA) != "" {
		loaded, _, loadErr := s.treeForSHA(ctx, trial.DefinitionSHA)
		if loadErr != nil {
			return bound, nil, loadErr
		}
		if loaded != nil {
			tree = loaded
		}
	}
	if tree == nil {
		return bound, nil, nil
	}
	benchRun, err := s.store.GetBenchmarkRun(ctx, trial.BenchmarkRunID)
	if err != nil {
		return bound, nil, err
	}
	suite, ok := tree.Benchmarks[benchRun.Suite]
	if !ok {
		prod := s.snapshot().tree
		if prod == nil {
			return bound, nil, nil
		}
		suite, ok = prod.Benchmarks[benchRun.Suite]
		if !ok {
			return bound, nil, nil
		}
		tree = prod
	}
	tasks := tree.BenchmarkTasks[benchRun.Suite]
	reps := trial.Repetition
	if reps < 1 {
		reps = 1
	}
	expanded, err := benchmark.Expand(suite, tasks, reps, tree.Factory.BenchmarkSubs)
	if err != nil {
		return bound, nil, err
	}
	var match *benchmark.Trial
	for i := range expanded {
		row := &expanded[i]
		if row.Configuration == trial.Configuration && row.Task == trial.Task && row.Repetition == trial.Repetition {
			match = row
			break
		}
	}
	if match == nil {
		return bound, nil, nil
	}
	name := bound.name
	if name == "" {
		name = suite.Agent
	}
	if agent, ok := tree.Agents[name]; ok {
		bound.name = name
		bound.agent = agent
		if runner, ok := tree.Runners[agent.Runner]; ok {
			bound.runner = runner
		}
	}
	if binding, ok := match.Bindings[name]; ok && binding.Model != "" {
		bound.agent.Model = binding.Model
	}
	return bound, match.Overrides, nil
}

func (s *Server) skillsFor(tree *definition.Tree, agentName string) (string, string) {
	if tree == nil || agentName == "" {
		return "", ""
	}
	skills := tree.AgentSkills[agentName]
	if len(skills) == 0 {
		return "", ""
	}
	names := make([]string, 0, len(skills))
	var text strings.Builder
	for _, skill := range skills {
		if skill.Name == "" {
			continue
		}
		names = append(names, skill.Name)
		body := s.redact(harness.Redact(skill.Body, s.secretMaterial()))
		text.WriteString("## skill ")
		text.WriteString(skill.Name)
		text.WriteString("\n")
		text.WriteString(strings.TrimSpace(body))
		text.WriteString("\n")
	}
	if len(names) == 0 {
		return "", ""
	}
	sort.Strings(names)
	return strings.Join(names, ","), strings.TrimSpace(text.String())
}

func (s *Server) secretMaterial() []string {
	var out []string
	for _, value := range s.cfg.Secrets {
		if value != "" {
			out = append(out, value)
		}
	}
	if s.cfg.InferenceAPIKey != "" {
		out = append(out, s.cfg.InferenceAPIKey)
	}
	return out
}

func sortStrings(in []string) {
	sort.Strings(in)
}
