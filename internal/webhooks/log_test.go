package webhooks

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedactRequestURI(t *testing.T) {
	t.Parallel()
	const token = "url-token-z9q.ABC_~"
	cases := []struct {
		in   string
		want string
	}{
		{in: "/api/v1/webhooks/u/" + token, want: "/api/v1/webhooks/u/{redacted}"},
		{in: "https://hooks.example/api/v1/webhooks/u/" + token + "?x=" + token, want: "https://hooks.example/api/v1/webhooks/u/{redacted}?x={redacted}"},
		{in: "/api/v1/webhooks/whk_ci", want: "/api/v1/webhooks/whk_ci"},
		{in: "/api/v1/webhooks/u/" + token + "/extra", want: "/api/v1/webhooks/u/{redacted}/extra"},
		{in: "/api/v1/webhooks/u/a?x=a&y=api", want: "/api/v1/webhooks/u/{redacted}?x={redacted}&y=api"},
	}
	for _, tc := range cases {
		got := RedactRequestURI(tc.in)
		if got != tc.want || strings.Contains(got, token) {
			t.Fatalf("in %s got %s", tc.in, got)
		}
	}
}

func TestAccessLogRedactsURLToken(t *testing.T) {
	t.Parallel()
	const token = "url-token-z9q.ABC_~"
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, token) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/u/"+token+"?echo="+token, nil)
	rr := httptest.NewRecorder()
	AccessLog(log, next).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d", rr.Code)
	}
	if strings.Contains(buf.String(), token) || !strings.Contains(buf.String(), "{redacted}") {
		t.Fatalf("log %s", buf.String())
	}
}
