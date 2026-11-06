package secrets

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
)

const (
	// CanaryOld and CanaryNew are synthetic witness labels from defined-factory/1.
	CanaryOld       = "CANARY-OLD"
	CanaryNew       = "CANARY-NEW"
	CanarySynthetic = "CANARY-SYNTHETIC"
)

// Redactor removes secret material from text, JSON, and structured logs while
// retaining active rotation canaries (old and new values) as redaction needles.
type Redactor struct {
	mu      sync.RWMutex
	needles []string
}

// NewRedactor returns a redactor with no active secret needles.
func NewRedactor() *Redactor { return &Redactor{} }

// Observe registers secret material seen at injection time. When value changes,
// the previous value stays in the needle set until ClearRetired is called.
func (r *Redactor) Observe(name, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.needles {
		if n == value {
			return
		}
	}
	r.needles = append(r.needles, value)
}

// RetainCanaries adds synthetic witness labels used in contract tests.
func (r *Redactor) RetainCanaries(oldValue, newValue string) {
	if strings.TrimSpace(oldValue) != "" {
		r.Observe(CanaryOld, oldValue)
	}
	if strings.TrimSpace(newValue) != "" {
		r.Observe(CanaryNew, newValue)
	}
}

// Needles returns a copy of active needles (for tests).
func (r *Redactor) Needles() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.needles...)
	return out
}

// Text redacts inline credentials and known needles from s.
func (r *Redactor) Text(s string) string {
	s = redactCredentialPatterns(s)
	s = harness.Redact(s, r.Needles())
	return s
}

// JSON redacts string values in raw JSON text.
func (r *Redactor) JSON(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return r.Text(raw)
	}
	red := redactJSONValue(v, r)
	out, err := json.Marshal(red)
	if err != nil {
		return r.Text(raw)
	}
	return string(out)
}

// Event formats a run event payload with redaction applied.
func (r *Redactor) Event(tool, redactedInput string) string {
	return fmt.Sprintf("tool.call tool=%s redacted_input=%s", tool, r.Text(redactedInput))
}

// SlogAttr returns a redacted slog attribute for arbitrary text fields.
func (r *Redactor) SlogAttr(key, value string) slog.Attr {
	return slog.String(key, r.Text(value))
}

func redactJSONValue(v any, r *Redactor) any {
	switch t := v.(type) {
	case string:
		return r.Text(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = redactJSONValue(child, r)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = redactJSONValue(child, r)
		}
		return out
	default:
		return v
	}
}

func redactCredentialPatterns(s string) string {
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
			panic(fmt.Sprintf("secrets: unhandled credential kind %d", int(neverCredentialKind(cred.Kind))))
		}
	}
	sort.Slice(reps, func(i, j int) bool { return len(reps[i].from) > len(reps[j].from) })
	for _, rep := range reps {
		s = strings.ReplaceAll(s, rep.from, rep.to)
	}
	return s
}

func peelTrailer(token string) (string, string) {
	// Copied from internal/convert/redact.go. convert is outside M10-S paths,
	// so this stays a local copy until a later consolidation pass.
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

func neverCredentialKind(kind definition.CredentialKind) definition.CredentialKind { return kind }
