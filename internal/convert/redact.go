package convert

import (
	"github.com/polds/factory/internal/definition"
)

// redactText removes inline credential values. {{SECRET_NAME}} references
// and ordinary names are unchanged, so a clean definition file is byte-identical.
// A value found in a Bearer, Basic, or token-prefix pattern is removed
// everywhere in the text, including a later copy in prose. Trailing quotes
// stay so the YAML still parses. The matchers live in definition.CredentialValues.
func redactText(s string) string { return definition.RedactCredentialValues(s) }

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
