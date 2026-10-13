package definition

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Corpus diagnostics are compared to want.txt as "file:line: message",
// sorted by file, then line, then message. Severity is not part of the line.
func TestCorpus(t *testing.T) {
	for _, kind := range []string{"valid", "invalid"} {
		root := filepath.Join("testdata", kind)
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Fatalf("no %s corpus cases", kind)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			t.Run(kind+"/"+e.Name(), func(t *testing.T) {
				dir := filepath.Join(root, e.Name())
				tree, diags := LoadDir(dir)
				got := normalizeReport(formatDiags(diags))
				want := normalizeReport(readWant(t, filepath.Join(dir, "want.txt")))
				if got != want {
					t.Fatalf("diagnostics:\n%s\nwant:\n%s", got, want)
				}
				if kind == "valid" {
					if tree == nil {
						t.Fatal("valid tree is nil")
					}
					for _, d := range diags {
						if d.Severity == "error" {
							t.Errorf("valid tree has error %s:%d: %s", d.File, d.Line, d.Message)
						}
					}
				}
			})
		}
	}
}

func TestLoadDirExamples(t *testing.T) {
	tree, diags := LoadDir("../../examples/acme-factory")
	if tree == nil {
		t.Fatal("nil tree")
	}
	var errs []Diagnostic
	seen := map[string]bool{}
	for _, d := range diags {
		if d.Severity == "error" {
			errs = append(errs, d)
			continue
		}
		if d.Severity != "warning" {
			t.Errorf("unexpected severity %q on %s:%d", d.Severity, d.File, d.Line)
			continue
		}
		if !strings.Contains(d.Message, "not applied in M2") {
			t.Errorf("warning %s:%d: %q", d.File, d.Line, d.Message)
		}
		for _, key := range []string{"pipeline", "automations", "scorers", "webhooks"} {
			if strings.Contains(d.File, key) || strings.Contains(d.Message, key) {
				seen[key] = true
			}
		}
	}
	if len(errs) != 0 {
		t.Fatalf("errors: %#v", errs)
	}
	for _, key := range []string{"pipeline", "automations", "scorers", "webhooks"} {
		if !seen[key] {
			t.Errorf("missing M3 warning for %s", key)
		}
	}
	if tree.Factory.Name != "acme-payments" || tree.Factory.SchemaVersion != "v1alpha1" {
		t.Fatalf("factory = %+v", tree.Factory)
	}
	if len(tree.Agents) != 5 {
		t.Fatalf("agents = %d", len(tree.Agents))
	}
	runner, ok := tree.Runners["linux-standard"]
	if !ok || runner.DockerImage != "ghcr.io/acme/payments-ci:2026-10" {
		t.Fatalf("runner = %+v ok=%v", runner, ok)
	}
	if tree.Digest == "" {
		t.Fatal("empty digest")
	}
	impl := tree.Agents["implementer"]
	if impl.AgentType != "IMPLEMENT" || impl.Model != "claude-5-opus-high" || impl.Runner != "linux-standard" {
		t.Fatalf("implementer = %+v", impl)
	}
	if !strings.Contains(impl.Body, "You do not merge") {
		t.Fatalf("body missing instructions: %q", impl.Body)
	}
}

func TestMinimalM2Tree(t *testing.T) {
	tree, diags := LoadDir("testdata/valid/m2")
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %#v", diags)
	}
	if tree.Factory.Name != "m2" || tree.Factory.SchemaVersion != "v1alpha1" {
		t.Fatalf("factory = %+v", tree.Factory)
	}
	if !reflect.DeepEqual(tree.Factory.Secrets, []string{"GH_TOKEN"}) {
		t.Fatalf("secrets = %#v", tree.Factory.Secrets)
	}
	if tree.Factory.AgentDefaults.Runner != "local" || tree.Factory.AgentDefaults.Model != "auto" {
		t.Fatalf("defaults = %+v", tree.Factory.AgentDefaults)
	}
	ag, ok := tree.Agents["implementer"]
	if !ok {
		t.Fatal("missing implementer")
	}
	if ag.Model != "auto" || ag.AgentType != "IMPLEMENT" || ag.Runner != "local" {
		t.Fatalf("agent = %+v", ag)
	}
	if !strings.Contains(ag.Body, "Do the task.") {
		t.Fatalf("body = %q", ag.Body)
	}
	rn, ok := tree.Runners["local"]
	if !ok || rn.Name != "local" || rn.DockerImage != "factory-m1-task:dev" {
		t.Fatalf("runner = %+v ok=%v", rn, ok)
	}
}

