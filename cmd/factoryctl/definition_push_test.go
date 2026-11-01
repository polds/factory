package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitionPushCLI(t *testing.T) {
	const planted = "planted-secret-value-9f3a"
	const apiKey = "factory-api-key-test"
	dir := t.TempDir()
	body := "schemaVersion: v1alpha1\nname: acme\nsecret: " + planted + "\n"
	if err := os.WriteFile(filepath.Join(dir, "factory.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotMethod, gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Not Implemented","status":501,"detail":"definition push is not implemented"}`))
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr strings.Builder
	getenv := func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return apiKey
		default:
			return ""
		}
	}
	code := execute([]string{"definition", "push", "--factory", "acme", dir}, &stdout, &stderr, getenv)
	if code != 1 {
		t.Fatalf("exit %d stdout %s stderr %s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/factory/acme/definition" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+apiKey {
		t.Fatal("missing bearer")
	}
	if !strings.Contains(gotBody, planted) || !strings.Contains(gotBody, "factory.yaml") {
		t.Fatal("request did not send the definition file")
	}
	if !strings.Contains(stderr.String(), "definition push is not implemented") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if strings.Contains(stderr.String(), planted) || strings.Contains(stdout.String(), planted) || strings.Contains(stderr.String(), apiKey) {
		t.Fatal("command output leaked a secret")
	}
}

func TestDefinitionPushCLIGitBacked(t *testing.T) {
	const planted = "planted-secret-value-9f3a"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "factory.yaml"), []byte("name: acme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Conflict","status":409,"detail":"factory is git-backed","value":"` + planted + `"}`))
	}))
	t.Cleanup(srv.Close)
	var stdout, stderr strings.Builder
	code := execute([]string{"definition", "push", "--factory", "acme", dir}, &stdout, &stderr, func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return "factory-api-key-test"
		default:
			return ""
		}
	})
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "factory is git-backed") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if strings.Contains(stderr.String(), planted) || strings.Contains(stdout.String(), planted) {
		t.Fatal("409 leaked the planted value")
	}
}
