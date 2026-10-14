package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

type boundAgent struct {
	name   string
	agent  definition.Agent
	runner definition.Runner
}

type loaded struct {
	tree    *definition.Tree
	factory store.Factory
	byName  map[string]string
	byID    map[string]boundAgent
}

func (s *Server) applyBoot(ctx context.Context) error {
	tree, diags, err := loadTree(s.cfg.DefinitionPath)
	if err != nil {
		return err
	}
	if msgs := errorTexts(diags); len(msgs) > 0 {
		return fmt.Errorf("server: definition: %s", strings.Join(msgs, "; "))
	}
	if _, err := s.applyTree(ctx, tree); err != nil {
		return err
	}
	return nil
}

func loadTree(path string) (*definition.Tree, []definition.Diagnostic, error) {
	tree, diags := definition.LoadDir(path)
	if tree == nil {
		if len(diags) == 0 {
			return nil, nil, fmt.Errorf("server: definition %s: unreadable", path)
		}
		return nil, diags, fmt.Errorf("server: definition: %s", strings.Join(errorTexts(diags), "; "))
	}
	return tree, diags, nil
}

func (s *Server) applyTree(ctx context.Context, tree *definition.Tree) (string, error) {
	applied, err := appliedDefinition(tree)
	if err != nil {
		return "", err
	}
	id, err := s.store.ApplyDefinition(ctx, tree.Factory.Name, applied)
	if err != nil {
		return "", err
	}
	fac, err := s.store.GetFactory(ctx, tree.Factory.Name)
	if err != nil {
		return "", err
	}
	agents, err := s.store.ListAgents(ctx, fac.ID)
	if err != nil {
		return "", err
	}
	next := loaded{
		tree:    tree,
		factory: fac,
		byName:  make(map[string]string, len(agents)),
		byID:    make(map[string]boundAgent, len(agents)),
	}
	for _, agent := range agents {
		spec, ok := tree.Agents[agent.Name]
		if !ok {
			continue
		}
		next.byName[agent.Name] = agent.ID
		next.byID[agent.ID] = boundAgent{name: agent.Name, agent: spec, runner: tree.Runners[spec.Runner]}
	}
	s.mu.Lock()
	s.loaded = next
	s.mu.Unlock()
	return id, nil
}

func (s *Server) snapshot() loaded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded
}

func appliedDefinition(tree *definition.Tree) (store.AppliedDefinition, error) {
	spec, err := json.Marshal(map[string]any{
		"name":    tree.Factory.Name,
		"secrets": tree.Factory.Secrets,
		"digest":  tree.Digest,
	})
	if err != nil {
		return store.AppliedDefinition{}, err
	}
	names := make([]string, 0, len(tree.Runners))
	for name := range tree.Runners {
		names = append(names, name)
	}
	sort.Strings(names)
	runners := make([]store.DefinedRunner, 0, len(names))
	for _, name := range names {
		runner := tree.Runners[name]
		platform, err := json.Marshal(map[string]any{
			"os":          runner.OS,
			"arch":        runner.Arch,
			"dockerImage": runner.DockerImage,
		})
		if err != nil {
			return store.AppliedDefinition{}, err
		}
		shape, err := json.Marshal(map[string]any{
			"vcpus":    runner.VCPUs,
			"memoryGb": runner.MemoryGB,
		})
		if err != nil {
			return store.AppliedDefinition{}, err
		}
		setup := runner.SetupCommands
		if setup == nil {
			setup = []string{}
		}
		setupJSON, err := json.Marshal(setup)
		if err != nil {
			return store.AppliedDefinition{}, err
		}
		runners = append(runners, store.DefinedRunner{
			Name:              name,
			PlatformJSON:      platform,
			InstanceShapeJSON: shape,
			SetupCommands:     setupJSON,
		})
	}
	agentNames := make([]string, 0, len(tree.Agents))
	for name := range tree.Agents {
		agentNames = append(agentNames, name)
	}
	sort.Strings(agentNames)
	agents := make([]store.DefinedAgent, 0, len(agentNames))
	for _, name := range agentNames {
		agent := tree.Agents[name]
		agentSpec, err := json.Marshal(map[string]any{
			"model":    agent.Model,
			"runner":   agent.Runner,
			"pool":     agent.Pool,
			"secrets":  agent.Secrets,
			"maxTurns": agent.MaxTurns,
		})
		if err != nil {
			return store.AppliedDefinition{}, err
		}
		agents = append(agents, store.DefinedAgent{
			Name:      name,
			AgentType: agent.AgentType,
			Runner:    agent.Runner,
			SpecJSON:  agentSpec,
		})
	}
	return store.AppliedDefinition{
		SchemaVersion:    tree.Factory.SchemaVersion,
		Alias:            tree.Factory.Alias,
		DefinitionSource: "local",
		GitSHA:           tree.Digest,
		SpecJSON:         spec,
		Agents:           agents,
		Runners:          runners,
	}, nil
}

func errorTexts(diags []definition.Diagnostic) []string {
	var out []string
	for _, diag := range diags {
		if diag.Severity != definition.SeverityError {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d: %s", diag.File, diag.Line, diag.Message))
	}
	return out
}

func toDiagnostics(diags []definition.Diagnostic) []api.Diagnostic {
	out := make([]api.Diagnostic, 0, len(diags))
	for _, diag := range diags {
		out = append(out, api.Diagnostic{File: diag.File, Line: diag.Line, Severity: diag.Severity, Message: diag.Message})
	}
	return out
}
