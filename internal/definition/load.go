package definition

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	syaml "sigs.k8s.io/yaml"
)

// LoadDir parses and validates a local factory definition directory.
// The returned tree is non-nil when the directory can be read, including
// when diagnostics are present. Severity is "error" or "warning".
func LoadDir(path string) (*Tree, []Diagnostic) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		msg := "not a directory"
		if err != nil {
			msg = err.Error()
		}
		return nil, []Diagnostic{{
			File:     filepath.ToSlash(path),
			Line:     1,
			Severity: SeverityError,
			Message:  msg,
		}}
	}

	sum, err := hashTree(path)
	if err != nil {
		return nil, []Diagnostic{{
			File:     "factory.yaml",
			Line:     1,
			Severity: SeverityError,
			Message:  err.Error(),
		}}
	}
	tree := &Tree{
		Agents:      map[string]Agent{},
		Runners:     map[string]Runner{},
		Routers:     map[string]Router{},
		Skills:      map[string]Skill{},
		AgentSkills: map[string][]Skill{},
		Automations: map[string]Automation{},
		Scorers:     map[string]Scorer{},
		Webhooks:    map[string]Webhook{},
		Digest:      sum,
	}
	var diags []Diagnostic

	factoryRaw, factoryLines := loadFactory(path, tree, &diags)
	loadRunners(path, tree, &diags)
	if factoryRaw != nil {
		checkRunnerRef("factory.yaml", tree.Factory.AgentDefaults.Runner, "agentDefaults.runner", factoryLines, tree.Runners, &diags)
		scanMCPCredentials("factory.yaml", "agentDefaults", childMap(factoryRaw, "agentDefaults"), factoryLines, &diags)
	}
	loadAgents(path, tree, &diags)
	loadRouters(path, tree, &diags)
	loadSkills(path, tree, &diags)
	loadAgentSkills(path, tree, &diags)
	loadAutomations(path, tree, &diags)
	loadScorers(path, tree, &diags)
	loadWebhooks(path, tree, &diags)
	validatePipeline(tree, factoryLines, &diags)
	validateScorerDefaults(tree, factoryLines, &diags)
	diags = append(diags, CheckAutomations(tree.Automations, nil)...)
	sortDiags(diags)
	return tree, diags
}

func loadFactory(root string, tree *Tree, diags *[]Diagnostic) (map[string]any, map[string]int) {
	const file = "factory.yaml"
	body, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		addDiag(diags, file, 1, SeverityError, "file not found")
		return nil, nil
	}
	raw, err := unmarshalMap(body)
	if err != nil {
		addDiag(diags, file, 1, SeverityError, "invalid YAML: "+err.Error())
		return nil, nil
	}
	lines := indexKeys(string(body))
	walkKeys(file, raw, factorySchema, "", lines, diags)
	checkSchemaVersion(file, raw, lines, diags)
	checkName(file, raw, lines, diags)
	checkExclusive(file, "agentDefaults", raw, lines, diags)
	scanMCPCredentials(file, "", raw, lines, diags)
	if err := syaml.Unmarshal(body, &tree.Factory); err != nil {
		addDiag(diags, file, 1, SeverityError, "invalid YAML: "+err.Error())
	}
	return raw, lines
}

func loadRunners(root string, tree *Tree, diags *[]Diagnostic) {
	dir := filepath.Join(root, "runners")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		addDiag(diags, "runners", 1, SeverityError, err.Error())
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("runners", e.Name()))
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			addDiag(diags, rel, 1, SeverityError, err.Error())
			continue
		}
		raw, err := unmarshalMap(body)
		if err != nil {
			addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
			continue
		}
		lines := indexKeys(string(body))
		walkKeys(rel, raw, runnerSchema, "", lines, diags)
		var doc runnerDoc
		if err := syaml.Unmarshal(body, &doc); err != nil {
			addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		tree.Runners[name] = doc.runner(name)
	}
}

