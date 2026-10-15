package handoff

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	secretRefExact = regexp.MustCompile(`^\{\{[A-Z][A-Z0-9_]*\}\}$`)
	secretRefAny   = regexp.MustCompile(`\{\{[A-Z][A-Z0-9_]*\}\}`)
	secretRefAuth  = regexp.MustCompile(`(?i)^(bearer|basic|token)\s+\{\{[A-Z][A-Z0-9_]*\}\}$`)
	schemeValue    = regexp.MustCompile(`(?i)^(bearer|basic|token)\s+(.+)$`)
	basicEncodings = []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
)

// minSecretBytes matches harness.Redact, which ignores shorter needles.
const minSecretBytes = 4

const redactedMark = "[REDACTED]"

// redactPrompt redacts needles in input. Percent-encoded hex digits match
// in any case. A {{SECRET_NAME}} span is copied through when a match sits
// entirely inside it, so a value such as TOKEN cannot rewrite {{GH_TOKEN}}.
// A match that contains the span is removed whole, including the placeholder.
func redactPrompt(input string, secrets []string) string {
	pats := redactPatterns(secrets)
	if len(pats) == 0 || input == "" {
		return input
	}
	spans := secretRefAny.FindAllStringIndex(input, -1)
	var b strings.Builder
	b.Grow(len(input))
	for i := 0; i < len(input); {
		if n := matchNeedle(input, i, pats, spans); n > 0 {
			b.WriteString(redactedMark)
			i += n
			continue
		}
		b.WriteByte(input[i])
		i++
	}
	return b.String()
}

func matchNeedle(input string, i int, pats []string, spans [][]int) int {
	for _, pat := range pats {
		n, ok := matchAt(input, i, pat)
		if !ok || insideSpan(i, i+n, spans) {
			continue
		}
		return n
	}
	return 0
}

func matchAt(input string, i int, pat string) (int, bool) {
	if len(pat) == 0 || i < 0 || i > len(input)-len(pat) {
		return 0, false
	}
	got := input[i : i+len(pat)]
	if got == pat || hexFoldEqual(got, pat) {
		return len(pat), true
	}
	return 0, false
}

// hexFoldEqual reports whether got and pat are equal when hex digits in
// pat's %HH escapes are compared case-insensitively. Other bytes are exact.
func hexFoldEqual(got, pat string) bool {
	if len(got) != len(pat) {
		return false
	}
	for i := 0; i < len(pat); i++ {
		if got[i] == pat[i] {
			continue
		}
		if hexDigitOfEscape(pat, i) && isHex(got[i]) && foldHex(got[i]) == foldHex(pat[i]) {
			continue
		}
		return false
	}
	return true
}

// hexDigitOfEscape reports whether pat[i] is a hex digit of a %HH escape,
// including the inner hex digits of a double-encoded escape. QueryEscape
// turns %HH into %25HH, so those two digits are no longer part of a %HH
// in the pattern and a mixed-case form such as %252f would not match %252F.
func hexDigitOfEscape(pat string, i int) bool {
	if hexDigitOfSingleEscape(pat, i) || hexDigitOfDoubleEncodedEscape(pat, i) {
		return true
	}
	return false
}

func hexDigitOfSingleEscape(pat string, i int) bool {
	if i+1 < len(pat) && i >= 1 && pat[i-1] == '%' && isHex(pat[i]) && isHex(pat[i+1]) {
		return true
	}
	return i >= 2 && pat[i-2] == '%' && isHex(pat[i-1]) && isHex(pat[i])
}

func hexDigitOfDoubleEncodedEscape(pat string, i int) bool {
	if i >= 3 && i+1 < len(pat) && pat[i-3:i] == "%25" && isHex(pat[i]) && isHex(pat[i+1]) {
		return true
	}
	return i >= 4 && pat[i-4:i-1] == "%25" && isHex(pat[i-1]) && isHex(pat[i])
}

func foldHex(c byte) byte {
	if c >= 'A' && c <= 'F' {
		return c + ('a' - 'A')
	}
	return c
}

func insideSpan(start, end int, spans [][]int) bool {
	for _, sp := range spans {
		if start >= sp[0] && end <= sp[1] {
			return true
		}
		if sp[0] >= end {
			return false
		}
	}
	return false
}

// redactPatterns adds the QueryEscape and JSON-string forms harness.Redact
// registers, then orders longest-first so a short prefix cannot win.
func redactPatterns(secrets []string) []string {
	seen := map[string]bool{}
	var pats []string
	add := func(p string) {
		if len(p) < minSecretBytes || p == "" || seen[p] {
			return
		}
		seen[p] = true
		pats = append(pats, p)
	}
	for _, secret := range expandSecrets(secrets) {
		add(secret)
		add(url.QueryEscape(secret))
		add(jsonStringContents(secret))
	}
	sort.Slice(pats, func(i, j int) bool { return len(pats[i]) > len(pats[j]) })
	return pats
}

