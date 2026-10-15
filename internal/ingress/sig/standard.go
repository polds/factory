package sig

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type standard struct{ keys [][]byte }

func newStandard(secrets []string) (*standard, error) {
	keys, err := decodeWHSecrets(secrets)
	if err != nil {
		return nil, err
	}
	return &standard{keys: keys}, nil
}

// Verify checks the Standard Webhooks headers webhook-id, webhook-timestamp,
// and webhook-signature. The signed message is "<id>.<timestamp>.<raw body>".
// Signatures are space-separated v1,<base64> values. Skew beyond 5 minutes
// in either direction fails. Non-v1 versions are ignored.
func (v *standard) Verify(h http.Header, body []byte, now time.Time) error {
	id := strings.TrimSpace(h.Get("Webhook-Id"))
	ts := strings.TrimSpace(h.Get("Webhook-Timestamp"))
	sig := h.Get("Webhook-Signature")
	if id == "" || ts == "" || strings.TrimSpace(sig) == "" {
		return ErrMissing
	}
	if headerTooLong(id) || headerTooLong(ts) || headerTooLong(sig) {
		return ErrMalformed
	}
	sigs, err := parseStandard(sig)
	if err != nil {
		return err
	}
	msg := withBody([]byte(id+"."+ts+"."), body)
	if !anyMAC(sha256.New, v.keys, msg, sigs) {
		return ErrSignature
	}
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrMalformed
	}
	if stale(now, time.Unix(sent, 0), standardSkew) {
		return ErrStale
	}
	return nil
}

func parseStandard(header string) ([][]byte, error) {
	var sigs [][]byte
	sawVersion := false
	for _, part := range strings.Fields(header) {
		ver, enc, ok := strings.Cut(part, ",")
		if !ok {
			return nil, ErrMalformed
		}
		if ver != "v1" {
			sawVersion = true
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(enc)
		}
		if err != nil {
			continue
		}
		sigs = append(sigs, raw)
	}
	if len(sigs) == 0 {
		if sawVersion {
			return nil, ErrSignature
		}
		return nil, ErrMalformed
	}
	return sigs, nil
}
