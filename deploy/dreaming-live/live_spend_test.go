package dreaminglive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkedPriceStaysOffline keeps the offline example out of the live boot.
// mode=live meters with gateway rates from factoryd, not workedPrice().
func TestWorkedPriceStaysOffline(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "deploy", "dreaming-live")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "offline.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "workedPrice") {
			t.Errorf("%s references workedPrice", name)
		}
	}
}
