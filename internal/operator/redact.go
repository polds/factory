package operator

import (
	"strings"

	"github.com/polds/factory/internal/definition"
)

// redact strips bearer, basic, and token-prefix credentials from s.
func redact(s string) string {
	return definition.RedactInline(s)
}

// Redact strips bearer, basic, and token-prefix credentials from s.
func Redact(s string) string { return redact(s) }

// Clip redacts s and limits it to n bytes.
func Clip(s string, n int) string { return clip(s, n) }

func clip(s string, n int) string {
	s = redact(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n])
}
