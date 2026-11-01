package operator

import (
	"strings"

	"github.com/polds/factory/internal/definition"
)

// redact strips bearer, basic, and token-prefix credentials from s.
func redact(s string) string {
	return definition.RedactInline(s)
}

func clip(s string, n int) string {
	s = redact(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n])
}
