package definition

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	syaml "sigs.k8s.io/yaml"
)

var (
	secretNameExact = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	headerNameExact = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_|~]+$`)
)

func loadRouters(root string, tree *Tree, diags *[]Diagnostic) {
	dir := filepath.Join(root, "routers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		addDiag(diags, "routers", 1, SeverityError, err.Error())
		return
	}
	type loaded struct {
		rel   string
		lines map[string]int
	}
	var docs []loaded
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("routers", e.Name()))
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
		walkKeys(rel, raw, routerSchema, "", lines, diags)
		var doc routerDoc
		if err := syaml.Unmarshal(body, &doc); err != nil {
			addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		tree.Routers[name] = routerFrom(name, doc, raw, rel, lines, diags)
		docs = append(docs, loaded{rel: rel, lines: lines})
	}
	for _, d := range docs {
		name := strings.TrimSuffix(filepath.Base(d.rel), ".yaml")
		checkRouterTargets(d.rel, d.lines, tree.Routers[name], tree.Routers, diags)
	}
}

type routerDoc struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Default string `json:"default,omitempty"`
}

func routerFrom(name string, doc routerDoc, raw map[string]any, rel string, lines map[string]int, diags *[]Diagnostic) Router {
	rt := Router{
		Name:        name,
		DisplayName: doc.Name,
		Type:        strings.TrimSpace(doc.Type),
		Default:     strings.TrimSpace(doc.Default),
	}
	routing, hasRouting := raw["routing"]
	switch rt.Type {
	case "prompt":
		if !hasRouting {
			return rt
		}
		list, ok := routing.([]any)
		if !ok {
			addDiag(diags, rel, lineAt(lines, "routing"), SeverityError, "prompt routing must be a list")
			return rt
		}
		for _, item := range list {
			m, _ := item.(map[string]any)
			desc, _ := m["description"].(string)
			model, _ := m["model"].(string)
			rt.Routes = append(rt.Routes, RouterRoute{Description: desc, Model: strings.TrimSpace(model)})
		}
	case "complexity":
		if !hasRouting {
			return rt
		}
		m, ok := routing.(map[string]any)
		if !ok {
			addDiag(diags, rel, lineAt(lines, "routing"), SeverityError, "complexity routing must be a map")
			return rt
		}
		rt.Complexity = map[string]string{}
		for _, key := range []string{"easy", "medium", "hard"} {
			v, ok := m[key]
			if !ok {
				continue
			}
			s, _ := v.(string)
			rt.Complexity[key] = strings.TrimSpace(s)
		}
	default:
		addDiag(diags, rel, lineAt(lines, "type"), SeverityError, fmt.Sprintf("unsupported router type %q", doc.Type))
	}
	return rt
}

func checkRouterTargets(rel string, lines map[string]int, rt Router, routers map[string]Router, diags *[]Diagnostic) {
	if strings.TrimSpace(rt.Default) == "" {
		addDiag(diags, rel, lineAt(lines, "default"), SeverityError, "router default is required")
	} else if msg := concreteTarget(rt.Default, routers); msg != "" {
		addDiag(diags, rel, lineAt(lines, "default"), SeverityError, msg)
	}
	switch rt.Type {
	case "prompt":
		for _, route := range rt.Routes {
			if msg := concreteTarget(route.Model, routers); msg != "" {
				addDiag(diags, rel, lineAt(lines, "routing"), SeverityError, msg)
			}
		}
	case "complexity":
		for _, key := range []string{"easy", "medium", "hard"} {
			model, ok := rt.Complexity[key]
			if !ok {
				continue
			}
			if msg := concreteTarget(model, routers); msg != "" {
				addDiag(diags, rel, lineAt(lines, "routing."+key), SeverityError, msg)
			}
		}
	}
}

func concreteTarget(target string, routers map[string]Router) string {
	target = strings.TrimSpace(target)
	if target == "" || target == "auto" {
		return fmt.Sprintf("router target %q is not a concrete model", target)
	}
	if _, ok := routers[target]; ok {
		return fmt.Sprintf("router target %q is not a concrete model", target)
	}
	return ""
}

func loadSkills(root string, tree *Tree, diags *[]Diagnostic) {
	loadSkillGroup(root, "skills", func(sk Skill) {
		tree.Skills[sk.Name] = sk
	}, diags)
}

func loadAgentSkills(root string, tree *Tree, diags *[]Diagnostic) {
	dir := filepath.Join(root, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		addDiag(diags, "agents", 1, SeverityError, err.Error())
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("agents", e.Name(), "skills"))
		skillDir := filepath.Join(root, filepath.FromSlash(rel))
		st, err := os.Stat(skillDir)
		if err != nil || !st.IsDir() {
			continue
		}
		if _, ok := tree.Agents[e.Name()]; !ok {
			addDiag(diags, rel, 1, SeverityError, fmt.Sprintf("agent %q not found", e.Name()))
		}
		var skills []Skill
		loadSkillGroup(root, rel, func(sk Skill) {
			skills = append(skills, sk)
		}, diags)
		if len(skills) == 0 {
			continue
		}
		sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
		tree.AgentSkills[e.Name()] = skills
	}
}

func loadSkillGroup(root, rel string, put func(Skill), diags *[]Diagnostic) {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		addDiag(diags, rel, 1, SeverityError, err.Error())
		return
	}
	for _, e := range entries {
		child := rel + "/" + e.Name()
		if !e.IsDir() {
			addDiag(diags, child, 1, SeverityError, "skill must be a directory containing SKILL.md")
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			addDiag(diags, child+"/SKILL.md", 1, SeverityError, "missing SKILL.md")
			continue
		}
		sk, ok := parseSkill(child+"/SKILL.md", e.Name(), body, diags)
		if !ok {
			continue
		}
		put(sk)
	}
}

func parseSkill(rel, name string, body []byte, diags *[]Diagnostic) (Skill, bool) {
	fm, markdown, ok, opened := splitFrontmatter(string(body))
	if !ok {
		if opened {
			addDiag(diags, rel, 1, SeverityError, "unclosed YAML frontmatter")
			return Skill{}, false
		}
		return Skill{Name: name, Body: string(body)}, true
	}
	raw, err := unmarshalMap([]byte(fm))
	if err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Skill{}, false
	}
	lines := indexKeys(fm)
	shiftLines(lines, 1)
	walkKeys(rel, raw, skillSchema, "", lines, diags)
	var doc struct {
		Description string `json:"description,omitempty"`
	}
	if err := syaml.Unmarshal([]byte(fm), &doc); err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Skill{}, false
	}
	return Skill{Name: name, Description: doc.Description, Body: markdown}, true
}

// eachResource walks one resource directory and calls fn with the file body.
//
// file is either a nested document name (automation.md, scorer.md, suite.yaml),
// read from dir/<name>/<file>, or a flat suffix that starts with '.' (.yaml),
// read from dir/<name>.yaml. A missing directory is not an error. A missing
// nested document is skipped. A flat-file read error is reported. Callers:
// automations, scorers, webhooks, and benchmarks (suites and tasks).
func eachResource(root, dir, file string, diags *[]Diagnostic, fn func(rel, name string, body []byte)) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	entries, err := os.ReadDir(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		addDiag(diags, dir, 1, SeverityError, err.Error())
		return
	}
	flat := strings.HasPrefix(file, ".")
	for _, e := range entries {
		if flat {
			if e.IsDir() || !strings.HasSuffix(e.Name(), file) {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(dir, e.Name()))
			body, err := os.ReadFile(filepath.Join(abs, e.Name()))
			if err != nil {
				addDiag(diags, rel, 1, SeverityError, err.Error())
				continue
			}
			fn(rel, strings.TrimSuffix(e.Name(), file), body)
			continue
		}
		if !e.IsDir() {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(dir, e.Name(), file))
		body, err := os.ReadFile(filepath.Join(abs, e.Name(), file))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			addDiag(diags, rel, 1, SeverityError, err.Error())
			continue
		}
		fn(rel, e.Name(), body)
	}
}

func loadAutomations(root string, tree *Tree, diags *[]Diagnostic) {
	eachResource(root, "automations", "automation.md", diags, func(rel, name string, body []byte) {
		auto, ok := parseAutomation(rel, name, body, tree, diags)
		if !ok {
			return
		}
		tree.Automations[name] = auto
	})
}

type automationDoc struct {
	Enabled            bool                 `json:"enabled"`
	Agent              string               `json:"agent,omitempty"`
	DisplayName        string               `json:"displayName,omitempty"`
	Triggers           []Trigger            `json:"triggers,omitempty"`
	Model              string               `json:"model,omitempty"`
	Harness            *Harness             `json:"harness,omitempty"`
	Runner             string               `json:"runner,omitempty"`
	Pool               string               `json:"pool,omitempty"`
	ComputerUseModel   string               `json:"computerUseModel,omitempty"`
	Secrets            []string             `json:"secrets,omitempty"`
	MCPServers         map[string]MCPServer `json:"mcpServers,omitempty"`
	RequiredRepos      []Repository         `json:"required_repos,omitempty"`
	CredentialStrategy string               `json:"credentialStrategy,omitempty"`
	IdleTimeoutMinutes int                  `json:"idleTimeoutMinutes,omitempty"`
	MaxTurns           int                  `json:"maxTurns,omitempty"`
	WorkerHost         string               `json:"workerHost,omitempty"`
	Description        string               `json:"description,omitempty"`
}

func parseAutomation(rel, name string, body []byte, tree *Tree, diags *[]Diagnostic) (Automation, bool) {
	fm, markdown, ok, opened := splitFrontmatter(string(body))
	if !ok {
		msg := "missing YAML frontmatter"
		if opened {
			msg = "unclosed YAML frontmatter"
		}
		addDiag(diags, rel, 1, SeverityError, msg)
		return Automation{}, false
	}
	raw, err := unmarshalMap([]byte(fm))
	if err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Automation{}, false
	}
	lines := indexKeys(fm)
	shiftLines(lines, 1)
	walkKeys(rel, raw, automationSchema, "", lines, diags)
	checkExclusive(rel, "", raw, lines, diags)
	scanMCPCredentials(rel, "", raw, lines, diags)
	var doc automationDoc
	if err := syaml.Unmarshal([]byte(fm), &doc); err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Automation{}, false
	}
	auto := Automation{
		Name:               name,
		Enabled:            doc.Enabled,
		Agent:              strings.TrimSpace(doc.Agent),
		DisplayName:        doc.DisplayName,
		Triggers:           doc.Triggers,
		Pool:               doc.Pool,
		ComputerUseModel:   doc.ComputerUseModel,
		CredentialStrategy: doc.CredentialStrategy,
		IdleTimeoutMinutes: doc.IdleTimeoutMinutes,
		MaxTurns:           doc.MaxTurns,
		WorkerHost:         doc.WorkerHost,
		Body:               markdown,
	}
	if nonBlankString(raw["model"]) {
		auto.Model = strings.TrimSpace(doc.Model)
	}
	if v, ok := raw["harness"]; ok && usableHarness(v) {
		auto.Harness = cloneHarness(doc.Harness)
	}
	if runnerName, explicit := resolveRunner(raw, ""); explicit {
		auto.Runner = runnerName
		checkRunnerRef(rel, runnerName, "runner", lines, tree.Runners, diags)
	}
	if _, set := raw["secrets"]; set {
		auto.Secrets = cloneStrings(doc.Secrets)
		if auto.Secrets == nil {
			auto.Secrets = []string{}
		}
	}
	if _, set := raw["mcpServers"]; set {
		auto.MCPServers = cloneMCP(doc.MCPServers)
		if auto.MCPServers == nil {
			auto.MCPServers = map[string]MCPServer{}
		}
	}
	if _, set := raw["required_repos"]; set {
		auto.RequiredRepos = cloneRepos(doc.RequiredRepos)
		if auto.RequiredRepos == nil {
			auto.RequiredRepos = []Repository{}
		}
	}
	if auto.Agent == "" {
		addDiag(diags, rel, lineAt(lines, "agent"), SeverityError, "agent is required")
		return auto, true
	}
	target, exists := tree.Agents[auto.Agent]
	if !exists {
		addDiag(diags, rel, lineAt(lines, "agent"), SeverityError, fmt.Sprintf("agent %q not found", auto.Agent))
		return auto, true
	}
	resolved := InheritAutomation(target, auto)
	resolved.Name = auto.Name
	resolved.Enabled = auto.Enabled
	resolved.Agent = auto.Agent
	resolved.DisplayName = auto.DisplayName
	resolved.Triggers = auto.Triggers
	resolved.Body = auto.Body
	return resolved, true
}

func loadScorers(root string, tree *Tree, diags *[]Diagnostic) {
	eachResource(root, "scorers", "scorer.md", diags, func(rel, name string, body []byte) {
		sc, ok := parseScorer(rel, name, body, tree, diags)
		if !ok {
			return
		}
		tree.Scorers[name] = sc
	})
}

type scorerDoc struct {
	Description     string        `json:"description,omitempty"`
	Agents          []ScorerAgent `json:"agents,omitempty"`
	Output          string        `json:"output,omitempty"`
	Labels          []ScorerLabel `json:"labels,omitempty"`
	PassingScore    float64       `json:"passingScore,omitempty"`
	SamplingRate    int           `json:"samplingRate,omitempty"`
	Model           string        `json:"model,omitempty"`
	SelfImprovement bool          `json:"selfImprovement,omitempty"`
}

func parseScorer(rel, name string, body []byte, tree *Tree, diags *[]Diagnostic) (Scorer, bool) {
	fm, markdown, ok, opened := splitFrontmatter(string(body))
	if !ok {
		msg := "missing YAML frontmatter"
		if opened {
			msg = "unclosed YAML frontmatter"
		}
		addDiag(diags, rel, 1, SeverityError, msg)
		return Scorer{}, false
	}
	raw, err := unmarshalMap([]byte(fm))
	if err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Scorer{}, false
	}
	lines := indexKeys(fm)
	shiftLines(lines, 1)
	walkKeys(rel, raw, scorerSchema, "", lines, diags)
	var doc scorerDoc
	if err := syaml.Unmarshal([]byte(fm), &doc); err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Scorer{}, false
	}
	// docs/03 does not give a scorer harness, runner, secrets, or mcpServers.
	// docs/08 runs the judge on scorerDefaults.runner with the scorer model
	// and no execution secrets. Accepting the keys and dropping them hid
	// that mistake.
	for _, key := range []string{"harness", "runner", "secrets", "mcpServers"} {
		if _, set := raw[key]; !set {
			continue
		}
		addDiag(diags, rel, lineAt(lines, key), SeverityError, fmt.Sprintf("scorer cannot set %q", key))
	}
	model := strings.TrimSpace(doc.Model)
	if model == "" && tree.Factory.ScorerDefaults != nil {
		model = strings.TrimSpace(tree.Factory.ScorerDefaults.Model)
	}
	meet, below := false, false
	for _, label := range doc.Labels {
		if label.Score >= doc.PassingScore {
			meet = true
		} else {
			below = true
		}
	}
	if !meet || !below {
		addDiag(diags, rel, lineAt(lines, "passingScore"), SeverityError, "scorer labels must fall on both sides of passingScore")
	}
	for _, agent := range doc.Agents {
		if strings.TrimSpace(agent.Name) == "" {
			addDiag(diags, rel, lineAt(lines, "agents"), SeverityError, "scorer agent name is required")
			continue
		}
		if _, exists := tree.Agents[agent.Name]; !exists {
			addDiag(diags, rel, lineAt(lines, "agents"), SeverityError, fmt.Sprintf("agent %q not found", agent.Name))
		}
	}
	sc := Scorer{
		Name:            name,
		Description:     doc.Description,
		Agents:          doc.Agents,
		Output:          doc.Output,
		Labels:          doc.Labels,
		PassingScore:    doc.PassingScore,
		SamplingRate:    doc.SamplingRate,
		Model:           model,
		SelfImprovement: doc.SelfImprovement,
		Body:            markdown,
	}
	if _, set := raw["selfImprovement"]; set {
		sc.selfImprovementLine = lineAt(lines, "selfImprovement")
	}
	return sc, true
}

func loadWebhooks(root string, tree *Tree, diags *[]Diagnostic) {
	eachResource(root, "webhooks", ".yaml", diags, func(rel, name string, body []byte) {
		hook, ok := parseWebhook(rel, name, body, diags)
		if !ok {
			return
		}
		tree.Webhooks[hook.Name] = hook
	})
}

type webhookDoc struct {
	AuthMode         string `json:"authMode,omitempty"`
	SignatureScheme  string `json:"signatureScheme,omitempty"`
	SecretName       string `json:"secretName,omitempty"`
	DeliveryIDHeader string `json:"deliveryIdHeader,omitempty"`
	Enabled          bool   `json:"enabled"`
}

func parseWebhook(rel, name string, body []byte, diags *[]Diagnostic) (Webhook, bool) {
	raw, err := unmarshalMap(body)
	if err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Webhook{}, false
	}
	lines := indexKeys(string(body))
	walkKeys(rel, raw, webhookSchema, "", lines, diags)
	var doc webhookDoc
	if err := syaml.Unmarshal(body, &doc); err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return Webhook{}, false
	}
	hook := Webhook{
		Name:             name,
		AuthMode:         strings.TrimSpace(doc.AuthMode),
		SignatureScheme:  strings.TrimSpace(doc.SignatureScheme),
		SecretName:       strings.TrimSpace(doc.SecretName),
		DeliveryIDHeader: strings.TrimSpace(doc.DeliveryIDHeader),
		Enabled:          doc.Enabled,
	}
	switch hook.AuthMode {
	case "token", "url_token", "signature":
	case "":
		addDiag(diags, rel, lineAt(lines, "authMode"), SeverityError, "authMode is required")
	default:
		addDiag(diags, rel, lineAt(lines, "authMode"), SeverityError, fmt.Sprintf("unsupported authMode %q", hook.AuthMode))
	}
	if hook.AuthMode == "signature" && hook.SignatureScheme == "" {
		addDiag(diags, rel, lineAt(lines, "signatureScheme"), SeverityError, "signatureScheme is required when authMode is signature")
	}
	if hook.SignatureScheme != "" && hook.AuthMode != "signature" {
		addDiag(diags, rel, lineAt(lines, "signatureScheme"), SeverityError, "signatureScheme is only valid when authMode is signature")
	}
	if hook.SignatureScheme != "" && !allowedSignatureScheme(hook.SignatureScheme) {
		addDiag(diags, rel, lineAt(lines, "signatureScheme"), SeverityError, fmt.Sprintf("unsupported signatureScheme %q", hook.SignatureScheme))
	}
	if hook.SecretName == "" || !secretNameExact.MatchString(hook.SecretName) || inlineCredential(hook.SecretName) {
		addDiag(diags, rel, lineAt(lines, "secretName"), SeverityError, "secretName must name a secret")
	}
	if hook.DeliveryIDHeader != "" {
		if !headerNameExact.MatchString(hook.DeliveryIDHeader) {
			addDiag(diags, rel, lineAt(lines, "deliveryIdHeader"), SeverityError, "deliveryIdHeader is not a valid header name")
		}
		if credentialHeader(hook.DeliveryIDHeader) {
			addDiag(diags, rel, lineAt(lines, "deliveryIdHeader"), SeverityError, fmt.Sprintf("deliveryIdHeader %q cannot carry credentials", hook.DeliveryIDHeader))
		}
	}
	return hook, true
}

func allowedSignatureScheme(scheme string) bool {
	switch scheme {
	case "github", "gitlab", "stripe", "sentry", "pagerduty", "vercel", "standard_webhooks":
		return true
	default:
		return false
	}
}

func credentialHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie":
		return true
	default:
		return strings.Contains(strings.ToLower(name), "signature")
	}
}
