// Package inference parses optional headers for an OpenAI-compatible endpoint.
// Header values are secret. Formatting, JSON, and slog show the header name
// or [REDACTED], never the value.
package inference

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"unicode"
)

// ExtraHeadersEnv is the process environment variable factoryd, the harness,
// and the M5 live preflight read. The value is newline-separated
// "Name: value" lines. Unset or empty means no extra headers.
const ExtraHeadersEnv = "FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS"

const redacted = "[REDACTED]"

const bearerPrefix = "bearer "

// Header is one extra request header. Value is unexported so it cannot be
// logged by accident; use Values to obtain secret material for redaction.
type Header struct {
	Name  string
	value headerSecret
}

type headerSecret string

func (headerSecret) String() string               { return redacted }
func (headerSecret) GoString() string             { return redacted }
func (headerSecret) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, redacted) }
func (headerSecret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (headerSecret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (headerSecret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

func (h Header) String() string             { return h.Name }
func (h Header) GoString() string           { return "inference.Header{Name:" + jsonQuote(h.Name) + "}" }
func (h Header) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, h.Name) }
func (h Header) LogValue() slog.Value       { return slog.StringValue(h.Name) }
func (h Header) MarshalText() ([]byte, error) {
	return []byte(h.Name), nil
}

func (h Header) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name string `json:"name"`
	}{Name: h.Name})
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// ParseExtraHeaders parses newline-separated "Name: value" lines.
// An empty string is valid and returns no headers. A CR anywhere is
// rejected, including CRLF line endings, because a value must not contain
// CR or LF. Malformed lines are rejected. The error names the variable
// and the line number and does not include the line text.
// Duplicate names are case-insensitive; the last value wins and keeps
// that line's spelling of the name.
func ParseExtraHeaders(raw string) ([]Header, error) {
	if strings.Contains(raw, "\r") {
		return nil, errors.New(ExtraHeadersEnv + ": CR or LF inside a header value")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	index := map[string]int{}
	var out []Header
	for n, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" || !strings.Contains(line, ":") {
			return nil, fmt.Errorf("%s: line %d is malformed", ExtraHeadersEnv, n+1)
		}
		name, value, _ := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !validHeaderName(name) || !validHeaderValue(value) {
			return nil, fmt.Errorf("%s: line %d is malformed", ExtraHeadersEnv, n+1)
		}
		key := strings.ToLower(name)
		hdr := Header{Name: name, value: headerSecret(value)}
		if i, ok := index[key]; ok {
			out[i] = hdr
			continue
		}
		index[key] = len(out)
		out = append(out, hdr)
	}
	return out, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isTChar(name[i]) {
			return false
		}
	}
	return true
}

func isTChar(c byte) bool {
	if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func validHeaderValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cc, r) {
			return false
		}
	}
	return true
}

// Format renders headers as newline-separated "Name: value" lines.
// An empty list is an empty string. ParseExtraHeaders accepts the result.
func Format(headers []Header) string {
	if len(headers) == 0 {
		return ""
	}
	var b strings.Builder
	for i, h := range headers {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(h.Name)
		b.WriteString(": ")
		b.WriteString(string(h.value))
	}
	return b.String()
}

// Values returns each header value. A value that starts with "Bearer "
// also contributes the token after that prefix, so a leaked token is
// redacted even when the "Bearer " prefix is not next to it.
func Values(headers []Header) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, h := range headers {
		v := string(h.value)
		add(v)
		if len(v) >= len(bearerPrefix) && strings.EqualFold(v[:len(bearerPrefix)], bearerPrefix) {
			add(strings.TrimSpace(v[len(bearerPrefix):]))
		}
	}
	return out
}

// Redact replaces header values of 4 bytes or more with [REDACTED].
// Longer values are replaced first.
func Redact(msg string, headers []Header) string {
	vals := Values(headers)
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		if len(v) < 4 {
			continue
		}
		msg = strings.ReplaceAll(msg, v, redacted)
	}
	return msg
}

// Apply sets the headers for one OpenAI-compatible request.
// Content-Type defaults to application/json. Authorization defaults to
// "Bearer " plus apiKey when apiKey is non-empty. An empty apiKey omits
// Authorization instead of sending an empty Bearer token.
// Each extra header is then set. A name that matches a default header
// (case-insensitive) replaces that default.
func Apply(h http.Header, apiKey string, extra []Header) {
	if h == nil {
		return
	}
	h.Set("Content-Type", "application/json")
	if apiKey != "" {
		h.Set("Authorization", "Bearer "+apiKey)
	}
	for _, hdr := range extra {
		h.Set(hdr.Name, string(hdr.value))
	}
}
