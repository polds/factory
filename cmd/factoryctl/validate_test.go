package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FACTORY_URL points at a closed port. validate is offline, so a dial
// would change the exit code or the diagnostic stdout.
func TestValidate(t *testing.T) {
	t.Setenv("FACTORY_URL", "http://127.0.0.1:1")
	getenv := func(k string) string {
		if k == "FACTORY_URL" {
			return "http://127.0.0.1:1"
		}
		return ""
	}

	t.Run("missing-runner", func(t *testing.T) {
		dir := filepath.Join("..", "..", "internal", "definition", "testdata", "invalid", "missing-runner")
		want, err := os.ReadFile(filepath.Join(dir, "want.txt"))
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		code := execute([]string{"validate", dir}, &stdout, &stderr, getenv)
		if code != 1 {
			t.Fatalf("exit %d stderr %s stdout %s", code, stderr.String(), stdout.String())
		}
		if stdout.String() != string(want) {
			t.Fatalf("stdout %q want %q", stdout.String(), string(want))
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr %s", stderr.String())
		}
	})

	t.Run("acme-factory", func(t *testing.T) {
		dir := filepath.Join("..", "..", "examples", "acme-factory")
		var stdout, stderr strings.Builder
		code := execute([]string{"validate", dir}, &stdout, &stderr, getenv)
		if code != 0 {
			t.Fatalf("exit %d stderr %s stdout %s", code, stderr.String(), stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr %s", stderr.String())
		}
	})
}