type runnerDoc struct {
	Description   string   `json:"description,omitempty"`
	SetupCommands []string `json:"setupCommands,omitempty"`
	InstanceShape *struct {
		VCPUs    int `json:"vcpus,omitempty"`
		MemoryGB int `json:"memoryGb,omitempty"`
	} `json:"instanceShape,omitempty"`
	Platform *struct {
		OS    string `json:"os,omitempty"`
		Arch  string `json:"arch,omitempty"`
		Linux *struct {
			DockerImage                  string `json:"dockerImage,omitempty"`
			RegistryCredentialSecretName string `json:"registryCredentialSecretName,omitempty"`
		} `json:"linux,omitempty"`
	} `json:"platform,omitempty"`
	FailureSessionRetentionMinutes int `json:"failureSessionRetentionMinutes,omitempty"`
}

func (d runnerDoc) runner(name string) Runner {
	r := Runner{
		Name:                           name,
		Description:                    d.Description,
		SetupCommands:                  d.SetupCommands,
		FailureSessionRetentionMinutes: d.FailureSessionRetentionMinutes,
	}
	if d.InstanceShape != nil {
		r.VCPUs = d.InstanceShape.VCPUs
		r.MemoryGB = d.InstanceShape.MemoryGB
	}
	if d.Platform != nil {
		r.OS = d.Platform.OS
		r.Arch = d.Platform.Arch
		if d.Platform.Linux != nil {
			r.DockerImage = d.Platform.Linux.DockerImage
			r.RegistryCredentialSecretName = d.Platform.Linux.RegistryCredentialSecretName
		}
	}
	return r
}

type agentDoc struct {
	Description        string               `json:"description,omitempty"`
	AgentType          string               `json:"agentType,omitempty"`
	Model              string               `json:"model,omitempty"`
	Harness            *Harness             `json:"harness,omitempty"`
	SpawnableBy        []string             `json:"spawnableBy,omitempty"`
	CredentialStrategy string               `json:"credentialStrategy,omitempty"`
	IdleTimeoutMinutes int                  `json:"idleTimeoutMinutes,omitempty"`
	RequiredRepos      []Repository         `json:"required_repos,omitempty"`
	Secrets            []string             `json:"secrets,omitempty"`
	MCPServers         map[string]MCPServer `json:"mcpServers,omitempty"`
	Runner             string               `json:"runner,omitempty"`
	Pool               string               `json:"pool,omitempty"`
	ComputerUseModel   string               `json:"computerUseModel,omitempty"`
	MaxTurns           int                  `json:"maxTurns,omitempty"`
	WorkerHost         string               `json:"workerHost,omitempty"`
}

type parsedAgent struct {
	slug  string
	file  string
	body  string
	raw   map[string]any
	doc   agentDoc
	lines map[string]int
}

func loadAgents(root string, tree *Tree, diags *[]Diagnostic) {
	dir := filepath.Join(root, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		addDiag(diags, "agents", 1, SeverityError, err.Error())
		return
	}
	var parsed []parsedAgent
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("agents", e.Name(), "agent.md"))
		body, err := os.ReadFile(filepath.Join(dir, e.Name(), "agent.md"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			addDiag(diags, rel, 1, SeverityError, err.Error())
			continue
		}
		fm, markdown, ok, opened := splitFrontmatter(string(body))
		if !ok {
			msg := "missing YAML frontmatter"
			if opened {
				msg = "unclosed YAML frontmatter"
			}
			addDiag(diags, rel, 1, SeverityError, msg)
			continue
		}
		raw, err := unmarshalMap([]byte(fm))
		if err != nil {
			addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
			continue
		}
		lines := indexKeys(fm)
		shiftLines(lines, 1)
		walkKeys(rel, raw, agentSchema, "", lines, diags)
		checkExclusive(rel, "", raw, lines, diags)
		scanMCPCredentials(rel, "", raw, lines, diags)
		var doc agentDoc
		if err := syaml.Unmarshal([]byte(fm), &doc); err != nil {
			addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
			continue
		}
		runnerName, runnerExplicit := resolveRunner(raw, tree.Factory.AgentDefaults.Runner)
		if runnerExplicit {
			checkRunnerRef(rel, runnerName, "runner", lines, tree.Runners, diags)
		} else if runnerName == "" {
			line := 1
			if _, ok := raw["runner"]; ok {
				line = lineAt(lines, "runner")
			}
			addDiag(diags, rel, line, SeverityError, "runner is required")
		}
		parsed = append(parsed, parsedAgent{
			slug:  e.Name(),
			file:  rel,
			body:  markdown,
			raw:   raw,
			doc:   doc,
			lines: lines,
		})
	}
	reportDuplicateAgents(parsed, diags)
	reportForemen(parsed, diags)
	for _, p := range parsed {
		ag := p.agent(tree.Factory)
		if strings.TrimSpace(ag.Model) == "" && ag.Harness == nil {
			line := 1
			if _, ok := p.raw["model"]; ok {
				line = lineAt(p.lines, "model")
			} else if _, ok := p.raw["harness"]; ok {
				line = lineAt(p.lines, "harness")
			}
			addDiag(diags, p.file, line, SeverityError, "model or harness is required")
		}
		tree.Agents[p.slug] = ag
	}
	warnSameModel(parsed, tree, diags)
}

