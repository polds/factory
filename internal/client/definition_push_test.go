package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/polds/factory/internal/api"
)

func TestPushDefinition(t *testing.T) {
	const planted = "planted-secret-value-9f3a"
	var gotMethod, gotPath, gotAuth string
	var gotBody api.DefinitionPushRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		switch r.URL.Query().Get("mode") {
		case "conflict":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Conflict","status":409,"detail":"factory is git-backed","value":"` + planted + `"}`))
		case "unimplemented":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Not Implemented","status":501,"detail":"definition push is not implemented","value":"` + planted + `"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"valid":true,"errors":[],"published_generation":4,"value":"` + planted + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "cli-key"}
	req := api.DefinitionPushRequest{
		Generation: 4,
		Files:      []api.DefinitionFile{{Path: "factory.yaml", Content: "name: acme\n" + planted}},
	}
	res, err := c.PushDefinition(context.Background(), "acme payments", req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.PublishedGeneration != 4 {
		t.Fatalf("response %+v", res)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/factory/acme%20payments/definition" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer cli-key" {
		t.Fatalf("auth %q", gotAuth)
	}
	if gotBody.Generation != 4 || len(gotBody.Files) != 1 {
		t.Fatalf("body %+v", gotBody)
	}

	c.BaseURL = srv.URL + "?mode=conflict"
	// query on the base URL is not how the client appends the path. Use a
	// dedicated server path instead.
	conflict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Conflict","status":409,"detail":"factory is git-backed","value":"`+planted+`"}`)
	}))
	t.Cleanup(conflict.Close)
	c = &Client{BaseURL: conflict.URL, APIKey: "cli-key"}
	_, err = c.PushDefinition(context.Background(), "acme", req)
	if !errors.Is(err, ErrGitBackedFactory) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), planted) {
		t.Fatalf("409 error leaked a secret: %v", err)
	}

	unwired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Not Implemented","status":501,"detail":"definition push is not implemented","value":"`+planted+`"}`)
	}))
	t.Cleanup(unwired.Close)
	c = &Client{BaseURL: unwired.URL, APIKey: "cli-key"}
	_, err = c.PushDefinition(context.Background(), "acme", req)
	if err == nil || !strings.Contains(err.Error(), "501") || strings.Contains(err.Error(), planted) {
		t.Fatalf("501 err %v", err)
	}
}
