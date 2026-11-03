package definition

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestRequiresPullRequestOptIn(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: claude-x\nrequires:\n  - pullRequest\n"))
	tree, diags := LoadDir(dir)
	if diagHas(diags, "requires") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	agent := tree.Agents["impl"]
	if !RequiresPullRequest(agent) {
		t.Fatal("pullRequest was not loaded")
	}
	if len(agent.Requires) != 1 || agent.Requires[0] != RequirePullRequest {
		t.Fatalf("requires %#v", agent.Requires)
	}
}

func TestRequiresEmptyListOptsOut(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: claude-x\nrequires: []\n"))
	tree, diags := LoadDir(dir)
	if diagHas(diags, "requires") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if RequiresPullRequest(tree.Agents["impl"]) {
		t.Fatal("empty list opted in")
	}
}

func TestRequiresOmittedOptsOut(t *testing.T) {
	tree, diags := LoadDir(baseTree(t))
	if diagHas(diags, "requires") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if tree.Agents["impl"].Requires != nil || RequiresPullRequest(tree.Agents["impl"]) {
		t.Fatalf("omitted requires %#v", tree.Agents["impl"].Requires)
	}
}

func TestRequiresRejectsUnknownAndScalar(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: claude-x\nrequires:\n  - banana\n"))
	_, diags := LoadDir(dir)
	if !diagHas(diags, `requires "banana" is not a work product`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}

	dir = baseTree(t)
	writeFile(t, filepath.Join(dir, "agents", "impl", "agent.md"), agentMD("IMPLEMENT", "model: claude-x\nrequires: pullRequest\n"))
	_, diags = LoadDir(dir)
	if !diagHas(diags, "requires must be a list") {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}

func TestM7ImplementerRequiresPullRequest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	tree, diags := LoadDir(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "m7-factory"))
	for _, d := range diags {
		if d.Severity == SeverityError {
			t.Fatalf("diagnostics:\n%s", formatDiags(diags))
		}
	}
	if tree == nil || !RequiresPullRequest(tree.Agents["implementer"]) {
		t.Fatal("implementer did not opt in")
	}
	if RequiresPullRequest(tree.Agents["hold"]) {
		t.Fatal("hold opted in")
	}
}
