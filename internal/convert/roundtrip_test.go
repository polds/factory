package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
)

func TestRoundTripTrees(t *testing.T) {
	root := moduleRoot(t)
	trees := []string{
		"examples/acme-factory",
		"deploy/m2-factory",
		"deploy/m3-factory",
		"deploy/m4-factory",
		"deploy/m5-factory",
		"deploy/m6-factory",
	}
	for _, rel := range trees {
		t.Run(rel, func(t *testing.T) {
			src := filepath.Join(root, rel)
			manifests, err := DirToManifests(src, Options{})
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if bytes.Contains(manifests, []byte("kind: AgentRun")) || bytes.Contains(manifests, []byte("kind: WorkerPool")) {
				t.Fatal("convert emitted AgentRun or WorkerPool")
			}
			dest := t.TempDir()
			if err := ManifestsToDir(manifests, dest); err != nil {
				t.Fatalf("from-manifests: %v", err)
			}
			assertSameFiles(t, src, dest)
		})
	}
}

func TestInstructionsConfigMapRoundTrip(t *testing.T) {
	root := moduleRoot(t)
	src := filepath.Join(root, "examples/acme-factory")
	manifests, err := DirToManifests(src, Options{InstructionsConfigMap: true})
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifests)
	if !strings.Contains(text, "kind: ConfigMap") {
		t.Fatal("missing ConfigMap")
	}
	if !strings.Contains(text, "instructionsFrom:") && !strings.Contains(text, "promptFrom:") {
		t.Fatal("missing instructionsFrom or promptFrom")
	}
	dest := t.TempDir()
	if err := ManifestsToDir(manifests, dest); err != nil {
		t.Fatal(err)
	}
	assertSameFiles(t, src, dest)
}

func TestConvertRejectsInvalidTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "factory.yaml"), []byte("schemaVersion: v1alpha1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DirToManifests(dir, Options{}); err == nil {
		t.Fatal("expected an error")
	}
}

func assertSameFiles(t *testing.T, src, dest string) {
	t.Helper()
	want, err := ReadFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadFiles(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("files %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Path != want[i].Path {
			t.Fatalf("path %s, want %s", got[i].Path, want[i].Path)
		}
		if got[i].Content != want[i].Content {
			t.Fatalf("%s bytes differ\n got %q\nwant %q", want[i].Path, got[i].Content, want[i].Content)
		}
	}
	// The rebuilt tree must still load. Warnings are allowed.
	tree, diags := definition.LoadDir(dest)
	if tree == nil {
		t.Fatal("rebuilt tree did not load")
	}
	for _, diag := range diags {
		if diag.Severity == definition.SeverityError {
			t.Fatalf("rebuilt tree: %s:%d: %s", diag.File, diag.Line, diag.Message)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}
