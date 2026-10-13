package definition

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	secretRefExact = regexp.MustCompile(`^\{\{[A-Z][A-Z0-9_]*\}\}$`)
	secretRefAny   = regexp.MustCompile(`\{\{[A-Z][A-Z0-9_]*\}\}`)
	bearerValue    = regexp.MustCompile(`(?i)\bbearer\s+(\S+)`)
	basicValue     = regexp.MustCompile(`(?i)\bbasic\s+(\S+)`)
	tokenPrefix    = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:sk-[A-Za-z0-9]|ghp_|github_pat_|gho_|glpat-|xox[baprs]-|AKIA[0-9A-Z]{16})`)
)

// splitFrontmatter splits a Markdown document. opened is true when the file
// starts with a --- fence, even if the closing fence is missing.
func splitFrontmatter(src string) (yamlText, body string, ok, opened bool) {
	src = strings.TrimPrefix(src, "\uFEFF")
	if !utf8.ValidString(src) {
		return "", "", false, false
	}
	lines := strings.Split(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			body = strings.Join(lines[i+1:], "\n")
			body = strings.TrimPrefix(body, "\n")
			return strings.Join(lines[1:i], "\n"), body, true, true
		}
	}
	return "", "", false, true
}

// indexKeys maps a dotted key path to the 1-based line of its first occurrence.
// List-item keys are indexed under the parent mapping. Block scalars are skipped
// so prose colons are not treated as keys.
func indexKeys(src string) map[string]int {
	lines := strings.Split(src, "\n")
	type frame struct {
		indent int
		path   string
	}
	stack := []frame{{indent: -1, path: ""}}
	out := map[string]int{}
	skipUntil := -1

	for i, line := range lines {
		lineNo := i + 1
		if skipUntil >= 0 {
			indent := leadingSpaceCount(line)
			if strings.TrimSpace(line) == "" || indent > skipUntil {
				continue
			}
			skipUntil = -1
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent, rest := leadingSpaces(line)
		if strings.HasPrefix(rest, "- ") || rest == "-" {
			indent += 2
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "-"))
			if rest == "" {
				continue
			}
		}
		for len(stack) > 1 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1].path

		if strings.HasPrefix(strings.TrimSpace(rest), "{") {
			recordInline(out, parent, rest, lineNo)
			continue
		}

		key, value, ok := splitYAMLKey(rest)
		if !ok {
			continue
		}
		path := joinPath(parent, key)
		if _, exists := out[path]; !exists {
			out[path] = lineNo
		}
		stack = append(stack, frame{indent: indent, path: path})
		if isBlockScalar(value) {
			skipUntil = indent
		}
		if strings.Contains(value, "{") {
			recordInline(out, path, value, lineNo)
		}
	}
	return out
}

func shiftLines(lines map[string]int, delta int) {
	if delta == 0 {
		return
	}
	for k, v := range lines {
		lines[k] = v + delta
	}
}

func leadingSpaces(line string) (int, string) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	return i, line[i:]
}

func leadingSpaceCount(line string) int {
	n, _ := leadingSpaces(line)
	return n
}

func splitYAMLKey(s string) (key, value string, ok bool) {
	if s == "" {
		return "", "", false
	}
	switch s[0] {
	case '"', '\'', '{', '[', '#':
		return "", "", false
	}
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				s = strings.TrimSpace(s[:i])
				i = len(s)
			}
		case ':':
			if inSingle || inDouble {
				continue
			}
			key = strings.TrimSpace(s[:i])
			if i+1 <= len(s) {
				value = strings.TrimSpace(s[i+1:])
			}
			if key == "" || strings.ContainsAny(key, " \t{}[]&,") {
				return "", "", false
			}
			return strings.Trim(key, `"'`), value, true
		}
	}
	return "", "", false
}

func isBlockScalar(value string) bool {
	switch strings.TrimSpace(value) {
	case ">", "|", ">-", "|-", ">+", "|+", ">|", "|>":
		return true
	default:
		return false
	}
}

func recordInline(out map[string]int, parent, value string, line int) {
	for _, key := range inlineKeys(value) {
		path := joinPath(parent, key)
		if _, exists := out[path]; !exists {
			out[path] = line
		}
	}
}

func inlineKeys(value string) []string {
	s := strings.TrimSpace(value)
	start := strings.Index(s, "{")
	if start < 0 {
		return nil
	}
	s = s[start+1:]
	var b strings.Builder
	var keys []string
	inSingle, inDouble := false, false
	depth := 1
	flush := func() {
		part := strings.TrimSpace(b.String())
		b.Reset()
		if part == "" {
			return
		}
		k, _, ok := splitYAMLKey(part)
		if ok {
			keys = append(keys, k)
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
			b.WriteByte(c)
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
			b.WriteByte(c)
		case '{':
			if !inSingle && !inDouble {
				depth++
			}
			b.WriteByte(c)
		case '}':
			if !inSingle && !inDouble {
				depth--
				if depth == 0 {
					flush()
					return keys
				}
			}
			b.WriteByte(c)
		case ',':
			if !inSingle && !inDouble && depth == 1 {
				flush()
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return keys
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func lineAt(lines map[string]int, path string) int {
	for path != "" {
		if n := lines[path]; n > 0 {
			return n
		}
		i := strings.LastIndex(path, ".")
		if i < 0 {
			break
		}
		path = path[:i]
	}
	return 1
}

func inlineCredential(v string) bool {
	if m := bearerValue.FindStringSubmatch(v); m != nil && !secretRefExact.MatchString(m[1]) {
		return true
	}
	if m := basicValue.FindStringSubmatch(v); m != nil && !secretRefExact.MatchString(m[1]) {
		return true
	}
	stripped := secretRefAny.ReplaceAllString(v, "")
	return tokenPrefix.MatchString(stripped)
}

func scanMCPCredentials(file, prefix string, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	if raw == nil {
		return
	}
	mcp, _ := raw["mcpServers"].(map[string]any)
	base := prefix
	if base != "" && !strings.HasSuffix(base, ".") {
		base += "."
	}
	for name, sv := range mcp {
		srv, ok := sv.(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"headers", "env"} {
			bag, _ := srv[field].(map[string]any)
			for hk, hv := range bag {
				s, ok := hv.(string)
				if !ok || !inlineCredential(s) {
					continue
				}
				path := fmt.Sprintf("%smcpServers.%s.%s.%s", base, name, field, hk)
				addDiag(diags, file, lineAt(lines, path), SeverityError, "inline credential in "+path)
			}
		}
	}
}

func addDiag(diags *[]Diagnostic, file string, line int, severity, message string) {
	if line <= 0 {
		line = 1
	}
	*diags = append(*diags, Diagnostic{
		File:     file,
		Line:     line,
		Severity: severity,
		Message:  message,
	})
}
