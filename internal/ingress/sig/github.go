package sig

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

type github struct{ keys [][]byte }

func newGitHub(secrets []string) (*github, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &github{keys: keys}, nil
}

// Verify checks X-Hub-Signature-256, sha256 HMAC of the raw body.
// GitHub does not sign a timestamp.
func (v *github) Verify(h http.Header, body []byte, _ time.Time) error {
	sig := strings.TrimSpace(h.Get("X-Hub-Signature-256"))
	if sig == "" {
		return ErrMissing
	}
	if headerTooLong(sig) || !strings.HasPrefix(sig, "sha256=") {
		return ErrMalformed
	}
	presented, err := hex.DecodeString(strings.TrimPrefix(sig, "sha256="))
	if err != nil {
		return ErrMalformed
	}
	if !anyMAC(sha256.New, v.keys, body, [][]byte{presented}) {
		return ErrSignature
	}
	return nil
}
