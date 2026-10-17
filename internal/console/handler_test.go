package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIForwarded(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("X-Reached", "api")
		w.WriteHeader(http.StatusTeapot)
	})
	h := Handler(api)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz?ready=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("X-Reached") != "api" || gotMethod != http.MethodGet || gotPath != "/api/v1/healthz" || gotQuery != "ready=1" {
		t.Fatalf("forwarded %s %s?%s header %q", gotMethod, gotPath, gotQuery, rec.Header().Get("X-Reached"))
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/healthz", strings.NewReader("x"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || gotMethod != http.MethodPost {
		t.Fatalf("post status %d method %s", rec.Code, gotMethod)
	}

	req = httptest.NewRequest(http.MethodGet, "/runs/../../api/v1/healthz", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || gotPath != "/api/v1/healthz" {
		t.Fatalf("cleaned path status %d path %s", rec.Code, gotPath)
	}
}

func TestStaticMethodNotAllowed(t *testing.T) {
	h := Handler(http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodPost, "/runs/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Allow"), http.MethodGet) {
		t.Fatalf("Allow %q", rec.Header().Get("Allow"))
	}
}

func TestNilAPI(t *testing.T) {
	h := Handler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("status %d body %s", rec.Code, body)
	}
}
