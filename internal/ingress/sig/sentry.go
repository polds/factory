package sig

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

type sentry struct{ keys [][]byte }

func newSentry(secrets []string) (*sentry, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &sentry{keys: keys}, nil
}

// Verify checks Sentry-Hook-Signature, hex HMAC-SHA256 of the raw body.
// Sentry's published check does not use Sentry-Hook-Timestamp.
func (v *sentry) Verify(h http.Header, body []byte, _ time.Time) error {
	sig := strings.TrimSpace(h.Get("Sentry-Hook-Signature"))
	if sig == "" {
		return ErrMissing
	}
	if headerTooLong(sig) {
		return ErrMalformed
	}
	presented, err := hex.DecodeString(sig)
	if err != nil {
		return ErrMalformed
	}
	if !anyMAC(sha256.New, v.keys, body, [][]byte{presented}) {
		return ErrSignature
	}
	return nil
}
