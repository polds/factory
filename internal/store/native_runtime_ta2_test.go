package store

import (
	"encoding/json"
	"testing"

	"github.com/polds/factory/internal/definition"
)

func TestLegacyEnvelopeSuppliesSyntheticTrue(t *testing.T) {
	// The pre-TA-2 witness is a persona array. An envelope without synthetic
	// is the same reload rule: the digest was computed with Synthetic true.
	array, err := json.Marshal([]definition.NativePersona{
		{Name: "coordinator", Role: "manager"},
		{Name: "lead", ReportsTo: "coordinator", SpawnableBy: []string{"coordinator"}, Role: "manager"},
		{Name: "specialist-a", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
		{Name: "specialist-b", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := unmarshalNativePersonas(array)
	if err != nil || doc.SyntheticStored {
		t.Fatalf("legacy array %+v %v", doc, err)
	}
	policy := NativePolicy{
		NativeHierarchy: true, MaxDepth: definition.NativeProofMaxDepth, AbsoluteMaxDepth: definition.NativeAbsoluteMaxDepth,
		Delegation: "direct_manager", Personas: doc.Personas, SyntheticStored: doc.SyntheticStored,
	}
	checked, err := definition.ValidateNativeTree(policy.tree())
	const witnessDigest = "14faa7694f746a143133d339b3243aafef69e9bda270000fee48b737324a1c82"
	if err != nil || !policy.tree().Synthetic || checked.Digest != witnessDigest {
		t.Fatalf("legacy digest %s synthetic %v %v", checked.Digest, policy.tree().Synthetic, err)
	}
	legacy := []byte(`{"personas":[{"name":"coordinator","reportsTo":"","spawnableBy":[],"role":"manager"}]}`)
	doc, err = unmarshalNativePersonas(legacy)
	if err != nil || doc.SyntheticStored {
		t.Fatalf("old envelope %+v %v", doc, err)
	}
}

func TestNewOpenPersistsSyntheticFlag(t *testing.T) {
	tree := definition.NativeTree{
		NativeHierarchy: true, Synthetic: false,
		MaxDepth: 8, AbsoluteMaxDepth: 8, Delegation: "direct_manager",
		SourceCommit: strings40(), RosterSHA256: strings64(), RosterGitBlob: strings40(),
		BundleMode: definition.BundleModeRoleFiles,
		Personas: []definition.NativePersona{{
			Name: "lead", Role: "lead",
			Files: []definition.RoleFile{{Name: "ROLE.md", SHA256: strings64(), GitBlob: strings40(), Bytes: []byte("x")}},
		}},
		Implementer: "lead", Reviewer: "lead",
		RosterPath: "team/roster.yaml", PathTemplate: "team/roles/{slug}/{file}", SeatFiles: []string{"ROLE.md"},
	}
	body, err := marshalNativePersonas(tree)
	if err != nil {
		t.Fatal(err)
	}
	if body[0] == '[' {
		t.Fatal("new open stored a legacy array")
	}
	doc, err := unmarshalNativePersonas(body)
	if err != nil || !doc.SyntheticStored || doc.Synthetic || doc.Implementer != "lead" || doc.PathTemplate != tree.PathTemplate {
		t.Fatalf("stored %+v %v", doc, err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"personas", "synthetic", "implementer", "pathTemplate"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing %s in %s", key, body)
		}
	}
	reloaded := NativePolicy{
		NativeHierarchy: true, MaxDepth: 8, AbsoluteMaxDepth: 8, Delegation: "direct_manager",
		Personas: doc.Personas, Synthetic: doc.Synthetic, SyntheticStored: doc.SyntheticStored,
		SourceCommit: doc.SourceCommit, RosterSHA256: doc.RosterSHA256, RosterGitBlob: doc.RosterGitBlob,
		BundleMode: doc.BundleMode,
	}
	want, err := definition.ValidateNativeTree(tree)
	got, gotErr := definition.ValidateNativeTree(reloaded.tree())
	if err != nil || gotErr != nil || got.Digest != want.Digest || reloaded.tree().Synthetic {
		t.Fatalf("reload digest %s want %s synthetic %v %v %v", got.Digest, want.Digest, reloaded.tree().Synthetic, err, gotErr)
	}
}

func strings40() string { return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }
func strings64() string { return strings40() + "bbbbbbbbbbbbbbbbbbbbbbbb" }
