package api

import (
	"net/http"
	"testing"
)

func TestSchemaRoutesUnauthenticated(t *testing.T) {
	rec := &schemaRec{}
	h := NewHandler(&schemaBackend{fakeBackend: newFake(), h: rec}, testAPIKey, nil)

	cases := []struct {
		path     string
		version  string
		document string
	}{
		{path: "/api/v1/factory-files/schemas", version: "", document: ""},
		{path: "/api/v1/factory-files/schemas/v1alpha1", version: "v1alpha1", document: ""},
		{path: "/api/v1/factory-files/schemas/v1alpha1/factory.schema.json", version: "v1alpha1", document: "factory.schema.json"},
	}
	for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong-key-value"}} {
		rec.calls = nil
		for _, tc := range cases {
			res := call(t, h, http.MethodGet, tc.path, "", hdr)
			if res.status != http.StatusOK {
				t.Fatalf("%s status %d body %s", tc.path, res.status, res.body)
			}
			if ct := res.header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content-type %q", ct)
			}
			assertNoSecretKeys(t, res.body)
			got := rec.calls[len(rec.calls)-1]
			if got.path != tc.path || got.version != tc.version || got.document != tc.document {
				t.Fatalf("%+v", got)
			}
		}
	}
	if len(rec.calls) != len(cases) {
		t.Fatalf("calls %d", len(rec.calls))
	}

	res := call(t, h, http.MethodPost, "/api/v1/factory-files/schemas", "", nil)
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	if res.header.Get("Allow") != http.MethodGet {
		t.Fatalf("Allow %q", res.header.Get("Allow"))
	}
}

func TestSchemaRoutesWithoutCatalog(t *testing.T) {
	h := NewHandler(newFake(), testAPIKey, nil)
	for _, path := range []string{
		"/api/v1/factory-files/schemas",
		"/api/v1/factory-files/schemas/v1alpha1",
		"/api/v1/factory-files/schemas/v1alpha1/factory.schema.json",
	} {
		res := call(t, h, http.MethodGet, path, "", nil)
		if res.status != http.StatusNotFound {
			t.Fatalf("%s status %d body %s", path, res.status, res.body)
		}
		assertProblem(t, res)
	}
}

type schemaHit struct {
	path     string
	version  string
	document string
}

type schemaRec struct {
	calls []schemaHit
}

func (s *schemaRec) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls = append(s.calls, schemaHit{
		path:     r.URL.Path,
		version:  r.PathValue("version"),
		document: r.PathValue("document"),
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"documents":["factory.schema.json"]}`))
}

type schemaBackend struct {
	*fakeBackend
	h http.Handler
}

func (s *schemaBackend) SchemaHandler() http.Handler { return s.h }
