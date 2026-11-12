package convert

import (
	"path/filepath"
	"testing"

	syaml "sigs.k8s.io/yaml"

	"github.com/polds/factory/api/factory/v1alpha1"
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
	if len(fac.Spec.Repositories) != 2 || !fac.Spec.Repositories[1].Deferred || fac.Spec.Repositories[1].Name != "payments-docs" {
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
