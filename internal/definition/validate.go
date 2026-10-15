package definition

import (
	"fmt"
	"sort"
	"strings"
)

func walkKeys(file string, v any, spec *keySpec, path string, lines map[string]int, diags *[]Diagnostic) {
	switch n := v.(type) {
	case map[string]any:
		if spec == nil {
			for k := range n {
				p := joinPath(path, k)
				addDiag(diags, file, lineAt(lines, p), SeverityError, fmt.Sprintf("unknown key %q", k))
			}
			return
		}
		if spec.free {
			for k, child := range n {
				walkKeys(file, child, spec.elem, joinPath(path, k), lines, diags)
			}
			return
		}
		for k, child := range n {
			p := joinPath(path, k)
			sub, ok := spec.children[k]
			if !ok {
				addDiag(diags, file, lineAt(lines, p), SeverityError, fmt.Sprintf("unknown key %q", k))
				continue
			}
			if sub != nil && sub.warn != "" {
				addDiag(diags, file, lineAt(lines, p), SeverityWarning, sub.warn)
				continue
			}
			walkKeys(file, child, sub, p, lines, diags)
		}
	case []any:
		if spec == nil || spec.elem == nil {
			return
		}
		for _, item := range n {
			walkKeys(file, item, spec.elem, path, lines, diags)
		}
	}
}

// checkSlackChannels rejects a present channel that is empty or not a string.
// Omitting the key is allowed, matching the published schema.
func checkSlackChannels(file string, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	items, ok := raw["integrations"].([]any)
	if !ok {
		return
	}
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		slack, ok := obj["slack"].(map[string]any)
		if !ok {
			continue
		}
		ch, present := slack["channel"]
		if !present {
			continue
		}
		s, isStr := ch.(string)
		if isStr && s != "" {
			continue
		}
		addDiag(diags, file, lineAt(lines, "integrations.slack.channel"), SeverityError, "slack.channel must be a non-empty string")
	}
}

func checkSchemaVersion(file string, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	v, ok := raw["schemaVersion"]
	line := lineAt(lines, "schemaVersion")
	if !ok || v == nil {
		addDiag(diags, file, line, SeverityError, "schemaVersion is required")
		return
	}
	s := fmt.Sprint(v)
	if s != "v1alpha1" {
		addDiag(diags, file, line, SeverityError, fmt.Sprintf("unsupported schemaVersion %q", s))
	}
}

func checkName(file string, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	s, _ := raw["name"].(string)
	if strings.TrimSpace(s) == "" {
		addDiag(diags, file, lineAt(lines, "name"), SeverityError, "name is required")
	}
}

func checkExclusive(file, prefix string, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	m := raw
	if prefix != "" {
		nested, ok := raw[prefix].(map[string]any)
		if !ok {
			return
		}
		m = nested
	}
	if !nonBlankString(m["model"]) || !usableHarness(m["harness"]) {
		return
	}
	modelPath := joinPath(prefix, "model")
	harnessPath := joinPath(prefix, "harness")
	line := lineAt(lines, modelPath)
	if other := lineAt(lines, harnessPath); other > line {
		line = other
	}
	addDiag(diags, file, line, SeverityError, "model and harness are mutually exclusive")
}

