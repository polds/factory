package convert

import (
	"regexp"
	"sort"
	"strings"
)

// These patterns match internal/definition's inline-credential check.
// That helper is unexported and internal/definition is outside this task's
// paths, so the check is duplicated here. A follow-up can export it.
var (
	secretRefExact = regexp.MustCompile(`^\{\{[A-Z][A-Z0-9_]*\}\}$`)
	bearerValue    = regexp.MustCompile(`(?i)\bbearer\s+(\S+)`)
	basicValue     = regexp.MustCompile(`(?i)\bbasic\s+(\S+)`)
	tokenWord      = regexp.MustCompile(`(?:^|[^A-Za-z0-9])((?:sk-|ghp_|github_pat_|gho_|glpat-|xox[baprs]-|AKIA)[A-Za-z0-9_/+.=-]*)`)
)

// redactText removes inline credential values. {{SECRET_NAME}} references
// and ordinary names are unchanged, so a clean definition file is byte-identical.
// A value found in a Bearer, Basic, or token-prefix pattern is removed
// everywhere in the text, including a later copy in prose. Trailing quotes
// stay so the YAML still parses.
func redactText(s string) string {
	type swap struct{ from, to string }
	var reps []swap
	seen := map[string]bool{}
	add := func(from, to string) {
		if from == "" || seen[from] || secretRefExact.MatchString(from) {
			return
		}
		seen[from] = true
		reps = append(reps, swap{from, to})
	}
	for _, re := range []*regexp.Regexp{bearerValue, basicValue} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) != 2 {
				continue
			}
			core, _ := peelTrailer(m[1])
			add(core, "{{REDACTED}}")
		}
	}
	for _, m := range tokenWord.FindAllStringSubmatch(s, -1) {
		if len(m) == 2 {
			add(m[1], "REDACTED")
		}
	}
	sort.Slice(reps, func(i, j int) bool { return len(reps[i].from) > len(reps[j].from) })
	for _, rep := range reps {
		s = strings.ReplaceAll(s, rep.from, rep.to)
	}
	return s
}

func peelTrailer(token string) (string, string) {
	i := len(token)
	for i > 0 {
		switch token[i-1] {
		case '"', '\'', '`', ',':
			i--
		default:
			return token[:i], token[i:]
		}
	}
	return "", token
}

func redactAny(v any) any {
	switch t := v.(type) {
	case string:
		return redactText(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = redactAny(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = redactAny(child)
		}
		return out
	default:
		return v
	}
}
