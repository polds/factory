package webhooks

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const (
	// PathPrefix is the uid route, POST /api/v1/webhooks/{webhook_uid}.
	PathPrefix = "/api/v1/webhooks/"
	// URLTokenPrefix is the url_token route. The next path segment is the secret.
	URLTokenPrefix = "/api/v1/webhooks/u/"
)

const redactedToken = "{redacted}"

// RedactRequestURI replaces the url_token path segment with "{redacted}".
// A query value equal to that segment is replaced too. Other characters are
// left alone, including a short token that happens to occur inside a word.
func RedactRequestURI(uri string) string {
	token := urlTokenSegment(uri)
	out := replaceURLTokenSegment(uri)
	if token == "" || token == redactedToken {
		return out
	}
	out = replaceQueryValue(out, token)
	if decoded, err := url.PathUnescape(token); err == nil && decoded != "" && decoded != token {
		out = replaceQueryValue(out, decoded)
	}
	return out
}

// AccessLog writes one line per request with a redacted URI.
// It records the method, the redacted request URI, and the status.
// It does not record headers or the body.
func AccessLog(log *slog.Logger, next http.Handler) http.Handler {
	if log == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		uri := ""
		if r.URL != nil {
			uri = r.URL.RequestURI()
		}
		if r.RequestURI != "" {
			uri = r.RequestURI
		}
		log.Info("webhook access",
			"http_method", r.Method,
			"path", RedactRequestURI(uri),
			"status", rec.code,
		)
	})
}

func urlTokenSegment(uri string) string {
	i := strings.Index(uri, URLTokenPrefix)
	if i < 0 {
		return ""
	}
	rest := uri[i+len(URLTokenPrefix):]
	return rest[:segmentEnd(rest)]
}

func replaceURLTokenSegment(uri string) string {
	var b strings.Builder
	rest := uri
	for {
		i := strings.Index(rest, URLTokenPrefix)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+len(URLTokenPrefix)])
		rest = rest[i+len(URLTokenPrefix):]
		end := segmentEnd(rest)
		if end > 0 {
			b.WriteString(redactedToken)
		}
		rest = rest[end:]
	}
}

func segmentEnd(rest string) int {
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '/', '?', '#':
			return i
		}
	}
	return len(rest)
}

func replaceQueryValue(uri, token string) string {
	q := strings.Index(uri, "?")
	if q < 0 || token == "" {
		return uri
	}
	raw := uri[q+1:]
	frag := ""
	if h := strings.Index(raw, "#"); h >= 0 {
		frag = raw[h:]
		raw = raw[:h]
	}
	parts := strings.Split(raw, "&")
	for i, part := range parts {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			if part == token {
				parts[i] = redactedToken
			}
			continue
		}
		if val == token {
			parts[i] = key + "=" + redactedToken
		}
	}
	return uri[:q+1] + strings.Join(parts, "&") + frag
}

type statusRecorder struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wrote {
		return
	}
	s.wrote = true
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if !s.wrote {
		s.code = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(p)
}
