package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type pushBackend struct {
	*fakeBackend
	publish func(context.Context, string, DefinitionPushRequest) (DefinitionPushResponse, error)
}

func (p pushBackend) PublishDefinition(ctx context.Context, name string, req DefinitionPushRequest) (DefinitionPushResponse, error) {
	return p.publish(ctx, name, req)
}

func TestPutDefinition(t *testing.T) {
	const planted = "planted-secret-value-9f3a"
	body := `{"generation":7,"files":[{"path":"factory.yaml","content":"name: acme\nsecret: ` + planted + `\n"}]}`

	t.Run("valid", func(t *testing.T) {
		h := NewHandler(pushBackend{
			fakeBackend: newFake(),
			publish: func(context.Context, string, DefinitionPushRequest) (DefinitionPushResponse, error) {
				return DefinitionPushResponse{Valid: true, Errors: []Diagnostic{}, PublishedGeneration: 7}, nil
			},
		}, testAPIKey, nil)
		res := call(t, h, http.MethodPut, "/api/v1/factory/acme/definition", body, apiHeaders())
		if res.status != http.StatusOK {
			t.Fatalf("status %d body %s", res.status, res.body)
		}
		assertPushGolden(t, res.body, "definition-push-200.json")
		if bytes.Contains(res.body, []byte(planted)) || bytes.Contains(res.body, []byte(testAPIKey)) {
			t.Fatalf("response leaked a secret: %s", res.body)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		h := NewHandler(pushBackend{
			fakeBackend: newFake(),
			publish: func(context.Context, string, DefinitionPushRequest) (DefinitionPushResponse, error) {
				return DefinitionPushResponse{
					Valid:               false,
					PublishedGeneration: 4,
					Errors:              []Diagnostic{{File: "factory.yaml", Line: 3, Severity: "error", Message: "pipeline.stages is required"}},
				}, nil
			},
		}, testAPIKey, nil)
		res := call(t, h, http.MethodPut, "/api/v1/factory/acme/definition", body, apiHeaders())
		if res.status != http.StatusOK {
			t.Fatalf("status %d body %s", res.status, res.body)
		}
		assertPushGolden(t, res.body, "definition-push-invalid.json")
		if bytes.Contains(res.body, []byte(planted)) {
			t.Fatalf("invalid response leaked a secret: %s", res.body)
		}
	})

	t.Run("git backed", func(t *testing.T) {
		h := NewHandler(pushBackend{
			fakeBackend: newFake(),
			publish: func(context.Context, string, DefinitionPushRequest) (DefinitionPushResponse, error) {
				return DefinitionPushResponse{}, ErrGitBackedDefinition
			},
		}, testAPIKey, nil)
		res := call(t, h, http.MethodPut, "/api/v1/factory/acme/definition", body, apiHeaders())
		if res.status != http.StatusConflict {
			t.Fatalf("status %d body %s", res.status, res.body)
		}
		if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("content-type %q", ct)
		}
		assertPushGolden(t, res.body, "definition-push-409.json")
		if bytes.Contains(res.body, []byte(planted)) || bytes.Contains(res.body, []byte(testAPIKey)) {
			t.Fatalf("409 leaked a secret: %s", res.body)
		}
	})

	h := NewHandler(pushBackend{
		fakeBackend: newFake(),
		publish: func(context.Context, string, DefinitionPushRequest) (DefinitionPushResponse, error) {
			return DefinitionPushResponse{Valid: true, PublishedGeneration: 1}, nil
		},
	}, testAPIKey, nil)
	res := call(t, h, http.MethodPut, "/api/v1/factory/acme/definition", body, nil)
	if res.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", res.status)
	}
	if bytes.Contains(res.body, []byte(planted)) {
		t.Fatal("401 leaked the body")
	}
	res = call(t, h, http.MethodPut, "/api/v1/factory/acme/definition", `{"generation":-1,"files":[{"path":"factory.yaml","content":"x"}]}`, apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("negative generation status %d body %s", res.status, res.body)
	}
	res = call(t, h, http.MethodPut, "/api/v1/factory/acme/definition", `{"files":[]}`, apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("empty files status %d", res.status)
	}

	plain := NewHandler(newFake(), testAPIKey, nil)
	res = call(t, plain, http.MethodPut, "/api/v1/factory/acme/definition", body, apiHeaders())
	if res.status != http.StatusNotImplemented {
		t.Fatalf("unwired status %d", res.status)
	}
	if bytes.Contains(res.body, []byte(planted)) {
		t.Fatal("501 leaked the body")
	}
}

func assertPushGolden(t *testing.T, body []byte, name string) {
	t.Helper()
	got := normalizeJSON(t, body)
	want := normalizeJSON(t, readRepoGolden(t, name))
	if !bytes.Equal(got, want) {
		t.Fatalf("golden %s\n got %s\nwant %s", name, got, want)
	}
}

func readRepoGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDefinitionPushJSON(t *testing.T) {
	var req DefinitionPushRequest
	raw := []byte(`{"generation":4,"files":[{"path":"factory.yaml","content":"name: acme\n"}],"value":"planted-secret-value-9f3a"}`)
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Generation != 4 || len(req.Files) != 1 || req.Files[0].Path != "factory.yaml" {
		t.Fatalf("decoded %+v", req)
	}
	out, err := json.Marshal(DefinitionPushResponse{Valid: true, Errors: []Diagnostic{}, PublishedGeneration: 4})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("planted")) {
		t.Fatal(out)
	}
	if !bytes.Contains(out, []byte(`"published_generation":4`)) || !bytes.Contains(out, []byte(`"valid":true`)) {
		t.Fatal(string(out))
	}
}
