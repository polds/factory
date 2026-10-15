package sig

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

type vercel struct{ keys [][]byte }

func newVercel(secrets []string) (*vercel, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &vercel{keys: keys}, nil
}

// Verify checks X-Vercel-Signature, hex HMAC-SHA1 of the raw body.
func (v *vercel) Verify(h http.Header, body []byte, _ time.Time) error {
	sig := strings.TrimSpace(h.Get("X-Vercel-Signature"))
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
	if !anyMAC(sha1.New, v.keys, body, [][]byte{presented}) {
		return ErrSignature
	}
	return nil
}
