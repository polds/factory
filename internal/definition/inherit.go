package definition

import "strings"

// Inherit resolves a sparse agent against factory defaults.
// An omitted list or map inherits; one set on the agent replaces the default.
// Factory-wide secrets and MCP servers stay on Factory. EffectiveSecrets and
// EffectiveMCPServers union them with the resolved agent.
func Inherit(factory Factory, defaults AgentDefaults, agent Agent) Agent {
	out := agent
	modelSet := strings.TrimSpace(out.Model) != ""
	harnessSet := out.Harness != nil
	switch {
	case modelSet && harnessSet:
	case modelSet:
		out.Harness = nil
	case harnessSet:
		out.Model = ""
	default:
		out.Model = strings.TrimSpace(defaults.Model)
		out.Harness = cloneHarness(defaults.Harness)
	}
	if strings.TrimSpace(out.Runner) == "" {
		out.Runner = strings.TrimSpace(defaults.Runner)
	}
	if out.Pool == "" {
		out.Pool = defaults.Pool
	}
	if out.ComputerUseModel == "" {
		out.ComputerUseModel = defaults.ComputerUseModel
	}
	if out.CredentialStrategy == "" {
		out.CredentialStrategy = defaults.CredentialStrategy
	}
	if out.IdleTimeoutMinutes == 0 {
		out.IdleTimeoutMinutes = defaults.IdleTimeoutMinutes
	}
	if out.MaxTurns == 0 {
		out.MaxTurns = defaults.MaxTurns
	}
	if out.WorkerHost == "" {
		out.WorkerHost = defaults.WorkerHost
	}
	if out.Secrets == nil {
		out.Secrets = cloneStrings(defaults.Secrets)
	} else {
		out.Secrets = cloneStrings(out.Secrets)
	}
	if out.MCPServers == nil {
		out.MCPServers = cloneMCP(defaults.MCPServers)
	} else {
		out.MCPServers = cloneMCP(out.MCPServers)
	}
	out.RequiredRepos = cloneRepos(out.RequiredRepos)
	out.SpawnableBy = cloneStrings(out.SpawnableBy)
	return out
}

// CanonicalModel reports the model identity used for equality.
// model: auto, harness warp-agent/auto, and harness oz/auto are one identity.
func CanonicalModel(agent Agent) string {
	if agent.Harness != nil {
		typ := strings.TrimSpace(agent.Harness.Type)
		model := strings.TrimSpace(agent.Harness.Model)
		if model == "auto" && (typ == "" || typ == "oz" || typ == "warp-agent") {
			return "warp-agent/auto"
		}
		return typ + "/" + model
	}
	model := strings.TrimSpace(agent.Model)
	if model == "auto" {
		return "warp-agent/auto"
	}
	return model
}

// EffectiveSecrets is factory secrets plus the resolved agent secrets.
// Factory names come first. The agent list does not include agentDefaults
// secrets when the agent set its own list.
func EffectiveSecrets(factory Factory, agent Agent) []string {
	return unionNames(factory.Secrets, agent.Secrets)
}

// EffectiveMCPServers is the resolved agent map with factory servers applied
// in addition. On a name collision the factory server is kept.
func EffectiveMCPServers(factory Factory, agent Agent) map[string]MCPServer {
	out := cloneMCP(agent.MCPServers)
	if out == nil {
		out = map[string]MCPServer{}
	}
	for name, server := range factory.MCPServers {
		out[name] = cloneMCPServer(server)
	}
	return out
}

// InheritAutomation resolves an automation onto its target agent.
// required_repos extends the agent list. Other lists and maps replace.
// Omitted execution settings inherit from the agent.
func InheritAutomation(agent Agent, auto Automation) Automation {
	out := auto
	modelSet := strings.TrimSpace(out.Model) != ""
	harnessSet := out.Harness != nil
	switch {
	case modelSet && harnessSet:
	case modelSet:
		out.Harness = nil
	case harnessSet:
		out.Model = ""
	default:
		out.Model = agent.Model
		out.Harness = cloneHarness(agent.Harness)
	}
	if strings.TrimSpace(out.Runner) == "" {
		out.Runner = agent.Runner
	}
	if out.Pool == "" {
		out.Pool = agent.Pool
	}
	if out.ComputerUseModel == "" {
		out.ComputerUseModel = agent.ComputerUseModel
	}
	if out.CredentialStrategy == "" {
		out.CredentialStrategy = agent.CredentialStrategy
	}
	if out.IdleTimeoutMinutes == 0 {
		out.IdleTimeoutMinutes = agent.IdleTimeoutMinutes
	}
	if out.MaxTurns == 0 {
		out.MaxTurns = agent.MaxTurns
	}
	if out.WorkerHost == "" {
		out.WorkerHost = agent.WorkerHost
	}
	if out.Secrets == nil {
		out.Secrets = cloneStrings(agent.Secrets)
	} else {
		out.Secrets = cloneStrings(out.Secrets)
	}
	if out.MCPServers == nil {
		out.MCPServers = cloneMCP(agent.MCPServers)
	} else {
		out.MCPServers = cloneMCP(out.MCPServers)
	}
	out.RequiredRepos = append(cloneRepos(agent.RequiredRepos), cloneRepos(auto.RequiredRepos)...)
	return out
}

func unionNames(lists ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, list := range lists {
		for _, name := range list {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func cloneRepos(in []Repository) []Repository {
	if in == nil {
		return nil
	}
	return append([]Repository{}, in...)
}

func cloneMCPServer(in MCPServer) MCPServer {
	if in.Headers != nil {
		in.Headers = cloneStringMap(in.Headers)
	}
	if in.Env != nil {
		in.Env = cloneStringMap(in.Env)
	}
	if in.Args != nil {
		in.Args = cloneStrings(in.Args)
	}
	return in
}
