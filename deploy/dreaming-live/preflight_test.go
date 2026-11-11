package dreaminglive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/harness"
)

func TestOfflineMatrix(t *testing.T) {
	if err := RunOffline(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestWorstCaseExample(t *testing.T) {
	cents, ok := workedPrice().WorstCaseCents()
	if !ok || cents != 163 {
		t.Fatalf("W=%d ok=%v", cents, ok)
	}
	over := harness.SpendPrice{
		PromptByteCap: 398976,
		MaxTokens:     8000,
		Models:        []harness.ModelRate{{Name: "high", InputCostPerToken: "0.00006", OutputCostPerToken: "0.00025"}},
	}
	cents, ok = over.WorstCaseCents()
	if !ok || cents != 2600 {
		t.Fatalf("over W=%d ok=%v", cents, ok)
	}
}

func TestDetailRedactsNeedles(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "dreaming-live.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	dir := t.TempDir()
	tree, pin, blobs, err := BuildSynthetic(dir)
	if err != nil {
		t.Fatal(err)
	}
	world := secureWorld(workflow, tree, pin, dir, blobs)
	needle := "super-secret-needle-value"
	world.Needles = []string{needle}
	world.IssueTitle = "title " + needle
	row, ok := Evaluate(world).Row(checkRead)
	if !ok || !row.Pass || strings.Contains(row.Detail, needle) {
		t.Fatalf("detail leaked: %+v", row)
	}
}