// nonBlankString reports whether v is a string with non-whitespace text.
// Null and blank values are unset so agentDefaults can be inherited.
func nonBlankString(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

// usableHarness reports whether v is a harness mapping. Null is unset.
func usableHarness(v any) bool {
	return v != nil
}

func resolveRunner(raw map[string]any, fallback string) (name string, explicit bool) {
	if v, ok := raw["runner"]; ok && nonBlankString(v) {
		s, _ := v.(string)
		return strings.TrimSpace(s), true
	}
	return strings.TrimSpace(fallback), false
}

func checkRunnerRef(file, name, path string, lines map[string]int, have map[string]Runner, diags *[]Diagnostic) {
	if strings.TrimSpace(name) == "" {
		return
	}
	if _, ok := have[name]; ok {
		return
	}
	addDiag(diags, file, lineAt(lines, path), SeverityError, fmt.Sprintf("runner %q not found", name))
}

func reportForemen(parsed []parsedAgent, diags *[]Diagnostic) {
	var found []parsedAgent
	for _, p := range parsed {
		if p.doc.AgentType == "FOREMAN" {
			found = append(found, p)
		}
	}
	if len(found) < 2 {
		return
	}
	sort.Slice(found, func(i, j int) bool { return found[i].file < found[j].file })
	for _, extra := range found[1:] {
		addDiag(diags, extra.file, 1, SeverityError, "more than one agent has agentType FOREMAN")
	}
}

func warnSameModel(parsed []parsedAgent, tree *Tree, diags *[]Diagnostic) {
	var implementers, reviewers []parsedAgent
	for _, p := range parsed {
		switch p.doc.AgentType {
		case "IMPLEMENT":
			implementers = append(implementers, p)
		case "REVIEW":
			reviewers = append(reviewers, p)
		}
	}
	sort.Slice(implementers, func(i, j int) bool { return implementers[i].file < implementers[j].file })
	sort.Slice(reviewers, func(i, j int) bool { return reviewers[i].file < reviewers[j].file })
	for _, rev := range reviewers {
		reviewModel := CanonicalModel(tree.Agents[rev.slug])
		if reviewModel == "" {
			continue
		}
		line := lineAt(rev.lines, "model")
		if strings.TrimSpace(rev.doc.Model) == "" && rev.doc.Harness != nil {
			line = lineAt(rev.lines, "harness")
		}
		for _, impl := range implementers {
			if CanonicalModel(tree.Agents[impl.slug]) != reviewModel {
				continue
			}
			addDiag(diags, rev.file, line, SeverityWarning, fmt.Sprintf("review agent %q uses the same model as implement agent %q", rev.slug, impl.slug))
		}
	}
}

func validatePipeline(tree *Tree, lines map[string]int, diags *[]Diagnostic) {
	p := tree.Factory.Pipeline
	if p == nil {
		return
	}
	const file = "factory.yaml"
	if len(p.Stages) == 0 {
		addDiag(diags, file, lineAt(lines, "pipeline.stages"), SeverityError, "pipeline.stages is required")
	}
	stages := map[string]bool{}
	for _, stage := range p.Stages {
		if stages[stage] {
			addDiag(diags, file, lineAt(lines, "pipeline.stages"), SeverityError, fmt.Sprintf("duplicate pipeline stage %q", stage))
		}
		stages[stage] = true
		agentName, ok := p.Agents[stage]
		if !ok || strings.TrimSpace(agentName) == "" {
			addDiag(diags, file, lineAt(lines, "pipeline.agents."+stage), SeverityError, fmt.Sprintf("pipeline stage %q has no agent", stage))
			continue
		}
		if _, exists := tree.Agents[agentName]; !exists {
			addDiag(diags, file, lineAt(lines, "pipeline.agents."+stage), SeverityError, fmt.Sprintf("agent %q not found", agentName))
		}
	}
	for stage := range p.Agents {
		if stages[stage] {
			continue
		}
		addDiag(diags, file, lineAt(lines, "pipeline.agents."+stage), SeverityError, fmt.Sprintf("pipeline agent stage %q is not in stages", stage))
	}
	for stage := range p.Skip {
		if stages[stage] {
			continue
		}
		addDiag(diags, file, lineAt(lines, "pipeline.skip."+stage), SeverityError, fmt.Sprintf("pipeline skip stage %q is not in stages", stage))
	}
	if p.RevisionLimit < 0 {
		addDiag(diags, file, lineAt(lines, "pipeline.revisionLimit"), SeverityError, "pipeline.revisionLimit must be >= 0")
	}
	for name, gate := range p.Approvals {
		if gate.Required && strings.TrimSpace(gate.Approvers) == "" {
			addDiag(diags, file, lineAt(lines, "pipeline.approvals."+name), SeverityError, fmt.Sprintf("approval %q requires approvers", name))
		}
	}
}

func validateScorerDefaults(tree *Tree, lines map[string]int, diags *[]Diagnostic) {
	if tree.Factory.ScorerDefaults == nil {
		return
	}
	checkRunnerRef("factory.yaml", tree.Factory.ScorerDefaults.Runner, "scorerDefaults.runner", lines, tree.Runners, diags)
}

func sortDiags(diags []Diagnostic) {
	sort.Slice(diags, func(i, j int) bool {
		if diags[i].File != diags[j].File {
			return diags[i].File < diags[j].File
		}
		if diags[i].Line != diags[j].Line {
			return diags[i].Line < diags[j].Line
		}
		if diags[i].Severity != diags[j].Severity {
			return diags[i].Severity < diags[j].Severity
		}
		return diags[i].Message < diags[j].Message
	})
}
