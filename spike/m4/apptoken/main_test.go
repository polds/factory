package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMintScopesRepository(t *testing.T) {
	const want = "ghs_apptoken_test_value"
	var sawRepo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/polds/factory/installation":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/7/access_tokens":
			var req struct {
				Repositories []string `json:"repositories"`
			}
			_ = json.Unmarshal(body, &req)
			if len(req.Repositories) == 1 {
				sawRepo = req.Repositories[0]
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":      want,
				"expires_at": time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mint(context.Background(), 99, key, srv.URL, "polds/factory")
	if err != nil {
		t.Fatal(err)
	}
	if got != want || sawRepo != "factory" {
		t.Fatalf("token minted=%t repo %q", got == want, sawRepo)
	}
}

func TestMintDeniedErrorIsRedacted(t *testing.T) {
	const planted = "ghs_should_not_appear_in_the_error"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "forbidden names the permission",
			status: http.StatusForbidden,
			body:   `{"message":"Resource not accessible by integration"}`,
			want:   "missing permission",
		},
		{
			name:   "unprocessable names the permission",
			status: http.StatusUnprocessableEntity,
			body:   `{"message":"There is at least one permission requested that the installation does not have: checks","status":"422"}`,
			want:   "checks",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() { _ = r.Body.Close() }()
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Errorf("read body: %v", err)
				}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/installation":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
				case r.Method == http.MethodPost && r.URL.Path == "/app/installations/7/access_tokens":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			_, err := mint(context.Background(), 99, key, srv.URL, "acme/widgets")
			if err == nil {
				t.Fatal("mint succeeded")
			}
			text := redactedMintError(err)
			if !strings.Contains(text, tc.want) || !strings.Contains(text, strconv.Itoa(tc.status)) {
				t.Fatalf("error missing permission or status")
			}
			if strings.Contains(text, planted) || strings.Contains(text, "eyJ") || strings.Contains(text, "PRIVATE KEY") {
				t.Fatal("mint error included secret material")
			}
		})
	}
}
