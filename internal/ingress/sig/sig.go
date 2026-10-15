package sig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"
	"time"
)

// Sentinel errors. The ingress handler maps every Verify error to 401.
var (
	ErrMissing   = errors.New("sig: missing signature")
	ErrMalformed = errors.New("sig: malformed signature")
	ErrSignature = errors.New("sig: signature mismatch")
	ErrStale     = errors.New("sig: timestamp outside tolerance")
)

// Verifier checks one provider signature over the raw body.
type Verifier interface {
	Verify(h http.Header, body []byte, now time.Time) error
}

const (
	schemeGitHub           = "github"
	schemeGitLab           = "gitlab"
	schemeStripe           = "stripe"
	schemeSentry           = "sentry"
	schemePagerDuty        = "pagerduty"
	schemeVercel           = "vercel"
	schemeStandardWebhooks = "standard_webhooks"
	schemeSlack            = "slack"
	schemeLinear           = "linear"

	maxSigHeader    = 16 << 10
	stripeTolerance = 5 * time.Minute
	slackSkew       = 5 * time.Minute
	standardSkew    = 5 * time.Minute
	linearSkew      = 60 * time.Second
	whsecPrefix     = "whsec_"
)

// Open returns the verifier for a signature scheme name.
// secrets are the raw signing secrets, except standard_webhooks, which
// expects whsec_ base64 keys. A match against any secret succeeds.
func Open(scheme string, secrets ...string) (Verifier, error) {
	switch scheme {
	case schemeGitHub:
		return newGitHub(secrets)
	case schemeGitLab:
		return newGitLab(secrets)
	case schemeStripe:
		return newStripe(secrets)
	case schemeSentry:
		return newSentry(secrets)
	case schemePagerDuty:
		return newPagerDuty(secrets)
	case schemeVercel:
		return newVercel(secrets)
	case schemeStandardWebhooks:
		return newStandard(secrets)
	case schemeSlack:
		return newSlack(secrets)
	case schemeLinear:
		return newLinear(secrets)
	default:
		return nil, fmt.Errorf("sig: unknown scheme %q", scheme)
	}
}

func rawKeys(secrets []string) ([][]byte, error) {
	if len(secrets) == 0 {
		return nil, errors.New("sig: secret is required")
	}
	out := make([][]byte, 0, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			return nil, errors.New("sig: secret is required")
		}
		out = append(out, []byte(secret))
	}
	return out, nil
}

func decodeWHSecrets(secrets []string) ([][]byte, error) {
	if len(secrets) == 0 {
		return nil, errors.New("sig: secret is required")
	}
	out := make([][]byte, 0, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			return nil, errors.New("sig: secret is required")
		}
		key, err := decodeWHSecret(secret)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, nil
}

func decodeWHSecret(secret string) ([]byte, error) {
	encoded := strings.TrimPrefix(secret, whsecPrefix)
	if encoded == "" {
		return nil, errors.New("sig: secret is required")
	}
	if key, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(key) > 0 {
		return key, nil
	}
	if key, err := base64.RawStdEncoding.DecodeString(encoded); err == nil && len(key) > 0 {
		return key, nil
	}
	return nil, errors.New("sig: secret is not base64")
}

func anyMAC(h func() hash.Hash, keys [][]byte, msg []byte, presented [][]byte) bool {
	ok := false
	for _, key := range keys {
		mac := hmac.New(h, key)
		if _, err := mac.Write(msg); err != nil {
			continue
		}
		sum := mac.Sum(nil)
		for _, sig := range presented {
			if hmac.Equal(sum, sig) {
				ok = true
			}
		}
	}
	return ok
}

func equalToken(got string, keys [][]byte) bool {
	ok := false
	sumGot := sha256.Sum256([]byte(got))
	for _, key := range keys {
		sumWant := sha256.Sum256(key)
		if hmac.Equal(sumGot[:], sumWant[:]) {
			ok = true
		}
	}
	return ok
}

func headerTooLong(v string) bool {
	return len(v) > maxSigHeader
}

func stale(now, sent time.Time, tolerance time.Duration) bool {
	return now.Sub(sent).Abs() > tolerance
}

func withBody(prefix []byte, body []byte) []byte {
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix...)
	out = append(out, body...)
	return out
}
