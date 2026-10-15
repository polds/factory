package ingress

import (
	"net/http"
	"strings"
)

// headerAllow is the persisted header allowlist. Credential and signature
// headers are absent on purpose: a denylist would keep a new signing header.
var headerAllow = map[string]struct{}{
	"Content-Type":                           {},
	"User-Agent":                             {},
	"X-Github-Delivery":                      {},
	"X-Github-Event":                         {},
	"X-Github-Hook-Id":                       {},
	"X-Github-Hook-Installation-Target-Id":   {},
	"X-Github-Hook-Installation-Target-Type": {},
	"X-Gitlab-Event":                         {},
	"X-Gitlab-Event-Uuid":                    {},
	"X-Slack-Request-Timestamp":              {},
	"X-Slack-Retry-Num":                      {},
	"X-Slack-Retry-Reason":                   {},
	"Linear-Delivery":                        {},
	"Linear-Event":                           {},
	"Linear-Timestamp":                       {},
	"Webhook-Id":                             {},
	"Webhook-Timestamp":                      {},
	"Sentry-Hook-Resource":                   {},
	"Sentry-Hook-Timestamp":                  {},
	"Request-Id":                             {},
	"X-Request-Id":                           {},
	"X-Webhook-Id":                           {},
}

// RedactHeaders copies the allowlisted header values.
// Authorization, Cookie, and signature headers are dropped.
func RedactHeaders(h http.Header) map[string]string {
	out := make(map[string]string)
	if h == nil {
		return out
	}
	for name := range headerAllow {
		if sensitiveHeader(name) {
			continue
		}
		vals := h.Values(name)
		if len(vals) == 0 {
			continue
		}
		out[name] = strings.Join(vals, ", ")
	}
	return out
}

func sensitiveHeader(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == "authorization", n == "cookie", n == "proxy-authorization":
		return true
	case strings.Contains(n, "signature"), strings.Contains(n, "secret"), strings.Contains(n, "token"):
		return true
	default:
		return false
	}
}