// jsonStringContents is the interior of json.Marshal(s), the same extra
// pattern harness.Redact registers for each needle.
func jsonStringContents(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return ""
	}
	return string(b[1 : len(b)-1])
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
	for _, part := range secretParts(v) {
		addPart(part, add)
	}
	return out
}

// secretParts returns v and, when v is split across lines, each line.
// A multi-line env value is often several secrets, and the prompt can
// contain any one of them.
func secretParts(v string) []string {
	if !strings.ContainsAny(v, "\r\n") {
		return []string{v}
	}
	lines := strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == '\r' })
	out := make([]string, 0, len(lines)+1)
	out = append(out, v)
	for _, line := range lines {
		if line != v {
			out = append(out, line)
		}
	}
	return out
}

func addPart(v string, add func(string)) {
	v = strings.TrimSpace(v)
	if v == "" || isSecretRef(v) {
		return
	}
	add(v)
	if m := schemeValue.FindStringSubmatch(v); m != nil {
		cred := cleanCredential(m[2])
		add(cred)
		if strings.EqualFold(m[1], "basic") {
			addDecodedBasic(cred, add)
		}
	}
	for _, pw := range userinfoPasswords(v) {
		add(pw)
	}
}

func addDecodedBasic(cred string, add func(string)) {
	decoded, ok := decodeBasic(cred)
	if !ok {
		return
	}
	add(decoded)
	if _, pass, found := strings.Cut(decoded, ":"); found {
		add(pass)
	}
}

func decodeBasic(cred string) (string, bool) {
	for _, enc := range basicEncodings {
		b, err := enc.DecodeString(cred)
		if err != nil {
			continue
		}
		if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
			return "", false
		}
		return string(b), true
	}
	return "", false
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
	if out := passwordsFromURL(v); len(out) > 0 {
		return out
	}
	// user:pass@host has no scheme, so url.Parse treats the user as the scheme.
	// Prepending "//" also drops a password that contains '/', '#', '?', or a
	// space, and it invents one for a scheme-opaque value such as mailto:.
	if strings.Contains(v, "://") || !strings.Contains(v, "@") || mailtoScheme(v) {
		return nil
	}
	return splitSchemelessPassword(v)
}

func mailtoScheme(v string) bool {
	u, err := url.Parse(v)
	return err == nil && strings.EqualFold(u.Scheme, "mailto")
}

// splitSchemelessPassword splits user:pass@host on the last '@' and the first
// ':'. Reserved characters stay in the password. The raw userinfo password
// and its percent-decoded form are both returned.
func splitSchemelessPassword(v string) []string {
	at := strings.LastIndex(v, "@")
	if at <= 0 {
		return nil
	}
	info := v[:at]
	colon := strings.IndexByte(info, ':')
	if colon < 0 || colon == len(info)-1 {
		return nil
	}
	raw := info[colon+1:]
	out := []string{raw}
	dec, err := url.PathUnescape(raw)
	if err == nil && dec != "" && dec != raw {
		out = append(out, dec)
	}
	return out
}

func passwordsFromURL(v string) []string {
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
// encoded form is longer than a short prefix of the same secret, so the
// longest match cannot stop after that prefix.
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
	forms := []string{
		secret,
		query,
		strings.ReplaceAll(query, "+", "%20"),
		url.PathEscape(secret),
	}
	return append(forms, encodingBesidePlaceholders(secret)...)
}

// encodingBesidePlaceholders percent-encodes the bytes outside a
// {{SECRET_NAME}} span and leaves the span literal. QueryEscape of the
// whole secret encodes the braces, so a%2Fb{{GH_TOKEN}} matches neither
// the raw secret nor that fully encoded form.
func encodingBesidePlaceholders(secret string) []string {
	spans := secretRefAny.FindAllStringIndex(secret, -1)
	if len(spans) == 0 {
		return nil
	}
	query := encodeOutsidePlaceholders(secret, spans, url.QueryEscape)
	path := encodeOutsidePlaceholders(secret, spans, url.PathEscape)
	return []string{query, path, strings.ReplaceAll(query, "+", "%20")}
}

func encodeOutsidePlaceholders(secret string, spans [][]int, esc func(string) string) string {
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(esc(secret[prev:sp[0]]))
		b.WriteString(secret[sp[0]:sp[1]])
		prev = sp[1]
	}
	b.WriteString(esc(secret[prev:]))
	return b.String()
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
