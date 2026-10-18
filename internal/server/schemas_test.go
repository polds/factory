package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemaHandlerServesEmbeddedSchemasWithoutDir is the container case:
// factoryd's image has no schemas/ directory, and the process cwd is not
// the repo. The handler must still serve v1alpha1 and factory.schema.json.
func TestSchemaHandlerServesEmbeddedSchemasWithoutDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(wd); chdirErr != nil {
			t.Errorf("restore cwd: %v", chdirErr)
		}
	})
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	h := schemaHandler("")
	if h == nil {
		t.Fatal("schema handler is nil when no schemas directory is on disk")
	}

	version := getSchema(t, h, "/api/v1/factory-files/schemas/v1alpha1")
	var docs map[string]json.RawMessage
	if err := json.Unmarshal(version, &docs); err != nil {
		t.Fatalf("v1alpha1: %v", err)
	}
	raw, ok := docs["factory.schema.json"]
	if !ok || !json.Valid(raw) {
		t.Fatalf("v1alpha1 listing missing factory.schema.json: %s", version)
	}

	document := getSchema(t, h, "/api/v1/factory-files/schemas/v1alpha1/factory.schema.json")
	want, err := os.ReadFile(filepath.Join(wd, "..", "..", "schemas", "v1alpha1", "factory.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document, want) {
		t.Fatalf("embedded factory.schema.json does not match schemas/v1alpha1")
	}
}

// TestSchemaHandlerExplicitDirOverridesEmbed keeps FACTORY_SCHEMAS_PATH
// ahead of the embedded copy, including when the process cwd has no tree.
func TestSchemaHandlerExplicitDirOverridesEmbed(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(wd); chdirErr != nil {
			t.Errorf("restore cwd: %v", chdirErr)
		}
	})
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "override")
	if err := os.MkdirAll(filepath.Join(dir, "v1alpha1"), 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = `{"title":"explicit-override"}`
	if err := os.WriteFile(filepath.Join(dir, "v1alpha1", "factory.schema.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}

	h := schemaHandler(dir)
	if h == nil {
		t.Fatal("schema handler is nil")
	}
	document := getSchema(t, h, "/api/v1/factory-files/schemas/v1alpha1/factory.schema.json")
	if string(document) != marker {
		t.Fatalf("explicit dir not served: %s", document)
	}
}

func getSchema(t *testing.T, h http.Handler, path string) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.Bytes())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("%s: content-type %q", path, ct)
	}
	return rec.Body.Bytes()
}
