package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
