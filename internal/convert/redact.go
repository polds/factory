package convert

import (
	"fmt"
	"sort"
	"strings"

	"github.com/polds/factory/internal/definition"
)

// redactText removes inline credential values. {{SECRET_NAME}} references
// and ordinary names are unchanged, so a clean definition file is byte-identical.
// A value found in a Bearer, Basic, or token-prefix pattern is removed
// everywhere in the text, including a later copy in prose. Trailing quotes
// stay so the YAML still parses. The matchers live in definition.CredentialValues.
func redactText(s string) string {
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
	for _, cred := range definition.CredentialValues(s) {
		switch cred.Kind {
		case definition.CredentialBearer, definition.CredentialBasic:
			core, _ := peelTrailer(cred.Value)
			if definition.ExactSecretRef(core) {
				continue
			}
			add(core, "{{REDACTED}}")
		case definition.CredentialToken:
			add(cred.Value, "REDACTED")
		default:
			panic(fmt.Sprintf("convert: unhandled credential kind %d", int(neverCredential(cred.Kind))))
		}
	}
	sort.Slice(reps, func(i, j int) bool { return len(reps[i].from) > len(reps[j].from) })
	for _, rep := range reps {
		s = strings.ReplaceAll(s, rep.from, rep.to)
	}
	return s
}

func neverCredential(kind definition.CredentialKind) definition.CredentialKind { return kind }

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
