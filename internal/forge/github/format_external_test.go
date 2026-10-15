package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/forge/github"
)

func TestClientFormatHidesCachedToken(t *testing.T) {
	const secret = "ghs_SUPER_SECRET_TOKEN_VALUE"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      secret,
			"expires_at": now.Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(srv.Close)

	client, err := github.New(github.Config{
		AppID:   4242,
		Key:     key,
		BaseURL: srv.URL,
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := client.InstallationToken(context.Background(), 7, []string{"factory"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value() != secret {
		t.Fatalf("token %q", tok.Value())
	}

	for _, verb := range []string{"%s", "%v", "%+v", "%#v"} {
		rendered := fmt.Sprintf(verb, client)
		if strings.Contains(rendered, secret) {
			t.Errorf("%s leaked the installation token: %s", verb, rendered)
		}
		if !strings.Contains(rendered, "4242") || !strings.Contains(rendered, "cached:1") {
			t.Errorf("%s = %s", verb, rendered)
		}
	}
	if strings.Contains(client.String(), secret) || strings.Contains(client.GoString(), secret) {
		t.Fatalf("String/GoString leaked: %s %s", client.String(), client.GoString())
	}
}
