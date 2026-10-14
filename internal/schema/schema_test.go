package schema

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// frontmatterNote is the docs/03 layer-1 statement. Markdown resources are
// not JSON Schema documents.
const frontmatterNote = "Markdown frontmatter resources (agent.md, automation.md, scorer.md, SKILL.md) are validated offline (layer 2), not by JSON Schema."

func TestSchemasAreDraft202012(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "schemas", "v1alpha1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read schemas: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckDraft(body); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
	}
	if len(names) < 4 {
		t.Fatalf("found %d schema documents, want at least 4", len(names))
	}
	for _, required := range []string{
		"factory.schema.json",
		"runner.schema.json",
		"router.schema.json",
		"webhook.schema.json",
	} {
		if !contains(names, required) {
			t.Errorf("missing %s", required)
		}
	}
}

func TestFactorySchemaAcceptsPipelineBlock(t *testing.T) {
	schema := readSchema(t, "factory.schema.json")
	// Shape from docs/03 pipeline:, including a gate with required: false
	// and no approvers.
	const instance = `{
		"schemaVersion": "v1alpha1",
		"name": "payments-factory",
		"pipeline": {
			"stages": ["triage", "planning", "building", "reviewing"],
			"agents": {
				"triage": "triage",
				"planning": "spec",
				"building": "implementer",
				"reviewing": "reviewer"
			},
			"skip": {
				"planning": "whenScopeClear",
				"triage": "whenIntakeHasScope"
			},
			"approvals": {
				"spec": {"required": true, "approvers": "role:editor"},
				"handoff": {"required": true, "approvers": "role:user"},
				"merge": {"required": false}
			},
			"revisionLimit": 3
		}
	}`
	if err := ValidateJSON(schema, []byte(instance)); err != nil {
		t.Fatal(err)
	}
}

func TestFactorySchemaRejectsUnknownKeys(t *testing.T) {
	schema := readSchema(t, "factory.schema.json")
	cases := []struct {
		name     string
		instance string
		key      string
	}{
		{
			name: "top level",
			instance: `{
				"schemaVersion": "v1alpha1",
				"name": "payments-factory",
				"notARealKey": true
			}`,
			key: "notARealKey",
		},
		{
			name: "pipeline",
			instance: `{
				"schemaVersion": "v1alpha1",
				"name": "payments-factory",
				"pipeline": {
					"stages": ["triage"],
					"agents": {"triage": "triage"},
					"nope": 1
				}
			}`,
			key: "nope",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateJSON(schema, []byte(tc.instance))
			if err == nil {
				t.Fatal("expected unknown key to be rejected")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %v does not name %s", err, tc.key)
			}
		})
	}
}

func TestOtherSchemasAcceptDocsExamplesAndRejectUnknownKeys(t *testing.T) {
	root := repoRoot(t)
	cases := []struct {
		schema string
		good   string
		bad    string
		key    string
	}{
		{
			schema: "runner.schema.json",
			good: `{
				"description": "Linux runner for payments builds and tests",
				"setupCommands": ["corepack enable", "pnpm install --frozen-lockfile"],
				"instanceShape": {"vcpus": 4, "memoryGb": 8},
				"platform": {
					"os": "linux",
					"arch": "x86_64",
					"linux": {
						"dockerImage": "ghcr.io/acme/payments-ci:2026-10",
						"registryCredentialSecretName": "GHCR_PULL"
					}
				},
				"failureSessionRetentionMinutes": 30
			}`,
			bad: `{"description": "x", "notARealKey": true}`,
			key: "notARealKey",
		},
		{
			schema: "router.schema.json",
			good: `{
				"name": "By task",
				"type": "prompt",
				"default": "claude-4-6-sonnet-high",
				"routing": [
					{"description": "Routine documentation changes", "model": "claude-4-5-haiku"},
					{"description": "Complex implementation or debugging", "model": "claude-4-8-opus-high"}
				]
			}`,
			bad: `{"type": "prompt", "default": "m", "routing": [], "notARealKey": true}`,
			key: "notARealKey",
		},
		{
			schema: "webhook.schema.json",
			good: `{
				"authMode": "token",
				"secretName": "INTERNAL_CI_WEBHOOK_SECRET",
				"deliveryIdHeader": "X-CI-Run-Id",
				"enabled": true
			}`,
			bad: `{"authMode": "token", "notARealKey": true}`,
			key: "notARealKey",
		},
	}
	for _, tc := range cases {
		t.Run(tc.schema, func(t *testing.T) {
			schema := readSchema(t, tc.schema)
			if err := ValidateJSON(schema, []byte(tc.good)); err != nil {
				t.Fatal(err)
			}
			err := ValidateJSON(schema, []byte(tc.bad))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("unknown key error = %v", err)
			}
		})
	}

	factory := readSchema(t, "factory.schema.json")
	validateYAMLFile(t, factory, filepath.Join(root, "examples", "acme-factory", "factory.yaml"))
	runner := readSchema(t, "runner.schema.json")
	validateYAMLFile(t, runner, filepath.Join(root, "examples", "acme-factory", "runners", "linux-standard.yaml"))
	webhook := readSchema(t, "webhook.schema.json")
	validateYAMLFile(t, webhook, filepath.Join(root, "examples", "acme-factory", "webhooks", "ci-failure.yaml"))
}

