package definition

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestNativeTreeProof(t *testing.T) {
	checked, err := ValidateNativeTree(proofTree())
	if err != nil {
		t.Fatal(err)
	}
	// Digest of proofTree() through treeDigest on c447f17 and on this head.
	// The copy of SpawnableBy uses append, so the root's nil slice encodes as
	// [] and the SHA256 is 14faa769.... aee80d35... is that same JSON with the
	// root spawnableBy left null. New omitempty fields stay empty, so this hex
	// does not move.
	const witnessDigest = "14faa7694f746a143133d339b3243aafef69e9bda270000fee48b737324a1c82"
	if checked.Digest != witnessDigest {
		t.Fatalf("witness digest %s", checked.Digest)
	}
	if checked.Root != "coordinator" || checked.Depth["specialist-a"] != 2 || checked.Digest == "" {
		t.Fatalf("tree %+v", checked)
	}
	again, err := ValidateNativeTree(proofTree())
	if err != nil || again.Digest != checked.Digest {
		t.Fatalf("digest %s %s %v", checked.Digest, again.Digest, err)
	}
	if err := AllowDelegate(persona(proofTree(), "specialist-a"), "lead", Ancestors(indexOf(proofTree()), "specialist-a"), false); err != nil {
		t.Fatal(err)
	}
	if err := AllowDelegate(persona(proofTree(), "specialist-a"), "coordinator", Ancestors(indexOf(proofTree()), "specialist-a"), false); !errors.Is(err, ErrDelegationDenied) {
		t.Fatalf("shortcut %v", err)
	}
	if err := CheckDepth(2, 2, 8); err != nil {
		t.Fatal(err)
	}
	if err := CheckDepth(3, 2, 8); err == nil || err.Error() == "" {
		t.Fatal("depth")
	}
	secrets := ReportsDoNotGrantSecrets("lead", []string{"token"})
	if len(secrets) != 1 || secrets[0] != "token" {
		t.Fatalf("secrets %+v", secrets)
	}
}

func TestNativeTreeRejects(t *testing.T) {
	cycled := proofTree()
	cycled.Personas[0].ReportsTo = "specialist-a"
	cycled.Personas[1].ReportsTo = "coordinator"
	if _, err := ValidateNativeTree(cycled); err == nil {
		t.Fatal("cycle accepted")
	}
	shortcut := proofTree()
	shortcut.AllowAncestorShortcut = true
	if _, err := ValidateNativeTree(shortcut); err == nil {
		t.Fatal("shortcut accepted")
	}
	atum := proofTree()
	atum.AtumRoster = true
	if _, err := ValidateNativeTree(atum); err != nil {
		t.Fatal(err)
	}
	legacy := NativeTree{NativeHierarchy: false, ReportsToOmitted: true, Enforced: false}
	if _, err := ValidateNativeTree(legacy); err != nil {
		t.Fatal(err)
	}
	enforced := NativeTree{NativeHierarchy: false, ReportsToOmitted: true, Enforced: true}
	if _, err := ValidateNativeTree(enforced); !errors.Is(err, ErrLegacyHierarchy) {
		t.Fatalf("legacy %v", err)
	}
}

func TestReportsToLoad(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: claude-x\nreportsTo: lead\n"))
	tree, diags := LoadDir(dir)
	if tree == nil || len(diags) > 0 {
		t.Fatalf("diags %+v", diags)
	}
	if tree.Agents["impl"].ReportsTo != "lead" {
		t.Fatalf("reportsTo %q", tree.Agents["impl"].ReportsTo)
	}
	omitted := baseTree(t)
	loaded, diags := LoadDir(omitted)
	if loaded == nil || len(diags) > 0 {
		t.Fatalf("omitted %+v", diags)
	}
	if loaded.Agents["impl"].ReportsTo != "" {
		t.Fatalf("omitted reportsTo %q", loaded.Agents["impl"].ReportsTo)
	}
}

func proofTree() NativeTree {
	return NativeTree{
		NativeHierarchy:  true,
		Synthetic:        true,
		MaxDepth:         NativeProofMaxDepth,
		AbsoluteMaxDepth: NativeAbsoluteMaxDepth,
		Delegation:       "direct_manager",
		Personas: []NativePersona{
			{Name: "coordinator", Role: "manager"},
			{Name: "lead", ReportsTo: "coordinator", SpawnableBy: []string{"coordinator"}, Role: "manager"},
			{Name: "specialist-a", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
			{Name: "specialist-b", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
		},
	}
}

func persona(tree NativeTree, name string) NativePersona {
	for _, p := range tree.Personas {
		if p.Name == name {
			return p
		}
	}
	return NativePersona{}
}

func indexOf(tree NativeTree) map[string]NativePersona {
	out := map[string]NativePersona{}
	for _, p := range tree.Personas {
		out[p.Name] = p
	}
	return out
}
