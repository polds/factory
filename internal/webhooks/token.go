package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
)

// ValidURLToken reports whether s is a non-empty url_token secret.
// The charset is the RFC 3986 unreserved set: A-Za-z0-9-._~
func ValidURLToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !urlTokenChar(r) {
			return false
		}
	}
	return true
}

func urlTokenChar(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '.', r == '_', r == '~':
		return true
	default:
		return false
	}
}

func validUID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// bearerMatch reports whether Authorization is "Bearer " plus secret.
// The compare hashes both sides so it does not leak the secret's length.
func bearerMatch(authorization, secret string) bool {
	const prefix = "Bearer "
	if len(authorization) > 8<<10 {
		return false
	}
	presented := authorization
	okPrefix := false
	if strings.HasPrefix(authorization, prefix) {
		presented = authorization[len(prefix):]
		okPrefix = true
	}
	sumGot := sha256.Sum256([]byte(presented))
	sumWant := sha256.Sum256([]byte(secret))
	equal := hmac.Equal(sumGot[:], sumWant[:])
	return okPrefix && equal && secret != ""
}
