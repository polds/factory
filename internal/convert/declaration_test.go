package convert

import (
	"path/filepath"
	"testing"

	syaml "sigs.k8s.io/yaml"

	"github.com/polds/factory/api/factory/v1alpha1"
	"github.com/polds/factory/internal/definition"
)

func TestDeclarationProjectsOntoFactory(t *testing.T) {
	src := filepath.Join("..", "definition", "testdata", "valid", "declaration")
	raw, err := DirToManifests(src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var fac v1alpha1.Factory
	if err := syaml.Unmarshal(raw, &fac); err != nil {
		t.Fatal(err)
	}
	if fac.Spec.Budget == nil || fac.Spec.Budget.CeilingCents != 2500 {
		t.Fatalf("budget %+v", fac.Spec.Budget)
	}
	if len(fac.Spec.Repositories) != 2 || !fac.Spec.Repositories[1].Deferred || fac.Spec.Repositories[1].Name != "payments-docs" || fac.Spec.Repositories[1].CeilingCents != 800 {
		t.Fatalf("repositories %+v", fac.Spec.Repositories)
	}
	if fac.Spec.RoleSource == nil || fac.Spec.RoleSource.Seats.PathTemplate != "team/roles/{slug}/{file}" {
		t.Fatalf("roleSource %+v", fac.Spec.RoleSource)
	}
	if fac.Spec.Native == nil || fac.Spec.Native.Limits.AbsoluteMaxDepth != 8 || fac.Spec.Native.Implementer != "worker" {
		t.Fatalf("native %+v", fac.Spec.Native)
	}
	if fac.Spec.Publish == nil || !fac.Spec.Publish.Enabled || fac.Spec.Publish.Repo == nil || fac.Spec.Publish.Repo.Name != "payments-service" {
		t.Fatalf("publish %+v", fac.Spec.Publish)
	}
	if fac.Spec.Evidence == nil || fac.Spec.Evidence.Sentinel != "factoryEvidencePass" || fac.Spec.Evidence.Break != "factoryEvidenceFail" {
		t.Fatalf("evidence %+v", fac.Spec.Evidence)
	}
	if fac.Spec.Inference == nil || fac.Spec.Inference.Rates == nil || fac.Spec.Inference.Rates.MaxTokens != 8192 {
		t.Fatalf("rates %+v", fac.Spec.Inference)
	}
	if len(fac.Spec.Inference.Rates.Models) != 1 || fac.Spec.Inference.Rates.Models[0].InputCostPerToken != "0.000003" {
		t.Fatalf("models %+v", fac.Spec.Inference.Rates)
	}
	dest := t.TempDir()
	if err := ManifestsToDir(raw, dest); err != nil {
		t.Fatal(err)
	}
	assertSameFiles(t, src, dest)
}

func TestFilesLessFactoryRoundTrip(t *testing.T) {
	src := filepath.Join("..", "definition", "testdata", "valid", "declaration")
	raw, err := DirToManifests(src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := syaml.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	spec, _ := obj["spec"].(map[string]any)
	delete(spec, "files")
	stripped, err := syaml.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := ManifestsToDir(stripped, dest); err != nil {
		t.Fatal(err)
	}
	tree, diags := definition.LoadDir(dest)
	for _, diag := range diags {
		if diag.Severity == definition.SeverityError {
			t.Fatalf("synthesized tree: %s:%d: %s", diag.File, diag.Line, diag.Message)
		}
	}
	if tree == nil {
		t.Fatal("synthesized tree did not load")
	}
	f := tree.Factory
	if f.RoleSource == nil || f.RoleSource.Seats.PathTemplate != "team/roles/{slug}/{file}" {
		t.Fatalf("roleSource %+v", f.RoleSource)
	}
	if f.Native == nil || f.Native.Limits.MaxDepth != 8 || f.Native.Implementer != "worker" || f.Native.Reviewer != "reviewer" {
		t.Fatalf("native %+v", f.Native)
	}
	if f.Publish == nil || !f.Publish.Enabled || f.Publish.Repo == nil || f.Publish.Repo.Name != "payments-service" {
		t.Fatalf("publish %+v", f.Publish)
	}
	if f.Evidence == nil || f.Evidence.Package != "./oracle" || f.Evidence.Sentinel != "factoryEvidencePass" {
		t.Fatalf("evidence %+v", f.Evidence)
	}
	if f.Budget == nil || f.Budget.CeilingCents != 2500 {
		t.Fatalf("budget %+v", f.Budget)
	}
	if f.Inference == nil || f.Inference.Rates == nil || len(f.Inference.Rates.Models) != 1 || f.Inference.Rates.Models[0].Name != "example-model" {
		t.Fatalf("rates %+v", f.Inference)
	}
	if len(f.Repositories) != 2 || f.Repositories[1].CeilingCents != 800 {
		t.Fatalf("repositories %+v", f.Repositories)
	}
}