func TestInheritedModelAndRunner(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "factory.yaml"), `
schemaVersion: v1alpha1
name: inherit
agentDefaults:
  model: auto
  runner: local
`)
	writeFile(t, filepath.Join(dir, "runners", "local.yaml"), `
description: local
platform:
  os: linux
  linux:
    dockerImage: factory-m1-task:dev
`)
	writeFile(t, filepath.Join(dir, "agents", "implementer", "agent.md"), `
---
description: Inherits model and runner.
agentType: IMPLEMENT
---

# Implement
`)
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %#v", diags)
	}
	ag := tree.Agents["implementer"]
	if ag.Model != "auto" || ag.Runner != "local" || ag.Harness != nil {
		t.Fatalf("inherited agent = %+v", ag)
	}
}

func TestSecretInheritance(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "factory.yaml"), `
schemaVersion: v1alpha1
name: secrets
secrets: [FACTORY_TOKEN]
mcpServers:
  shared:
    url: https://mcp.example/shared
agentDefaults:
  model: auto
  runner: local
  secrets: [DEFAULT_TOKEN]
  mcpServers:
    local:
      url: https://mcp.example/local
      headers:
        Authorization: "Bearer {{DEFAULT_MCP_TOKEN}}"
`)
	writeFile(t, filepath.Join(dir, "runners", "local.yaml"), `
description: local
platform:
  os: linux
  linux:
    dockerImage: factory-m1-task:dev
`)
	writeFile(t, filepath.Join(dir, "agents", "plain", "agent.md"), `
---
description: Inherits secrets.
agentType: IMPLEMENT
---

# Plain
`)
	writeFile(t, filepath.Join(dir, "agents", "custom", "agent.md"), `
---
description: Replaces default secrets.
agentType: IMPLEMENT
secrets: [OWN]
---

# Custom
`)
	writeFile(t, filepath.Join(dir, "agents", "cleared", "agent.md"), `
---
description: Clears default secrets.
agentType: IMPLEMENT
secrets: []
---

# Cleared
`)
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %#v", diags)
	}
	if !reflect.DeepEqual(tree.Factory.Secrets, []string{"FACTORY_TOKEN"}) {
		t.Fatalf("factory secrets = %#v", tree.Factory.Secrets)
	}
	if _, ok := tree.Factory.MCPServers["shared"]; !ok {
		t.Fatal("factory MCP server missing")
	}
	plain := tree.Agents["plain"]
	if !reflect.DeepEqual(plain.Secrets, []string{"DEFAULT_TOKEN"}) {
		t.Fatalf("plain secrets = %#v", plain.Secrets)
	}
	if plain.MCPServers["local"].URL != "https://mcp.example/local" {
		t.Fatalf("plain mcp = %#v", plain.MCPServers)
	}
	if _, ok := plain.MCPServers["shared"]; ok {
		t.Fatal("factory MCP server copied onto agent")
	}
	custom := tree.Agents["custom"]
	if !reflect.DeepEqual(custom.Secrets, []string{"OWN"}) {
		t.Fatalf("custom secrets = %#v", custom.Secrets)
	}
	if custom.MCPServers["local"].URL != "https://mcp.example/local" {
		t.Fatalf("custom mcp = %#v", custom.MCPServers)
	}
	if cleared := tree.Agents["cleared"].Secrets; len(cleared) != 0 {
		t.Fatalf("cleared secrets = %#v", cleared)
	}
}

func TestInlineCredentialInEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "factory.yaml"), `
schemaVersion: v1alpha1
name: env-leak
agentDefaults:
  model: auto
  runner: local
mcpServers:
  tools:
    command: tools
    env:
      API_TOKEN: sk-live-abcdefghijklmnopqrstuvwxyz
`)
	writeFile(t, filepath.Join(dir, "runners", "local.yaml"), `
description: local
platform:
  os: linux
  linux:
    dockerImage: factory-m1-task:dev
`)
	_, diags := LoadDir(dir)
	got := normalizeReport(formatDiags(diags))
	want := normalizeReport("factory.yaml:10: inline credential in mcpServers.tools.env.API_TOKEN\n")
	if got != want {
		t.Fatalf("diagnostics:\n%s\nwant:\n%s", got, want)
	}
}

func TestDigestStableAndSensitive(t *testing.T) {
	dir := t.TempDir()
	copyDir(t, "testdata/valid/m2", dir)
	first, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %#v", diags)
	}
	second, _ := LoadDir(dir)
	if first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("digest not stable: %q vs %q", first.Digest, second.Digest)
	}

	factoryPath := filepath.Join(dir, "factory.yaml")
	body, err := os.ReadFile(factoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(factoryPath, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, _ := LoadDir(dir)
	if edited.Digest == first.Digest {
		t.Fatal("digest unchanged after editing factory.yaml")
	}

	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, _ := LoadDir(dir)
	if added.Digest == edited.Digest {
		t.Fatal("digest unchanged after adding a file")
	}
}

func formatDiags(ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%s:%d: %s\n", d.File, d.Line, d.Message)
	}
	return b.String()
}

func readWant(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func normalizeReport(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s + "\n"
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimPrefix(body, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