func reportDuplicateAgents(parsed []parsedAgent, diags *[]Diagnostic) {
	groups := map[string][]parsedAgent{}
	for _, p := range parsed {
		key := strings.ToLower(p.slug)
		groups[key] = append(groups[key], p)
	}
	var keys []string
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		g := groups[key]
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(i, j int) bool { return g[i].file < g[j].file })
		for _, extra := range g[1:] {
			addDiag(diags, extra.file, 1, SeverityError, fmt.Sprintf("duplicate agent slug %q", key))
		}
	}
}

func (p parsedAgent) agent(factory Factory) Agent {
	sparse := Agent{
		Name:               p.slug,
		Description:        p.doc.Description,
		AgentType:          p.doc.AgentType,
		SpawnableBy:        spawnableBy(p.raw, p.doc.SpawnableBy),
		CredentialStrategy: p.doc.CredentialStrategy,
		IdleTimeoutMinutes: p.doc.IdleTimeoutMinutes,
		RequiredRepos:      cloneRepos(p.doc.RequiredRepos),
		Pool:               p.doc.Pool,
		ComputerUseModel:   p.doc.ComputerUseModel,
		MaxTurns:           p.doc.MaxTurns,
		WorkerHost:         p.doc.WorkerHost,
		Body:               p.body,
	}
	if nonBlankString(p.raw["model"]) {
		sparse.Model = strings.TrimSpace(p.doc.Model)
	}
	if v, ok := p.raw["harness"]; ok && usableHarness(v) {
		sparse.Harness = cloneHarness(p.doc.Harness)
	}
	if name, explicit := resolveRunner(p.raw, ""); explicit {
		sparse.Runner = name
	}
	if _, set := p.raw["secrets"]; set {
		sparse.Secrets = cloneStrings(p.doc.Secrets)
		if sparse.Secrets == nil {
			sparse.Secrets = []string{}
		}
	}
	if _, set := p.raw["mcpServers"]; set {
		sparse.MCPServers = cloneMCP(p.doc.MCPServers)
		if sparse.MCPServers == nil {
			sparse.MCPServers = map[string]MCPServer{}
		}
	}
	return Inherit(factory, factory.AgentDefaults, sparse)
}

// spawnableBy keeps an explicit empty list distinct from an omitted key.
// The omitted key is nil so Inherit can default it to foreman only.
func spawnableBy(raw map[string]any, parsed []string) []string {
	if _, set := raw["spawnableBy"]; !set {
		return nil
	}
	out := cloneStrings(parsed)
	if out == nil {
		out = []string{}
	}
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func cloneMCP(in map[string]MCPServer) map[string]MCPServer {
	if in == nil {
		return nil
	}
	out := make(map[string]MCPServer, len(in))
	for k, v := range in {
		if v.Headers != nil {
			v.Headers = cloneStringMap(v.Headers)
		}
		if v.Env != nil {
			v.Env = cloneStringMap(v.Env)
		}
		if v.Args != nil {
			v.Args = cloneStrings(v.Args)
		}
		out[k] = v
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneHarness(h *Harness) *Harness {
	if h == nil {
		return nil
	}
	c := *h
	if h.Auth != nil {
		a := *h.Auth
		c.Auth = &a
	}
	return &c
}

func childMap(raw map[string]any, key string) map[string]any {
	m, _ := raw[key].(map[string]any)
	return m
}

func unmarshalMap(body []byte) (map[string]any, error) {
	var raw map[string]any
	if err := syaml.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}
