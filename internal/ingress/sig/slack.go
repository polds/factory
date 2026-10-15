package sig

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type slack struct{ keys [][]byte }

func newSlack(secrets []string) (*slack, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &slack{keys: keys}, nil
}

// Verify checks X-Slack-Signature. The signed message is "v0:<ts>:<raw body>".
// X-Slack-Request-Timestamp must be within 5 minutes of now.
func (v *slack) Verify(h http.Header, body []byte, now time.Time) error {
	sig := strings.TrimSpace(h.Get("X-Slack-Signature"))
	ts := strings.TrimSpace(h.Get("X-Slack-Request-Timestamp"))
	if sig == "" || ts == "" {
		return ErrMissing
	}
	if headerTooLong(sig) || headerTooLong(ts) || !strings.HasPrefix(sig, "v0=") {
		return ErrMalformed
	}
	presented, err := hex.DecodeString(strings.TrimPrefix(sig, "v0="))
	if err != nil {
		return ErrMalformed
	}
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrMalformed
	}
	msg := withBody([]byte("v0:"+ts+":"), body)
	if !anyMAC(sha256.New, v.keys, msg, [][]byte{presented}) {
		return ErrSignature
	}
	if stale(now, time.Unix(sent, 0), slackSkew) {
		return ErrStale
	}
	return nil
}
