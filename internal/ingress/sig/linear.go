package sig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type linear struct{ keys [][]byte }

func newLinear(secrets []string) (*linear, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &linear{keys: keys}, nil
}

// Verify checks Linear-Signature, hex HMAC-SHA256 of the raw body.
// webhookTimestamp, in milliseconds, is read only after the HMAC matches
// and must be within 60 seconds of now.
func (v *linear) Verify(h http.Header, body []byte, now time.Time) error {
	sig := strings.TrimSpace(h.Get("Linear-Signature"))
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
	var payload struct {
		WebhookTimestamp *int64 `json:"webhookTimestamp"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.WebhookTimestamp == nil {
		return ErrMalformed
	}
	if stale(now, time.UnixMilli(*payload.WebhookTimestamp), linearSkew) {
		return ErrStale
	}
	return nil
}
