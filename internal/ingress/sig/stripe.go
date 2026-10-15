package sig

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type stripe struct{ keys [][]byte }

func newStripe(secrets []string) (*stripe, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &stripe{keys: keys}, nil
}

// Verify checks Stripe-Signature. The signed message is "<t>.<raw body>".
// v1 signatures match; v0 is ignored. The secret is the raw endpoint secret,
// including a whsec_ prefix. Skew beyond 5 minutes in either direction fails.
func (v *stripe) Verify(h http.Header, body []byte, now time.Time) error {
	header := h.Get("Stripe-Signature")
	if strings.TrimSpace(header) == "" {
		return ErrMissing
	}
	if headerTooLong(header) {
		return ErrMalformed
	}
	ts, sigs, err := parseStripe(header)
	if err != nil {
		return err
	}
	msg := withBody([]byte(ts+"."), body)
	if !anyMAC(sha256.New, v.keys, msg, sigs) {
		return ErrSignature
	}
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrMalformed
	}
	if stale(now, time.Unix(sent, 0), stripeTolerance) {
		return ErrStale
	}
	return nil
}

func parseStripe(header string) (string, [][]byte, error) {
	var ts string
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		k, val, ok := strings.Cut(part, "=")
		if !ok || k == "" {
			return "", nil, ErrMalformed
		}
		switch k {
		case "t":
			if ts != "" || val == "" {
				return "", nil, ErrMalformed
			}
			ts = val
		case "v1":
			raw, err := hex.DecodeString(val)
			if err != nil {
				return "", nil, ErrMalformed
			}
			sigs = append(sigs, raw)
		default:
		}
	}
	if ts == "" || len(sigs) == 0 {
		return "", nil, ErrMalformed
	}
	if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
		return "", nil, ErrMalformed
	}
	return ts, sigs, nil
}
