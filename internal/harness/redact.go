package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
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
