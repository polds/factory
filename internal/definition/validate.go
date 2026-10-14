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

var m3Dirs = []string{"automations", "routers", "scorers", "webhooks"}
