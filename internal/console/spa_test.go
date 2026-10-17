//go:build console

package console

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPAFallback(t *testing.T) {
	index, err := fs.ReadFile(embedded, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "/runs/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type %q", ct)
	}
	if cache := rec.Header().Get("Cache-Control"); strings.Contains(cache, "immutable") {
		t.Fatalf("index cache %q", cache)
	}
	if rec.Body.String() != string(index) {
		t.Fatalf("body is not index.html")
	}
}

func TestAssetImmutable(t *testing.T) {
	var asset string
	err := fs.WalkDir(embedded, "dist", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(name, "dist/")
		if rel != "index.html" && asset == "" {
			asset = rel
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if asset == "" {
		t.Fatal("embedded dist has no asset beside index.html")
	}
	h := Handler(http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "/"+asset, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d for %s", rec.Code, asset)
	}
	if cache := rec.Header().Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("cache %q", cache)
	}
}

func TestEmbeddedPathEscape(t *testing.T) {
	index, err := fs.ReadFile(embedded, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "/assets/../../handler.go", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(index) {
		t.Fatalf("status %d", rec.Code)
	}
}
