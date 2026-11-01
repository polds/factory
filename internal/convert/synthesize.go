package convert

import (
	"bytes"
	"sort"
	"strings"

	syaml "sigs.k8s.io/yaml"
)

func filesFromDoc(kind string, obj map[string]any, configMaps map[string]map[string]string) ([]SourceFile, error) {
	if files := sourceFiles(obj); len(files) > 0 {
		return files, nil
	}
	switch kind {
	case "Factory":
		return oneFile(synthesizeFactory(obj))
	case "FactoryAgent":
		return oneFile(synthesizeAgent(obj, configMaps))
	case "Automation":
		return oneFile(synthesizeAutomation(obj, configMaps))
	case "Runner":
		return oneFile(synthesizeRunner(obj))
	case "ModelRouter":
		return oneFile(synthesizeRouter(obj))
	case "Scorer":
		return oneFile(synthesizeScorer(obj, configMaps))
	case "Skill":
		return oneFile(synthesizeSkill(obj, configMaps))
	case "FactoryWebhook":
		return oneFile(synthesizeWebhook(obj))
	case "BenchmarkSuite":
		return synthesizeBenchmark(obj)
	default:
		return nil, nil
	}
}

func sourceFiles(obj map[string]any) []SourceFile {
	spec, _ := obj["spec"].(map[string]any)
	raw, _ := spec["files"].([]any)
	var out []SourceFile
	for _, item := range raw {
		m, _ := item.(map[string]any)
		path, _ := m["path"].(string)
		content, _ := m["content"].(string)
		if path == "" {
			continue
		}
		out = append(out, SourceFile{Path: path, Content: content})
	}
	return out
}

