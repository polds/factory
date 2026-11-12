package definition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTA2DepthZeroThreeAndNine(t *testing.T) {
	if _, err := ValidateNativeTree(ta2Chain(0, 8, 8)); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateNativeTree(ta2Chain(3, 3, 8)); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateNativeTree(ta2Chain(3, 8, 8)); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateNativeTree(ta2Chain(9, 8, 8)); err == nil {
		t.Fatal("depth 9 accepted")
	}
	nine := proofTree()
	nine.AbsoluteMaxDepth = 9
	if _, err := ValidateNativeTree(nine); err == nil {
		t.Fatal("absoluteMaxDepth 9 accepted")
	}
	plain := ta2Chain(0, 8, 8)
	plain.Synthetic = false
	plain.AtumRoster = true
	if _, err := ValidateNativeTree(plain); err != nil {
		t.Fatal(err)
	}
	checked, err := ValidateNativeTree(proofTree())
	const witnessDigest = "14faa7694f746a143133d339b3243aafef69e9bda270000fee48b737324a1c82"
	if err != nil || checked.Digest != witnessDigest {
		t.Fatalf("treeDigest keys moved %s %v", checked.Digest, err)
	}
}

func TestTA2WidgetAndDreamingLayouts(t *testing.T) {
	widget := ta2GitLayout(t, "team/roster.yaml", "team/roles/{slug}/{file}", []string{"ROLE.md"}, []string{"lead", "worker"})
	roster := []byte("agents:\n  - {slug: lead, role: lead}\n  - {slug: worker, role: worker, reports_to: lead}\n")
	if err := os.WriteFile(filepath.Join(widget, "team", "roster.yaml"), roster, 0o644); err != nil {
		t.Fatal(err)
	}
	lead := []byte("lead-role\n")
	worker := []byte("worker-role\n")
	if err := os.WriteFile(filepath.Join(widget, "team", "roles", "lead", "ROLE.md"), lead, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(widget, "team", "roles", "worker", "ROLE.md"), worker, 0o644); err != nil {
		t.Fatal(err)
	}
	ta2GitCommit(t, widget, "team")
	commit := strings.TrimSpace(ta2GitOut(t, widget, "rev-parse", "HEAD"))
	layout := RoleLayout{
		RosterPath: "team/roster.yaml", PathTemplate: "team/roles/{slug}/{file}", Files: []string{"ROLE.md"},
		MaxDepth: 3, AbsoluteMaxDepth: 8, Root: "lead", Implementer: "worker", Reviewer: "lead",
	}
	tree := ta2Load(t, widget, layout, []string{"lead", "worker"}, map[string][]byte{"lead": lead, "worker": worker}, commit, roster)
	if tree.MaxDepth != 3 || tree.Implementer != "worker" || tree.Reviewer != "lead" || persona(tree, "worker").ReportsTo != "lead" || persona(tree, "lead").ReportsTo != "" {
		t.Fatalf("widget tree %+v", tree)
	}
	if err := AttestRoleSource(context.Background(), widget, commit, tree, layout); err != nil {
		t.Fatal(err)
	}
	dreaming := HistoricalRoleLayout()
	err := AttestRoleSource(context.Background(), widget, commit, tree, dreaming)
	if err == nil || !strings.Contains(err.Error(), "absent object") {
		t.Fatalf("org/agents should fail closed: %v", err)
	}

	dream := ta2GitLayout(t, "org/roster.yaml", "org/agents/{slug}/{file}", pinnedRoleFiles, []string{"lead"})
	body := []byte("seat\n")
	if err := os.WriteFile(filepath.Join(dream, "org", "roster.yaml"), rosterLead(t), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range pinnedRoleFiles {
		if err := os.WriteFile(filepath.Join(dream, "org", "agents", "lead", name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ta2GitCommit(t, dream, "org")
	dcommit := strings.TrimSpace(ta2GitOut(t, dream, "rev-parse", "HEAD"))
	dlayout := HistoricalRoleLayout()
	dlayout.Root = "lead"
	dtree := ta2Load(t, dream, dlayout, []string{"lead"}, map[string][]byte{"lead": body}, dcommit, rosterLead(t))
	if err := AttestRoleSource(context.Background(), dream, dcommit, dtree, dlayout); err != nil {
		t.Fatal(err)
	}
}

func TestTA2RosterWithoutMetaAcceptsLeadRole(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "org", "agents", "lead"), 0o755); err != nil {
		t.Fatal(err)
	}
	roster := []byte("agents:\n  - slug: lead\n    role: lead\n")
	if err := os.WriteFile(filepath.Join(root, "org", "roster.yaml"), roster, 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte("ROLE\n")
	pins := map[string]map[string]PinnedFile{"lead": {}}
	for _, name := range pinnedRoleFiles {
		if err := os.WriteFile(filepath.Join(root, "org", "agents", "lead", name), body, 0o644); err != nil {
			t.Fatal(err)
		}
		pins["lead"][name] = PinnedFile{SHA256: ta2Sum(body), GitBlob: gitBlobID(body), Size: len(body)}
	}
	tree, err := LoadPinnedRoles(PinnedRoles{
		Root: root, SourceCommit: "source-commit", RosterSHA256: ta2Sum(roster), RosterGitBlob: gitBlobID(roster),
		Slugs: []string{"lead"}, ProofRoot: "lead", Files: pins,
	})
	if err != nil || persona(tree, "lead").Role != "lead" || persona(tree, "lead").ReportsTo != "" {
		t.Fatalf("%+v %v", tree, err)
	}
	bad := []byte("agents:\n  - slug: lead\n    role: lead\n    extra: true\n")
	if _, err := decodeRoster(bad); err == nil {
		t.Fatal("unknown roster key accepted")
	}
}

func ta2Chain(depth, maxDepth, absolute int) NativeTree {
	personas := []NativePersona{{Name: "p0", Role: "lead"}}
	for i := 1; i <= depth; i++ {
		parent := fmt.Sprintf("p%d", i-1)
		name := fmt.Sprintf("p%d", i)
		personas = append(personas, NativePersona{
			Name: name, ReportsTo: parent, SpawnableBy: []string{parent}, Role: "lead",
		})
	}
	return NativeTree{
		NativeHierarchy: true, Synthetic: true,
		MaxDepth: maxDepth, AbsoluteMaxDepth: absolute, Delegation: "direct_manager",
		Personas: personas,
	}
}

func ta2GitLayout(t *testing.T, roster, template string, files, slugs []string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(filepath.Dir(roster))), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, slug := range slugs {
		rel, ok := ExpandRolePath(RoleLayout{RosterPath: roster, PathTemplate: template, Files: files}, slug, files[0])
		if !ok {
			t.Fatal(rel)
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(filepath.Dir(rel))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func ta2Load(t *testing.T, root string, layout RoleLayout, slugs []string, bodies map[string][]byte, commit string, roster []byte) NativeTree {
	t.Helper()
	files := map[string]map[string]PinnedFile{}
	for _, slug := range slugs {
		files[slug] = map[string]PinnedFile{}
		for _, name := range layout.Files {
			body := bodies[slug]
			files[slug][name] = PinnedFile{SHA256: ta2Sum(body), GitBlob: gitBlobID(body), Size: len(body)}
		}
	}
	tree, err := LoadPinnedRoles(PinnedRoles{
		Root: root, SourceCommit: commit, RosterSHA256: ta2Sum(roster), RosterGitBlob: gitBlobID(roster),
		Slugs: slugs, Layout: layout, Files: files,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func rosterLead(t *testing.T) []byte {
	t.Helper()
	return []byte("agents:\n  - slug: lead\n    role: lead\n")
}

func ta2Sum(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func ta2GitCommit(t *testing.T, dir, path string) {
	t.Helper()
	ta2Git(t, dir, "init")
	ta2Git(t, dir, "config", "user.email", "t@example.com")
	ta2Git(t, dir, "config", "user.name", "t")
	ta2Git(t, dir, "add", path)
	ta2Git(t, dir, "commit", "-m", "roles")
}

func ta2Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = ta2GitOut(t, dir, args...)
}

func ta2GitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil && args[0] != "init" {
		t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
