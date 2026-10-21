package benchmark

import (
	"fmt"
	"strings"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/protocol"
)

// Binding is one agent model or harness inside a trial.
type Binding struct {
	Model   string
	Harness *definition.Harness
}

// Trial is one configuration × task × repetition. Overrides are COMMIT_SHA
// checkouts. DefinitionRef is copied from the configuration for T63 to resolve.
type Trial struct {
	Configuration string
	Task          string
	Repetition    int
	Bindings      map[string]Binding
	Overrides     []protocol.RepositoryHeadOverride
	DefinitionRef string
}

// Expand returns configurations × tasks × repetitions in suite order, then
// task order, then repetition 1..reps. Each startingRepoRefs entry becomes a
// COMMIT_SHA override. A benchmarkRepoSubstitutions match sets CloneFrom and
// PreserveOrigin.
func Expand(suite definition.BenchmarkSuite, tasks map[string]definition.BenchmarkTask, reps int, subs []definition.BenchmarkSubstitution) ([]Trial, error) {
	if reps < 1 {
		return nil, fmt.Errorf("repetitions must be >= 1")
	}
	if len(suite.Configurations) == 0 {
		return nil, fmt.Errorf("suite has no configurations")
	}
	if len(suite.Tasks) == 0 {
		return nil, fmt.Errorf("suite has no tasks")
	}
	out := make([]Trial, 0, len(suite.Configurations)*len(suite.Tasks)*reps)
	for _, cfg := range suite.Configurations {
		bindings := map[string]Binding{}
		for _, binding := range cfg.Agents {
			name := strings.TrimSpace(binding.Agent)
			if name == "" {
				continue
			}
			bindings[name] = Binding{Model: strings.TrimSpace(binding.Model), Harness: binding.Harness}
		}
		for _, taskName := range suite.Tasks {
			task, ok := tasks[taskName]
			if !ok {
				return nil, fmt.Errorf("task %q not found", taskName)
			}
			ovs := overrides(task, subs)
			for rep := 1; rep <= reps; rep++ {
				out = append(out, Trial{
					Configuration: cfg.Name,
					Task:          taskName,
					Repetition:    rep,
					Bindings:      copyBindings(bindings),
					Overrides:     copyOverrides(ovs),
					DefinitionRef: strings.TrimSpace(cfg.DefinitionRef),
				})
			}
		}
	}
	return out, nil
}

func copyBindings(in map[string]Binding) map[string]Binding {
	out := make(map[string]Binding, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyOverrides(in []protocol.RepositoryHeadOverride) []protocol.RepositoryHeadOverride {
	if len(in) == 0 {
		return nil
	}
	out := make([]protocol.RepositoryHeadOverride, len(in))
	for i, ov := range in {
		out[i] = ov
		if ov.CloneFrom != nil {
			cp := *ov.CloneFrom
			out[i].CloneFrom = &cp
		}
	}
	return out
}

func overrides(task definition.BenchmarkTask, subs []definition.BenchmarkSubstitution) []protocol.RepositoryHeadOverride {
	out := make([]protocol.RepositoryHeadOverride, 0, len(task.StartingRepoRefs))
	for _, ref := range task.StartingRepoRefs {
		ov := protocol.RepositoryHeadOverride{
			CodeForge: codeForgeForHost(ref.Host),
			RepoOwner: ref.Owner,
			RepoName:  ref.Repo,
			Head: protocol.RepositoryHeadRef{
				Type:  protocol.RepositoryHeadTypeCommitSHA,
				Value: ref.SHA,
			},
		}
		if sub, ok := matchSubstitution(ov, subs); ok {
			ov.CloneFrom = &protocol.RepositoryIdentity{
				CodeForge: sub.Target.CodeForge,
				Owner:     sub.Target.Owner,
				Repo:      sub.Target.Name,
			}
			ov.PreserveOrigin = true
		}
		out = append(out, ov)
	}
	return out
}

func matchSubstitution(ov protocol.RepositoryHeadOverride, subs []definition.BenchmarkSubstitution) (definition.BenchmarkSubstitution, bool) {
	for _, sub := range subs {
		if sub.Source.CodeForge == ov.CodeForge && sub.Source.Owner == ov.RepoOwner && sub.Source.Name == ov.RepoName {
			return sub, true
		}
	}
	return definition.BenchmarkSubstitution{}, false
}

// codeForgeForHost maps a startingRepoRefs host onto the wire code_forge.
// github.com is GITHUB, matching docs/08 replay.
func codeForgeForHost(host string) string {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "github.com":
		return "GITHUB"
	case "gitlab.com":
		return "GITLAB"
	case "bitbucket.org":
		return "BITBUCKET"
	default:
		return strings.ToUpper(strings.TrimSpace(host))
	}
}
