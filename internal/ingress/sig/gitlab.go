package sig

import (
	"net/http"
	"time"
)

type gitlab struct{ keys [][]byte }

func newGitLab(secrets []string) (*gitlab, error) {
	keys, err := rawKeys(secrets)
	if err != nil {
		return nil, err
	}
	return &gitlab{keys: keys}, nil
}

// Verify compares X-Gitlab-Token in constant time.
// The token does not cover the body. GitLab's HMAC signing token is the
// standard_webhooks scheme, not this one.
func (v *gitlab) Verify(h http.Header, _ []byte, _ time.Time) error {
	token := h.Get("X-Gitlab-Token")
	if token == "" {
		return ErrMissing
	}
	if headerTooLong(token) || !equalToken(token, v.keys) {
		return ErrSignature
	}
	return nil
}
