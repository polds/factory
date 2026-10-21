package definition

import (
	"fmt"
	"strings"
	"unicode"

	syaml "sigs.k8s.io/yaml"
)

// validDefinitionRef reports whether ref is a git ref name or a 40-hex SHA.
// Ref rules follow git check-ref-format --allow-onelevel, plus a leading
// hyphen rejection: no "..", no leading "/" or "-", no "@{", no space or
// control characters, no trailing ".lock" or "/", no empty or dot-leading
// component, no trailing dot, and none of "~^:?*[\" .
func validDefinitionRef(ref string) bool {
	if isHexSHA(ref) {
		return true
	}
	return validGitRef(ref)
}

func isHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func validGitRef(ref string) bool {
	if ref == "" || ref == "@" || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "-") || strings.HasSuffix(ref, "/") {
		return false
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") || strings.Contains(ref, `\`) {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
				return false
			}
			if strings.ContainsRune("~^:?*[", r) {
				return false
			}
		}
	}
	return true
}

func loadBenchmarks(root string, tree *Tree, diags *[]Diagnostic) {
	eachResource(root, "benchmarks", "suite.yaml", diags, func(rel, name string, body []byte) {
		suite, ok := parseBenchmarkSuite(rel, name, body, tree, diags)
		if !ok {
			return
		}
		loaded := map[string]BenchmarkTask{}
		taskDir := "benchmarks/" + name + "/tasks"
		eachResource(root, taskDir, ".yaml", diags, func(trel, tname string, tbody []byte) {
			task, tok := parseBenchmarkTask(trel, tname, tbody, diags)
			if !tok {
				return
			}
			loaded[tname] = task
		})
		lines := indexKeys(string(body))
		for _, want := range suite.Tasks {
			want = strings.TrimSpace(want)
			if want == "" {
				continue
			}
			if _, ok := loaded[want]; ok {
				continue
			}
			addDiag(diags, rel, lineAt(lines, "tasks"), SeverityError, fmt.Sprintf("task %q not found", want))
		}
		tree.Benchmarks[name] = suite
		tree.BenchmarkTasks[name] = loaded
	})
}

func parseBenchmarkSuite(rel, name string, body []byte, tree *Tree, diags *[]Diagnostic) (BenchmarkSuite, bool) {
	raw, err := unmarshalMap(body)
	if err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return BenchmarkSuite{}, false
	}
	lines := indexKeys(string(body))
	walkKeys(rel, raw, suiteSchema, "", lines, diags)
	var suite BenchmarkSuite
	if err := syaml.Unmarshal(body, &suite); err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return BenchmarkSuite{}, false
	}
	validateSuite(rel, suite, raw, lines, tree, diags)
	return suite, true
}

func validateSuite(rel string, suite BenchmarkSuite, raw map[string]any, lines map[string]int, tree *Tree, diags *[]Diagnostic) {
	if strings.TrimSpace(suite.Name) == "" {
		addDiag(diags, rel, lineAt(lines, "name"), SeverityError, "name is required")
	}
	agent := strings.TrimSpace(suite.Agent)
	if agent == "" {
		addDiag(diags, rel, lineAt(lines, "agent"), SeverityError, "agent is required")
	} else if tree == nil || tree.Agents == nil || !agentKnown(tree, agent) {
		addDiag(diags, rel, lineAt(lines, "agent"), SeverityError, fmt.Sprintf("agent %q not found", agent))
	}
	n := len(suite.Configurations)
	if n < 1 || n > 6 {
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, "configurations must be from 1 to 6")
	}
	rawList, _ := raw["configurations"].([]any)
	seen := map[string]bool{}
	for i, cfg := range suite.Configurations {
		var rawCfg map[string]any
		if i < len(rawList) {
			rawCfg, _ = rawList[i].(map[string]any)
		}
		checkConfiguration(rel, cfg, rawCfg, lines, tree, seen, diags)
	}
	if len(suite.Tasks) == 0 {
		addDiag(diags, rel, lineAt(lines, "tasks"), SeverityError, "tasks must list at least one task")
	}
	seenTask := map[string]bool{}
	for _, task := range suite.Tasks {
		task = strings.TrimSpace(task)
		if task == "" {
			addDiag(diags, rel, lineAt(lines, "tasks"), SeverityError, "task name is empty")
			continue
		}
		if seenTask[task] {
			addDiag(diags, rel, lineAt(lines, "tasks"), SeverityError, fmt.Sprintf("duplicate task %q", task))
		}
		seenTask[task] = true
	}
}

