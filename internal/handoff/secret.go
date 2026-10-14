package handoff

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"

	"github.com/polds/factory/internal/harness"
)

var (
	secretRefExact = regexp.MustCompile(`^\{\{[A-Z][A-Z0-9_]*\}\}$`)
	secretRefAny   = regexp.MustCompile(`\{\{[A-Z][A-Z0-9_]*\}\}`)
	secretRefAuth  = regexp.MustCompile(`(?i)^(bearer|basic|token)\s+\{\{[A-Z][A-Z0-9_]*\}\}$`)
	schemeValue    = regexp.MustCompile(`(?i)^(bearer|basic|token)\s+(.+)$`)
)

// minSecretBytes matches harness.Redact, which ignores shorter needles.
const minSecretBytes = 4

func redactPrompt(input string, secrets []string) string {
	needles := expandSecrets(secrets)
	locs := secretRefAny.FindAllStringIndex(input, -1)
	if len(locs) == 0 {
		return harness.Redact(input, needles)
	}
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		b.WriteString(harness.Redact(input[prev:loc[0]], needles))
		b.WriteString(input[loc[0]:loc[1]])
		prev = loc[1]
	}
	b.WriteString(harness.Redact(input[prev:], needles))
	return b.String()
}

func secretValues(agent Agent, ctx Context) []string {
	var vals []string
	for _, v := range ctx.Env {
		vals = append(vals, secretMaterial(v)...)
	}
	for _, srv := range agent.MCPServers {
		for _, v := range srv.Env {
			vals = append(vals, secretMaterial(v)...)
		}
		for _, v := range srv.Headers {
			vals = append(vals, secretMaterial(v)...)
		}
	}
	return vals
}

func secretMaterial(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || isSecretRef(v) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || isSecretRef(s) {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(v)
	if m := schemeValue.FindStringSubmatch(v); m != nil {
		add(cleanCredential(m[2]))
	}
	for _, pw := range userinfoPasswords(v) {
		add(pw)
	}
	return out
}

func isSecretRef(v string) bool {
	return secretRefExact.MatchString(v) || secretRefAuth.MatchString(v)
}

// cleanCredential strips wrapping quotes and trailing punctuation so a header
// value like `Bearer "token",` yields the bare credential.
func cleanCredential(s string) string {
	s = strings.TrimSpace(s)
	s = trimTrailingPunct(s)
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = trimTrailingPunct(strings.TrimSpace(s[1 : len(s)-1]))
			s = strings.TrimSpace(s)
		}
	}
	return s
}

func trimTrailingPunct(s string) string {
	return strings.TrimRight(s, ".,;:)]}")
}

func userinfoPasswords(v string) []string {
	u, err := url.Parse(v)
	if err != nil || u.User == nil {
		return nil
	}
	var out []string
	if pw, ok := u.User.Password(); ok && pw != "" {
		out = append(out, pw)
	}
	info := u.User.String()
	if i := strings.Index(info, ":"); i >= 0 {
		raw := info[i+1:]
		if raw != "" && (len(out) == 0 || raw != out[0]) {
			out = append(out, raw)
		}
	}
	return out
}

// expandSecrets adds the encodings prior-stage text can contain. The full
// encoded form is longer than a short prefix of the same secret, so
// harness.Redact's longest match cannot stop after that prefix.
func expandSecrets(secrets []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if len(s) < minSecretBytes || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, secret := range secrets {
		if len(secret) < minSecretBytes {
			continue
		}
		for _, form := range encodingForms(secret) {
			add(form)
			add(lowerHexEscapes(form))
			add(upperHexEscapes(form))
		}
		for _, form := range base64Forms(secret) {
			add(form)
		}
	}
	return out
}

func encodingForms(secret string) []string {
	query := url.QueryEscape(secret)
	return []string{
		secret,
		query,
		strings.ReplaceAll(query, "+", "%20"),
		url.PathEscape(secret),
	}
}

func base64Forms(secret string) []string {
	raw := []byte(secret)
	return []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	}
}

func lowerHexEscapes(s string) string {
	return recaseHexEscapes(s, false)
}

func upperHexEscapes(s string) string {
	return recaseHexEscapes(s, true)
}

func recaseHexEscapes(s string, upper bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte('%')
			b.WriteByte(recaseHex(s[i+1], upper))
			b.WriteByte(recaseHex(s[i+2], upper))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func recaseHex(c byte, upper bool) byte {
	if upper {
		if c >= 'a' && c <= 'f' {
			return c - ('a' - 'A')
		}
		return c
	}
	if c >= 'A' && c <= 'F' {
		return c + ('a' - 'A')
	}
	return c
}
