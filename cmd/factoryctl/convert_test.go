package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/convert"
)

func TestCmdConvertRoundTrip(t *testing.T) {
	root := filepath.Clean(filepath.Join(mustWd(t), "..", ".."))
	src := filepath.Join(root, "examples", "acme-factory")
	var stdout, stderr strings.Builder
	if code := cmdConvertReader([]string{src}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "kind: AgentRun") || strings.Contains(stdout.String(), "kind: WorkerPool") {
		t.Fatal("convert emitted AgentRun or WorkerPool")
	}
	dest := t.TempDir()
	stderr.Reset()
	if code := cmdConvertReader([]string{"--from-manifests", "-", dest}, ioDiscard{}, &stderr, strings.NewReader(stdout.String())); code != 0 {
		t.Fatalf("from-manifests exit %d stderr %s", code, stderr.String())
	}
	want, err := convert.ReadFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := convert.ReadFiles(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("files %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Path != want[i].Path || got[i].Content != want[i].Content {
			t.Fatalf("%s differs", want[i].Path)
		}
	}
}

func TestCRDsPrint(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := execute([]string{"crds", "print"}, &stdout, &stderr, func(string) string { return "" }); code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	want, err := convert.CRDYAML()
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != string(want) {
		t.Fatalf("print len %d, embedded len %d", stdout.Len(), len(want))
	}
	if !bytes.Contains(want, []byte("factories.factory.dev")) {
		t.Fatal("embedded CRDs missing factories.factory.dev")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func mustWd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
