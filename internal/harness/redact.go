package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const redacted = "[REDACTED]"

// Digest is the sha256 of the original bytes, prefixed for event correlation.
func Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Redact replaces each secret, its query-escaped form, and its JSON-string
// escaped form. Longer secrets win at each byte so a short secret cannot
// punch a hole in a longer one. Secrets shorter than 4 bytes are ignored.
func Redact(input string, secrets []string) string {
	pats := secretPatterns(secrets)
	if len(pats) == 0 || input == "" {
		return input
	}
	var b strings.Builder
	b.Grow(len(input))
	for i := 0; i < len(input); {
		hit := -1
		for pi, p := range pats {
			if strings.HasPrefix(input[i:], p) {
				hit = pi
				break
			}
		}
		if hit >= 0 {
			b.WriteString(redacted)
			i += len(pats[hit])
			continue
		}
		b.WriteByte(input[i])
		i++
	}
	return b.String()
}

func secretPatterns(secrets []string) []string {
	seen := map[string]bool{}
	var pats []string
	for _, secret := range secrets {
		if len(secret) < 4 {
			continue
		}
		for _, p := range []string{secret, url.QueryEscape(secret), jsonStringContents(secret)} {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			pats = append(pats, p)
		}
	}
	sort.Slice(pats, func(i, j int) bool { return len(pats[i]) > len(pats[j]) })
	return pats
}

func jsonStringContents(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return ""
	}
	return string(b[1 : len(b)-1])
}

// hostRE and segRE match spike/lib/remote.py. A public definition URL is
// not a secret there, and the harness uses the same shape.
var (
	hostRE = regexp.MustCompile(`^[A-Za-z0-9.-]+(?::[0-9]+)?$`)
	segRE  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// RedactionNeedles is the list harness.Redact should apply. A public https
// repository URL, its pull-request URL, and a bare https origin are not
// needles: live stores the target repo URL as DEFINITION_GIT_URL, and that
// string is a prefix of the pull request html_url. A credential-bearing URL
// stays a needle, and so does its userinfo password, so the token is masked
// when it appears without the rest of the URL.
func RedactionNeedles(values []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) < 4 || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, value := range values {
		for _, pw := range userinfoSecrets(value) {
			add(pw)
		}
		if publicHTTPSURL(value) {
			continue
		}
		add(value)
	}
	return out
}

// publicHTTPSURL reports a credential-free https locator. It matches
// remote.public_definition_url and remote.public_https_origin: no userinfo,
// query, or fragment, and no character urlsplit would delete.
func publicHTTPSURL(raw string) bool {
	text := strings.TrimSpace(raw)
	if text == "" || hasControl(text) {
		return false
	}
	u, err := url.Parse(text)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") || !hostRE.MatchString(u.Host) {
		return false
	}
	if u.Path == "" || u.Path == "/" {
		return true
	}
	parts := repoPath(u.Path)
	if len(parts) == 2 {
		return true
	}
	return len(parts) == 4 && parts[2] == "pull" && digitsOnly(parts[3])
}

func repoPath(path string) []string {
	path = strings.TrimSuffix(path, ".git")
	var parts []string
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		if !segRE.MatchString(part) {
			return nil
		}
		parts = append(parts, part)
	}
	return parts
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 32 {
			return true
		}
	}
	return false
}

// userinfoSecrets returns the decoded password and the raw userinfo
// password. The username is not a credential. User.String re-encodes, so
// the raw password is the text between ':' and '@' in the original URL.
func userinfoSecrets(raw string) []string {
	text := strings.TrimSpace(raw)
	if text == "" || hasControl(text) {
		return nil
	}
	u, err := url.Parse(text)
	if err != nil || u.User == nil {
		return nil
	}
	var out []string
	if pw, ok := u.User.Password(); ok && pw != "" {
		out = append(out, pw)
	}
	if encoded := rawUserinfoPassword(text); encoded != "" && (len(out) == 0 || encoded != out[0]) {
		out = append(out, encoded)
	}
	return out
}

func rawUserinfoPassword(text string) string {
	rest := text
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	at := strings.IndexByte(rest, '@')
	if at < 0 {
		return ""
	}
	info := rest[:at]
	colon := strings.IndexByte(info, ':')
	if colon < 0 || colon+1 >= len(info) {
		return ""
	}
	return info[colon+1:]
}
