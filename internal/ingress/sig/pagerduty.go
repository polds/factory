package sig

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

type pagerduty struct{ keys [][]byte }

func newPagerDuty(secrets []string) (*pagerduty, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &pagerduty{keys: keys}, nil
}

// Verify checks X-PagerDuty-Signature. The header is a comma-separated list
// of v1= hex HMACs over the raw body. Any v1 match is enough.
func (v *pagerduty) Verify(h http.Header, body []byte, _ time.Time) error {
	header := h.Get("X-PagerDuty-Signature")
	if strings.TrimSpace(header) == "" {
		return ErrMissing
	}
	if headerTooLong(header) {
		return ErrMalformed
	}
	sigs, err := parsePagerDuty(header)
	if err != nil {
		return err
	}
	if !anyMAC(sha256.New, v.keys, body, sigs) {
		return ErrSignature
	}
	return nil
}

func parsePagerDuty(header string) ([][]byte, error) {
	var sigs [][]byte
	saw := false
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		saw = true
		if !strings.HasPrefix(part, "v1=") {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(part, "v1="))
		if err != nil {
			continue
		}
		sigs = append(sigs, raw)
	}
	if len(sigs) == 0 {
		if !saw {
			return nil, ErrMissing
		}
		return nil, ErrMalformed
	}
	return sigs, nil
}