func TestRouterComplexityAndWebhookSignature(t *testing.T) {
	router := readSchema(t, "router.schema.json")
	const complexity = `{
		"name": "By complexity",
		"type": "complexity",
		"default": "claude-4-6-sonnet-high",
		"routing": {
			"easy": "claude-4-5-haiku",
			"medium": "claude-4-6-sonnet-high",
			"hard": "claude-4-8-opus-high"
		}
	}`
	if err := ValidateJSON(router, []byte(complexity)); err != nil {
		t.Fatal(err)
	}

	webhook := readSchema(t, "webhook.schema.json")
	const signed = `{
		"authMode": "signature",
		"signatureScheme": "github",
		"secretName": "CI_WEBHOOK_SECRET",
		"enabled": true
	}`
	if err := ValidateJSON(webhook, []byte(signed)); err != nil {
		t.Fatal(err)
	}
	err := ValidateJSON(webhook, []byte(`{"authMode": "signature", "secretName": "CI_WEBHOOK_SECRET"}`))
	if err == nil {
		t.Fatal("signature mode without signatureScheme was accepted")
	}
}

func TestMarkdownFrontmatterDocumented(t *testing.T) {
	root := repoRoot(t)
	readme, err := os.ReadFile(filepath.Join(root, "schemas", "v1alpha1", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), frontmatterNote) {
		t.Fatalf("README missing %q", frontmatterNote)
	}
	factory := readSchema(t, "factory.schema.json")
	if !strings.Contains(string(factory), "validated offline (layer 2), not by JSON Schema") {
		t.Fatal("factory.schema.json does not document layer 2")
	}
}

func TestHandlerUnauthenticated(t *testing.T) {
	root := repoRoot(t)
	h := NewHandler(os.DirFS(filepath.Join(root, "schemas")))
	file := readSchema(t, "factory.schema.json")

	t.Run("document", func(t *testing.T) {
		status, hdr, body := get(t, h, "/api/v1/factory-files/schemas/v1alpha1/factory.schema.json", nil)
		if status != http.StatusOK {
			t.Fatalf("status %d body %s", status, body)
		}
		if hdr.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type %q", hdr.Get("Content-Type"))
		}
		if !jsonEqual(file, body) {
			t.Fatalf("body is not factory.schema.json")
		}
	})

	t.Run("no authorization header", func(t *testing.T) {
		status, _, body := get(t, h, "/api/v1/factory-files/schemas/v1alpha1/factory.schema.json", http.Header{})
		if status != http.StatusOK {
			t.Fatalf("status %d body %s", status, body)
		}
	})

	t.Run("ignored bearer", func(t *testing.T) {
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer not-a-key")
		status, _, body := get(t, h, "/api/v1/factory-files/schemas", hdr)
		if status != http.StatusOK {
			t.Fatalf("status %d body %s", status, body)
		}
	})

	t.Run("index", func(t *testing.T) {
		status, hdr, body := get(t, h, "/api/v1/factory-files/schemas", nil)
		if status != http.StatusOK {
			t.Fatalf("status %d body %s", status, body)
		}
		if hdr.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type %q", hdr.Get("Content-Type"))
		}
		var index struct {
			Current             string   `json:"current"`
			Versions            []string `json:"versions"`
			MarkdownFrontmatter string   `json:"markdownFrontmatter"`
		}
		if err := json.Unmarshal(body, &index); err != nil {
			t.Fatal(err)
		}
		if index.Current != "v1alpha1" {
			t.Fatalf("current %q", index.Current)
		}
		if !contains(index.Versions, "v1alpha1") {
			t.Fatalf("versions %v", index.Versions)
		}
		if index.MarkdownFrontmatter != frontmatterNote {
			t.Fatalf("markdownFrontmatter %q", index.MarkdownFrontmatter)
		}
	})

	t.Run("version", func(t *testing.T) {
		status, hdr, body := get(t, h, "/api/v1/factory-files/schemas/v1alpha1", nil)
		if status != http.StatusOK {
			t.Fatalf("status %d body %s", status, body)
		}
		if hdr.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type %q", hdr.Get("Content-Type"))
		}
		var docs map[string]json.RawMessage
		if err := json.Unmarshal(body, &docs); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{
			"factory.schema.json",
			"runner.schema.json",
			"router.schema.json",
			"webhook.schema.json",
		} {
			raw, ok := docs[name]
			if !ok {
				t.Fatalf("version listing missing %s (keys %v)", name, keys(docs))
			}
			if !jsonEqual(readSchema(t, name), raw) {
				t.Fatalf("%s in the listing does not match the file", name)
			}
		}
	})

	t.Run("unknown document", func(t *testing.T) {
		status, _, _ := get(t, h, "/api/v1/factory-files/schemas/v1alpha1/missing.schema.json", nil)
		if status != http.StatusNotFound {
			t.Fatalf("status %d", status)
		}
	})
}

func get(t *testing.T, h http.Handler, path string, hdr http.Header) (int, http.Header, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, vals := range hdr {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	res := rr.Result()
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, body
}

func readSchema(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "schemas", "v1alpha1", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func validateYAMLFile(t *testing.T, schema []byte, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateJSON(schema, instance); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func jsonEqual(a, b []byte) bool {
	var ja, jb any
	if json.Unmarshal(a, &ja) != nil || json.Unmarshal(b, &jb) != nil {
		return false
	}
	return reflect.DeepEqual(ja, jb)
}