func agentKnown(tree *Tree, name string) bool {
	_, ok := tree.Agents[name]
	return ok
}

func checkConfiguration(rel string, cfg BenchmarkConfiguration, raw map[string]any, lines map[string]int, tree *Tree, seen map[string]bool, diags *[]Diagnostic) {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, "configuration name is required")
	} else if seen[name] {
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, fmt.Sprintf("duplicate configuration %q", name))
	}
	if name != "" {
		seen[name] = true
	}
	ref := strings.TrimSpace(cfg.DefinitionRef)
	if ref != "" && !validDefinitionRef(ref) {
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, fmt.Sprintf("definitionRef %q is not a git ref or a 40-hex SHA", ref))
	}
	if len(cfg.Agents) == 0 {
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, fmt.Sprintf("configuration %q has no agents", name))
	}
	var rawAgents []any
	if raw != nil {
		rawAgents, _ = raw["agents"].([]any)
	}
	for i, binding := range cfg.Agents {
		var rawBinding map[string]any
		if i < len(rawAgents) {
			rawBinding, _ = rawAgents[i].(map[string]any)
		}
		checkBinding(rel, name, binding, rawBinding, lines, tree, diags)
	}
}

func checkBinding(rel, config string, binding BenchmarkBinding, raw map[string]any, lines map[string]int, tree *Tree, diags *[]Diagnostic) {
	agent := strings.TrimSpace(binding.Agent)
	if agent == "" {
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, fmt.Sprintf("configuration %q binding agent is required", config))
	} else if tree == nil || !agentKnown(tree, agent) {
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, fmt.Sprintf("agent %q not found", agent))
	}
	hasModel := raw != nil && nonBlankString(raw["model"])
	hasHarness := raw != nil && usableHarness(raw["harness"])
	switch {
	case hasModel && hasHarness:
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, fmt.Sprintf("binding for agent %q sets both model and harness", agent))
	case !hasModel && !hasHarness:
		addDiag(diags, rel, lineAt(lines, "configurations"), SeverityError, fmt.Sprintf("binding for agent %q requires model or harness", agent))
	}
}

func parseBenchmarkTask(rel, name string, body []byte, diags *[]Diagnostic) (BenchmarkTask, bool) {
	raw, err := unmarshalMap(body)
	if err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return BenchmarkTask{}, false
	}
	lines := indexKeys(string(body))
	walkKeys(rel, raw, taskSchema, "", lines, diags)
	var doc struct {
		Title           string   `json:"title,omitempty"`
		Prompt          string   `json:"prompt,omitempty"`
		SuccessCriteria string   `json:"successCriteria,omitempty"`
		SourceRunID     string   `json:"sourceRunId,omitempty"`
		Tags            []string `json:"tags,omitempty"`
	}
	if err := syaml.Unmarshal(body, &doc); err != nil {
		addDiag(diags, rel, 1, SeverityError, "invalid YAML: "+err.Error())
		return BenchmarkTask{}, false
	}
	task := BenchmarkTask{
		Name:            name,
		Title:           doc.Title,
		Prompt:          doc.Prompt,
		SuccessCriteria: doc.SuccessCriteria,
		SourceRunID:     doc.SourceRunID,
		Tags:            doc.Tags,
	}
	if strings.TrimSpace(task.Title) == "" {
		addDiag(diags, rel, lineAt(lines, "title"), SeverityError, "title is required")
	}
	if strings.TrimSpace(task.Prompt) == "" {
		addDiag(diags, rel, lineAt(lines, "prompt"), SeverityError, "prompt is required")
	}
	if strings.TrimSpace(task.SuccessCriteria) == "" {
		addDiag(diags, rel, lineAt(lines, "successCriteria"), SeverityError, "successCriteria is required")
	}
	rawRefs, present := raw["startingRepoRefs"]
	if !present || rawRefs == nil {
		return task, true
	}
	refs, ok := rawRefs.([]any)
	if !ok {
		addDiag(diags, rel, lineAt(lines, "startingRepoRefs"), SeverityError, "startingRepoRefs must be a list")
		return task, true
	}
	for _, item := range refs {
		s, ok := item.(string)
		if !ok {
			addDiag(diags, rel, lineAt(lines, "startingRepoRefs"), SeverityError, "startingRepoRefs entry is not a string")
			continue
		}
		parsed, err := ParseRepoRef(s)
		if err != nil {
			addDiag(diags, rel, lineAt(lines, "startingRepoRefs"), SeverityError, err.Error())
			continue
		}
		task.StartingRepoRefs = append(task.StartingRepoRefs, parsed)
	}
	return task, true
}
