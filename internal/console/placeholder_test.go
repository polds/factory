//go:build !console

package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlaceholder(t *testing.T) {
	h := Handler(http.NotFoundHandler())
	for _, path := range []string{"/", "/runs/x"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Fatalf("content-type %q", ct)
			}
			if !strings.Contains(rec.Body.String(), "Console not built") {
				t.Fatalf("body %q", rec.Body.String())
			}
		})
	}
}
