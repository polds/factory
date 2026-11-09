package definition

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	secretRefExact = regexp.MustCompile(`^\{\{[A-Z][A-Z0-9_]*\}\}$`)
	secretRefAny   = regexp.MustCompile(`\{\{[A-Z][A-Z0-9_]*\}\}`)
	bearerValue    = regexp.MustCompile(`(?i)\bbearer\s+(\S+)`)
	basicValue     = regexp.MustCompile(`(?i)\bbasic\s+(\S+)`)
	// tokenPattern is the credential union.
	// A short sk- match needs a boundary, so ask-pass, task-runner,
	// task-id, and disk-usage stay words. A long sk- body still matches
	// when it is glued to letters. A slash continues a token only when
	// the next segment has an uppercase letter or a digit, so a URL path
	// stays put. ghs_ is a GitHub App installation token.
	tokenPattern = regexp.MustCompile(
		`\bsk-[A-Za-z0-9][A-Za-z0-9_+=.-]*|` +
			`sk-[A-Za-z0-9][A-Za-z0-9_+=.-]{15,}|` +
			`\b(?:ghp_|github_pat_|gho_|glpat-|xox[baprs]-)[A-Za-z0-9_+=.-]+` +
			`(?:/[A-Za-z0-9_+=.-]*[A-Z0-9][A-Za-z0-9_+=.-]*)?|` +
			`\bghs_[A-Za-z0-9._-]{36,}|` +
			`\bAKIA[0-9A-Z]{4,}`,
	)
)

// CredentialKind is the pattern that produced a CredentialValue.
type CredentialKind int

const (
	CredentialBearer CredentialKind = iota
	CredentialBasic
	CredentialToken
)

// CredentialValue is one matched credential. Formatting, JSON, and slog
// omit the matched text.
type CredentialValue struct {
	Kind  CredentialKind
	Value string
}

func (CredentialValue) String() string { return "[REDACTED]" }

func (CredentialValue) GoString() string { return "[REDACTED]" }

func (CredentialValue) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte("[REDACTED]"))
}

func (c CredentialValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind string `json:"kind"`
	}{Kind: c.kindName()})
}

func (c CredentialValue) LogValue() slog.Value {
	return slog.StringValue(c.kindName())
}

func (c CredentialValue) kindName() string {
	switch c.Kind {
	case CredentialBearer:
		return "bearer"
	case CredentialBasic:
		return "basic"
	case CredentialToken:
		return "token"
	default:
		panic(fmt.Sprintf("definition: unhandled credential kind %d", int(neverCredential(c.Kind))))
	}
}

func neverCredential(kind CredentialKind) CredentialKind { return kind }

// ExactSecretRef reports whether s is a whole {{SECRET_NAME}} reference.
func ExactSecretRef(s string) bool { return secretRefExact.MatchString(s) }

// CredentialValues lists bearer, basic, and token matches in s.
// A bearer or basic value that is exactly {{SECRET_NAME}} is omitted.
// A token that overlaps a {{SECRET_NAME}} span is omitted.
func CredentialValues(s string) []CredentialValue {
	var out []CredentialValue
	out = append(out, authValues(s, bearerValue, CredentialBearer)...)
	out = append(out, authValues(s, basicValue, CredentialBasic)...)
	refs := secretRefAny.FindAllStringIndex(s, -1)
	for _, span := range tokenPattern.FindAllStringIndex(s, -1) {
		if overlaps(span, refs) {
			continue
		}
		out = append(out, CredentialValue{Kind: CredentialToken, Value: s[span[0]:span[1]]})
	}
	return out
}

// RedactCredentialValues removes inline credential values. {{SECRET_NAME}}
// references and ordinary names are unchanged. A value found in a Bearer,
// Basic, or token-prefix pattern is removed everywhere in the text, including
// a later copy in prose. Trailing quotes stay. Bearer and Basic cores become
// {{REDACTED}}; token matches become REDACTED.
func RedactCredentialValues(s string) string {
	type swap struct{ from, to string }
	var reps []swap
	seen := map[string]bool{}
	add := func(from, to string) {
		if from == "" || seen[from] {
			return
		}
		seen[from] = true
		reps = append(reps, swap{from, to})
	}
	for _, cred := range CredentialValues(s) {
		switch cred.Kind {
		case CredentialBearer, CredentialBasic:
			core, _ := peelTrailer(cred.Value)
			if ExactSecretRef(core) {
				continue
			}
			add(core, "{{REDACTED}}")
		case CredentialToken:
			add(cred.Value, "REDACTED")
		default:
			panic(fmt.Sprintf("definition: unhandled credential kind %d", int(neverCredential(cred.Kind))))
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

func authValues(s string, re *regexp.Regexp, kind CredentialKind) []CredentialValue {
	var out []CredentialValue
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if len(m) != 2 || secretRefExact.MatchString(m[1]) {
			continue
		}
		out = append(out, CredentialValue{Kind: kind, Value: m[1]})
	}
	return out
}

func overlaps(span []int, spans [][]int) bool {
	for _, other := range spans {
		if span[0] < other[1] && other[0] < span[1] {
			return true
		}
	}
	return false
}

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

// InlineCredential reports whether v contains an inline credential.
// A {{SECRET_NAME}} reference is not a credential.
func InlineCredential(v string) bool {
	return inlineCredential(v)
}

func inlineCredential(v string) bool {
	return len(CredentialValues(v)) > 0
}

// RedactInline replaces bearer, basic, and token-prefix credentials.
// {{SECRET_NAME}} references are left in place. Markers stay
// "Bearer [REDACTED]", "Basic [REDACTED]", and "[REDACTED]".
func RedactInline(s string) string {
	s = replaceAuth(s, bearerValue, "Bearer [REDACTED]")
	s = replaceAuth(s, basicValue, "Basic [REDACTED]")
	return replaceTokens(s)
}

func replaceAuth(s string, re *regexp.Regexp, repl string) string {
	return re.ReplaceAllStringFunc(s, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) == 2 && secretRefExact.MatchString(parts[1]) {
			return match
		}
		return repl
	})
}

func replaceTokens(s string) string {
	refs := secretRefAny.FindAllStringIndex(s, -1)
	matches := tokenPattern.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, span := range matches {
		if overlaps(span, refs) {
			continue
		}
		b.WriteString(s[last:span[0]])
		b.WriteString("[REDACTED]")
		last = span[1]
	}
	b.WriteString(s[last:])
	return b.String()
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
