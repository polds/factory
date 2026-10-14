package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/polds/factory/internal/api"
)

// problemSSE turns pre-stream handler errors into problem+json. Once the
// response is an event stream, bytes pass through unchanged.
func problemSSE(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&problemWriter{ResponseWriter: w}, r)
	})
}

type problemWriter struct {
	http.ResponseWriter
	code    int
	sent    bool
	decided bool
}

func (p *problemWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

func (p *problemWriter) Flush() {
	if !p.sent {
		return
	}
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *problemWriter) WriteHeader(code int) {
	if p.decided {
		return
	}
	p.code = code
	p.decided = true
	if code >= 400 && !strings.Contains(p.Header().Get("Content-Type"), "text/event-stream") {
		return
	}
	p.ResponseWriter.WriteHeader(code)
	p.sent = true
}

func (p *problemWriter) Write(b []byte) (int, error) {
	if !p.decided {
		p.WriteHeader(http.StatusOK)
	}
	if p.code >= 400 && !p.sent {
		writeProblem(p.ResponseWriter, p.code, strings.TrimSpace(string(b)))
		p.sent = true
		return len(b), nil
	}
	return p.ResponseWriter.Write(b)
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	body, err := json.Marshal(api.Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Del("X-Content-Type-Options")
	h.Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
