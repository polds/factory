package webhooks

import (
	"log/slog"
	"net/http"
	"strings"
)

const (
	// PathPrefix is the uid route, POST /api/v1/webhooks/{webhook_uid}.
	PathPrefix = "/api/v1/webhooks/"
	// URLTokenPrefix is the url_token route. The next path segment is the secret.
	URLTokenPrefix = "/api/v1/webhooks/u/"
)

const redactedToken = "{redacted}"

// RedactRequestURI returns the request path with the url_token segment
// replaced by "{redacted}". The query string and fragment are dropped, so a
// copy of the token in the query, including a percent-encoded copy, is not
// logged.
func RedactRequestURI(uri string) string {
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	return replaceURLTokenSegment(uri)
}

// AccessLog writes one line per request.
// It records the method, the redacted path, and the status.
// The query string, fragment, headers, and body are omitted.
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
