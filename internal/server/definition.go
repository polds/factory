package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
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
	// dir is the tree on disk. Boot, git-sync, and a successful upload all
	// use DefinitionPath. A failed upload may still reference its temp dir.
	dir string
}

func (s *Server) applyBoot(ctx context.Context) error {
	if s.apiBacked() {
		if _, err := os.Stat(filepath.Join(s.cfg.DefinitionPath, "factory.yaml")); err != nil {
			if os.IsNotExist(err) {
				s.setAPIServed(readAPIGeneration(s.cfg.DefinitionPath), nil)
				return nil
			}
		}
	}
	if err := s.publishDirectory(ctx, s.cfg.DefinitionPath); err != nil {
		return err
	}
	if s.apiBacked() {
		files, err := definitionFiles(s.cfg.DefinitionPath)
		if err != nil {
			return err
		}
		s.setAPIServed(readAPIGeneration(s.cfg.DefinitionPath), files)
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
	return s.applyTreeDir(ctx, tree, s.cfg.DefinitionPath)
}

func (s *Server) applyTreeDir(ctx context.Context, tree *definition.Tree, dir string) (string, error) {
	if err := s.refusePatSecondFactory(ctx, tree.Factory.Name); err != nil {
		return "", err
	}
	if err := s.applyDefaultRates(tree); err != nil {
		return "", err
	}
	prev := s.snapshot()
	applied, err := s.appliedDefinition(tree)
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
		dir:     dir,
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
	if prev.dir != "" && prev.dir != dir && prev.dir != s.cfg.DefinitionPath {
		_ = os.RemoveAll(prev.dir)
	}
	// Schedules and webhook registrations follow the published tree, before
	// the dispatcher claims a delivery that names an agent.
	if err := s.syncDefinition(ctx, tree); err != nil {
		return "", err
	}
	if err := s.syncFactoryScope(ctx, fac.ID, tree, dir); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) refusePatSecondFactory(ctx context.Context, name string) error {
	mode := strings.ToLower(strings.TrimSpace(s.cfg.GitHubTokenMode))
	if mode == "" {
		mode = "pat"
	}
	if mode != "pat" || s.store == nil {
		return nil
	}
	n, err := s.store.CountFactories(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err = s.store.GetFactory(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("server: FACTORY_GITHUB_TOKEN_MODE=pat refuses a second factory")
	}
	return err
}

func (s *Server) applyDefaultRates(tree *definition.Tree) error {
	if tree == nil || strings.TrimSpace(s.cfg.DefaultRates) == "" {
		return nil
	}
	if tree.Factory.Inference != nil && tree.Factory.Inference.Rates != nil {
		return nil
	}
	parsed, err := harness.ParseSpendPrice(s.cfg.DefaultRates)
	if err != nil {
		return err
	}
	rates := &definition.InferenceRates{
		PromptByteCap: parsed.PromptByteCap,
		MaxTokens:     parsed.MaxTokens,
	}
	for _, model := range parsed.Models {
		rates.Models = append(rates.Models, definition.ModelRate{
			Name: model.Name, InputCostPerToken: model.InputCostPerToken,
			OutputCostPerToken:          model.OutputCostPerToken,
			CacheCreationInputTokenCost: model.CacheCreationInputTokenCost,
			InputAbove:                  model.InputAbove, OutputAbove: model.OutputAbove,
		})
	}
	if tree.Factory.Inference == nil {
		tree.Factory.Inference = &definition.Inference{}
	}
	tree.Factory.Inference.Rates = rates
	return nil
}

func (s *Server) syncFactoryScope(ctx context.Context, factoryID string, tree *definition.Tree, dir string) error {
	if s.store == nil || tree == nil {
		return nil
	}
	if tree.Factory.Budget != nil && tree.Factory.Budget.CeilingCents > 0 {
		if err := s.store.UpsertFactoryBudget(ctx, factoryID, "", tree.Factory.Budget.CeilingCents); err != nil {
			return err
		}
	}
	for _, repo := range tree.Factory.Repositories {
		if repo.CeilingCents <= 0 || repo.Owner == "" || repo.Name == "" {
			continue
		}
		if err := s.store.UpsertFactoryBudget(ctx, factoryID, repo.Owner+"/"+repo.Name, repo.CeilingCents); err != nil {
			return err
		}
	}
	if _, _, err := s.store.EnsureFactoryKey(ctx, factoryID); err != nil {
		return err
	}
	if dir == s.cfg.DefinitionPath {
		s.bindBootstrapSecrets(factoryID)
	}
	return nil
}

func (s *Server) bindBootstrapSecrets(factoryID string) {
	if factoryID == "" || len(s.cfg.BootstrapSecrets) == 0 {
		return
	}
	if s.cfg.FactorySecrets == nil {
		s.cfg.FactorySecrets = map[string]map[string]string{}
	}
	grants := s.cfg.FactorySecrets[factoryID]
	if grants == nil {
		grants = map[string]string{}
	}
	for name, value := range s.cfg.BootstrapSecrets {
		if _, exists := grants[name]; !exists {
			grants[name] = value
		}
	}
	s.cfg.FactorySecrets[factoryID] = grants
}

func (s *Server) snapshot() loaded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded
}

func (s *Server) appliedDefinition(tree *definition.Tree) (store.AppliedDefinition, error) {
	var rates any
	if tree.Factory.Inference != nil {
		rates = tree.Factory.Inference.Rates
	}
	spec, err := json.Marshal(map[string]any{
		"name":         tree.Factory.Name,
		"secrets":      tree.Factory.Secrets,
		"digest":       tree.Digest,
		"repositories": tree.Factory.Repositories,
		"publish":      tree.Factory.Publish,
		"evidence":     tree.Factory.Evidence,
		"budget":       tree.Factory.Budget,
		"rates":        rates,
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
	source := api.DefinitionLocal
	var gitSHA string
	if strings.TrimSpace(s.cfg.DefinitionGitURL) != "" {
		source = api.DefinitionGit
		s.mu.Lock()
		gitSHA = s.gitSHA
		s.mu.Unlock()
	}
	autoNames := make([]string, 0, len(tree.Automations))
	for name := range tree.Automations {
		autoNames = append(autoNames, name)
	}
	sort.Strings(autoNames)
	automations := make([]store.DefinedAutomation, 0, len(autoNames))
	for _, name := range autoNames {
		auto := tree.Automations[name]
		triggers, err := json.Marshal(auto.Triggers)
		if err != nil {
			return store.AppliedDefinition{}, err
		}
		if auto.Triggers == nil {
			triggers = []byte(`[]`)
		}
		overrides, err := automationOverrides(auto)
		if err != nil {
			return store.AppliedDefinition{}, err
		}
		automations = append(automations, store.DefinedAutomation{
			Name:          name,
			Enabled:       auto.Enabled,
			TriggersJSON:  triggers,
			Prompt:        auto.Body,
			OverridesJSON: overrides,
		})
	}
	return store.AppliedDefinition{
		SchemaVersion:    tree.Factory.SchemaVersion,
		Alias:            tree.Factory.Alias,
		DefinitionSource: source,
		GitSHA:           gitSHA,
		SpecJSON:         spec,
		Agents:           agents,
		Runners:          runners,
		Automations:      automations,
	}, nil
}

// automationOverrides is the execution settings stored on
// automations.overrides_json. Secrets are names. Values are not on the
// definition type and are not written here.
func automationOverrides(auto definition.Automation) (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		Model              string   `json:"model,omitempty"`
		Runner             string   `json:"runner,omitempty"`
		Pool               string   `json:"pool,omitempty"`
		Secrets            []string `json:"secrets,omitempty"`
		IdleTimeoutMinutes int      `json:"idleTimeoutMinutes,omitempty"`
	}{
		Model:              auto.Model,
		Runner:             auto.Runner,
		Pool:               auto.Pool,
		Secrets:            auto.Secrets,
		IdleTimeoutMinutes: auto.IdleTimeoutMinutes,
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
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