func configMapData(obj map[string]any) (string, map[string]string) {
	meta, _ := obj["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	data, _ := obj["data"].(map[string]any)
	out := map[string]string{}
	for key, value := range data {
		if s, ok := value.(string); ok {
			out[key] = s
		}
	}
	return name, out
}

func oneFile(path string, body []byte, err error) ([]SourceFile, error) {
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return []SourceFile{{Path: path, Content: string(body)}}, nil
}

func metaName(obj map[string]any) string {
	meta, _ := obj["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	return name
}

func specMap(obj map[string]any) map[string]any {
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return map[string]any{}
	}
	return spec
}

func synthesizeFactory(obj map[string]any) (string, []byte, error) {
	spec := specMap(obj)
	out := map[string]any{
		"schemaVersion": "v1alpha1",
		"name":          metaName(obj),
	}
	if s, _ := spec["schemaVersion"].(string); s != "" {
		out["schemaVersion"] = s
	}
	copyString(out, spec, "description")
	copyString(out, spec, "alias")
	copyAny(out, spec, "codeForges")
	copyAny(out, spec, "repositories")
	copyString(out, spec, "credentialStrategy")
	if secrets := stringList(spec["secrets"]); len(secrets) > 0 {
		out["secrets"] = secrets
	} else if refs := refNames(spec["secretRefs"]); len(refs) > 0 {
		out["secrets"] = refs
	}
	if mcp := mcpToDefinition(spec["mcpServers"]); len(mcp) > 0 {
		out["mcpServers"] = mcp
	}
	if defaults := agentDefaultsToDefinition(spec["agentDefaults"]); len(defaults) > 0 {
		out["agentDefaults"] = defaults
	}
	copyAny(out, spec, "inference")
	copyAny(out, spec, "pipeline")
	copyAny(out, spec, "selfImprovement")
	copyAny(out, spec, "scorerDefaults")
	body, err := syaml.Marshal(out)
	return "factory.yaml", body, err
}

func synthesizeAgent(obj map[string]any, configMaps map[string]map[string]string) (string, []byte, error) {
	name := metaName(obj)
	spec := specMap(obj)
	fm := map[string]any{}
	copyString(fm, spec, "description")
	copyString(fm, spec, "agentType")
	copyString(fm, spec, "model")
	copyAny(fm, spec, "harness")
	copyAny(fm, spec, "spawnableBy")
	copyString(fm, spec, "credentialStrategy")
	copyAny(fm, spec, "idleTimeoutMinutes")
	if repos := spec["requiredRepos"]; repos != nil {
		fm["required_repos"] = repos
	}
	if secrets := stringList(spec["secrets"]); len(secrets) > 0 {
		fm["secrets"] = secrets
	} else if refs := refNames(spec["secretRefs"]); len(refs) > 0 {
		fm["secrets"] = refs
	}
	if mcp := mcpToDefinition(spec["mcpServers"]); len(mcp) > 0 {
		fm["mcpServers"] = mcp
	}
	copyString(fm, spec, "runner")
	if pool, _ := spec["pool"].(string); pool != "" {
		fm["pool"] = pool
	} else if pool := nestedName(spec["poolRef"]); pool != "" {
		fm["pool"] = pool
	}
	copyString(fm, spec, "computerUseModel")
	copyAny(fm, spec, "maxTurns")
	copyString(fm, spec, "workerHost")
	body := stringField(spec, "instructions")
	if body == "" {
		body = configMapBody(spec["instructionsFrom"], configMaps)
	}
	return "agents/" + name + "/agent.md", frontmatter(fm, body), nil
}

func synthesizeAutomation(obj map[string]any, configMaps map[string]map[string]string) (string, []byte, error) {
	name := metaName(obj)
	spec := specMap(obj)
	fm := map[string]any{}
	if enabled, ok := spec["enabled"].(bool); ok {
		fm["enabled"] = enabled
	}
	if agent := nestedName(spec["agentRef"]); agent != "" {
		fm["agent"] = agent
	}
	copyString(fm, spec, "displayName")
	copyAny(fm, spec, "triggers")
	copyString(fm, spec, "model")
	copyAny(fm, spec, "harness")
	copyString(fm, spec, "runner")
	copyString(fm, spec, "pool")
	if repos := spec["requiredRepos"]; repos != nil {
		fm["required_repos"] = repos
	}
	body := stringField(spec, "prompt")
	if body == "" {
		body = configMapBody(spec["promptFrom"], configMaps)
	}
	return "automations/" + name + "/automation.md", frontmatter(fm, body), nil
}

func synthesizeRunner(obj map[string]any) (string, []byte, error) {
	name := metaName(obj)
	spec := specMap(obj)
	out := map[string]any{}
	copyString(out, spec, "description")
	copyAny(out, spec, "setupCommands")
	copyAny(out, spec, "instanceShape")
	copyAny(out, spec, "platform")
	copyAny(out, spec, "failureSessionRetentionMinutes")
	body, err := syaml.Marshal(out)
	return "runners/" + name + ".yaml", body, err
}

func synthesizeRouter(obj map[string]any) (string, []byte, error) {
	name := metaName(obj)
	spec := specMap(obj)
	out := map[string]any{}
	if display, _ := spec["displayName"].(string); display != "" {
		out["name"] = display
	}
	copyString(out, spec, "type")
	copyString(out, spec, "default")
	if routes := spec["routes"]; routes != nil {
		out["routing"] = routes
	}
	if complexity := spec["complexity"]; complexity != nil {
		out["routing"] = complexity
	}
	body, err := syaml.Marshal(out)
	return "routers/" + name + ".yaml", body, err
}

func synthesizeScorer(obj map[string]any, configMaps map[string]map[string]string) (string, []byte, error) {
	name := metaName(obj)
	spec := specMap(obj)
	fm := map[string]any{"name": name}
	copyString(fm, spec, "description")
	if agents, ok := spec["agents"].([]any); ok {
		var list []any
		for _, agent := range agents {
			m, _ := agent.(map[string]any)
			if m == nil {
				continue
			}
			if desc, _ := m["includeDescendants"].(bool); desc {
				list = append(list, map[string]any{"name": m["name"], "includeDescendants": true})
				continue
			}
			if agentName, _ := m["name"].(string); agentName != "" {
				list = append(list, agentName)
			}
		}
		if len(list) > 0 {
			fm["agents"] = list
		}
	}
	copyString(fm, spec, "output")
	copyAny(fm, spec, "labels")
	copyAny(fm, spec, "passingScore")
	copyAny(fm, spec, "samplingRate")
	copyString(fm, spec, "model")
	if on, ok := spec["selfImprovement"].(bool); ok && on {
		fm["selfImprovement"] = true
	}
	body := stringField(spec, "rubric")
	if body == "" {
		body = configMapBody(spec["rubricFrom"], configMaps)
	}
	return "scorers/" + name + "/scorer.md", frontmatter(fm, body), nil
}

func synthesizeSkill(obj map[string]any, configMaps map[string]map[string]string) (string, []byte, error) {
	name := metaName(obj)
	spec := specMap(obj)
	fm := map[string]any{}
	copyString(fm, spec, "description")
	body := stringField(spec, "instructions")
	if body == "" {
		body = configMapBody(spec["instructionsFrom"], configMaps)
	}
	rel := "skills/" + name + "/SKILL.md"
	if agent := nestedName(spec["agentRef"]); agent != "" {
		skillName := name
		if strings.HasPrefix(skillName, agent+"-") {
			skillName = strings.TrimPrefix(skillName, agent+"-")
		}
		rel = "agents/" + agent + "/skills/" + skillName + "/SKILL.md"
		fm["name"] = skillName
	}
	return rel, frontmatter(fm, body), nil
}

func synthesizeWebhook(obj map[string]any) (string, []byte, error) {
	name := metaName(obj)
	spec := specMap(obj)
	out := map[string]any{}
	copyString(out, spec, "authMode")
	copyString(out, spec, "signatureScheme")
	copyString(out, spec, "secretName")
	copyString(out, spec, "deliveryIdHeader")
	if enabled, ok := spec["enabled"].(bool); ok {
		out["enabled"] = enabled
	}
	body, err := syaml.Marshal(out)
	return "webhooks/" + name + ".yaml", body, err
}

func synthesizeBenchmark(obj map[string]any) ([]SourceFile, error) {
	name := metaName(obj)
	spec := specMap(obj)
	suite := map[string]any{}
	if display := metaName(obj); display != "" {
		suite["name"] = display
	}
	copyString(suite, spec, "description")
	copyString(suite, spec, "agent")
	if configs, ok := spec["configurations"].([]any); ok {
		var out []any
		for _, cfg := range configs {
			m, _ := cfg.(map[string]any)
			if m == nil {
				continue
			}
			item := map[string]any{}
			copyString(item, m, "name")
			copyString(item, m, "definitionRef")
			if agents, ok := m["agents"].([]any); ok {
				item["agents"] = agents
			}
			out = append(out, item)
		}
		if len(out) > 0 {
			suite["configurations"] = out
		}
	}
	copyAny(suite, spec, "tasks")
	body, err := syaml.Marshal(suite)
	if err != nil {
		return nil, err
	}
	return []SourceFile{{Path: "benchmarks/" + name + "/suite.yaml", Content: string(body)}}, nil
}

func frontmatter(fm map[string]any, body string) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	if len(fm) > 0 {
		raw, err := syaml.Marshal(fm)
		if err == nil {
			buf.Write(raw)
		}
	}
	buf.WriteString("---\n")
	if body != "" {
		if !strings.HasPrefix(body, "\n") {
			buf.WriteByte('\n')
		}
		buf.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

func copyString(dst, src map[string]any, key string) {
	if s, ok := src[key].(string); ok && s != "" {
		dst[key] = s
	}
}

func copyAny(dst, src map[string]any, key string) {
	if v, ok := src[key]; ok && v != nil {
		dst[key] = v
	}
}

func stringField(spec map[string]any, key string) string {
	s, _ := spec[key].(string)
	return s
}

func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func refNames(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if as, _ := m["as"].(string); as != "" {
			out = append(out, as)
			continue
		}
		if name, _ := m["name"].(string); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func nestedName(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	name, _ := m["name"].(string)
	return name
}

func configMapBody(v any, configMaps map[string]map[string]string) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	ref, _ := m["configMapKeyRef"].(map[string]any)
	if ref == nil {
		return ""
	}
	name, _ := ref["name"].(string)
	key, _ := ref["key"].(string)
	return configMaps[name][key]
}

func agentDefaultsToDefinition(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	out := map[string]any{}
	copyString(out, m, "model")
	copyAny(out, m, "harness")
	copyString(out, m, "runner")
	if pool, _ := m["pool"].(string); pool != "" {
		out["pool"] = pool
	} else if pool := nestedName(m["poolRef"]); pool != "" {
		out["pool"] = pool
	}
	copyString(out, m, "computerUseModel")
	if secrets := stringList(m["secrets"]); len(secrets) > 0 {
		out["secrets"] = secrets
	}
	if mcp := mcpToDefinition(m["mcpServers"]); len(mcp) > 0 {
		out["mcpServers"] = mcp
	}
	copyString(out, m, "workerHost")
	copyString(out, m, "credentialStrategy")
	copyAny(out, m, "idleTimeoutMinutes")
	copyAny(out, m, "maxTurns")
	return out
}

func mcpToDefinition(v any) map[string]any {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := map[string]any{}
	for name, raw := range m {
		srv, _ := raw.(map[string]any)
		if srv == nil {
			continue
		}
		item := map[string]any{}
		copyString(item, srv, "url")
		copyString(item, srv, "command")
		copyAny(item, srv, "args")
		if headers := jsonStringMap(srv["headers"]); len(headers) > 0 {
			item["headers"] = headers
		}
		if env := jsonStringMap(srv["env"]); len(env) > 0 {
			item["env"] = env
		}
		out[name] = item
	}
	return out
}

func jsonStringMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, raw := range m {
		switch t := raw.(type) {
		case string:
			out[key] = t
		default:
			// Objects are secretKeyRef values. Keep the name only.
			if obj, ok := raw.(map[string]any); ok {
				if ref, ok := obj["secretKeyRef"].(map[string]any); ok {
					name, _ := ref["name"].(string)
					if name != "" {
						out[key] = "Bearer {{" + sanitizeRef(name) + "}}"
					}
				}
			}
		}
	}
	return out
}

func sanitizeRef(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		return "SECRET"
	}
	if s[0] >= '0' && s[0] <= '9' {
		return "S_" + s
	}
	return s
}
