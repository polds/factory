package definition

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApproversWidened(t *testing.T) {
	cases := []struct {
		old, new string
		want     bool
	}{
		{old: "role:editor", new: "role:editor", want: false},
		{old: " role:editor ", new: "role:editor", want: false},
		{old: "role:editor", new: "role:user", want: true},
		{old: "role:editor", new: "", want: true},
		{old: "role:editor", new: "anyone", want: true},
		{old: "role:user", new: "role:editor", want: false},
		{old: "role:user", new: "", want: true},
		{old: "role:user", new: "anyone", want: true},
		{old: "", new: "role:editor", want: false},
		{old: "", new: "role:user", want: false},
		{old: "", new: "anyone", want: true},
		{old: "anyone", new: "", want: true},
		{old: "anyone", new: "role:editor", want: false},
	}
	for _, tc := range cases {
		if got := approversWidened(tc.old, tc.new); got != tc.want {
			t.Errorf("approversWidened(%q, %q) = %v, want %v", tc.old, tc.new, got, tc.want)
		}
	}
}

func TestWeakensApprovalsTable(t *testing.T) {
	base := acmeApprovals()
	spec := base["spec"]
	cases := []struct {
		name string
		head map[string]ApprovalGate
		want []string
	}{
		{name: "unchanged", head: acmeApprovals()},
		{
			name: "remove",
			head: withoutGate(acmeApprovals(), "spec"),
			want: []string{`approval gate "spec" removed`},
		},
		{
			name: "rename",
			head: func() map[string]ApprovalGate {
				h := withoutGate(acmeApprovals(), "spec")
				h["planning"] = spec
				return h
			}(),
			want: []string{`approval gate "spec" removed`},
		},
		{
			name: "required",
			head: func() map[string]ApprovalGate {
				h := acmeApprovals()
				g := h["spec"]
				g.Required = false
				h["spec"] = g
				return h
			}(),
			want: []string{`approval gate "spec" required weakened from true to false`},
		},
		{
			name: "widen",
			head: func() map[string]ApprovalGate {
				h := acmeApprovals()
				g := h["spec"]
				g.Approvers = "role:user"
				h["spec"] = g
				return h
			}(),
			want: []string{`approval gate "spec" approvers widened from "role:editor" to "role:user"`},
		},
		{
			name: "add",
			head: func() map[string]ApprovalGate {
				h := acmeApprovals()
				h["extra"] = ApprovalGate{Required: false, Approvers: "anyone"}
				return h
			}(),
		},
		{
			name: "raise rank",
			head: func() map[string]ApprovalGate {
				h := acmeApprovals()
				g := h["handoff"]
				g.Approvers = "role:editor"
				h["handoff"] = g
				g = h["merge"]
				g.Approvers = "role:editor"
				h["merge"] = g
				return h
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WeakensApprovals(treeWithApprovals(base), treeWithApprovals(tc.head))
			if len(tc.want) == 0 {
				if got != nil {
					t.Fatalf("findings %#v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("nil findings")
			}
			blob := formatDiags(got)
			for _, w := range tc.want {
				if !strings.Contains(blob, w) {
					t.Fatalf("missing %q in\n%s", w, blob)
				}
			}
			if tc.name == "rename" && strings.Contains(blob, "planning") {
				t.Fatalf("added gate flagged:\n%s", blob)
			}
			for _, d := range got {
				if d.File != "factory.yaml" || d.Severity != SeverityError || d.Line != 1 {
					t.Fatalf("diagnostic %+v", d)
				}
			}
		})
	}
}

func TestApprovalMutations(t *testing.T) {
	loaded, diags := LoadDir(filepath.Join("..", "..", "examples", "acme-factory"))
	if loaded == nil || loaded.Factory.Pipeline == nil {
		t.Fatalf("acme tree %#v", diags)
	}
	base := loaded.Factory.Pipeline.Approvals
	if base["spec"].Approvers != "role:editor" || base["handoff"].Approvers != "role:user" || base["merge"].Required {
		t.Fatalf("acme approvals %+v", base)
	}
	rng := rand.New(rand.NewSource(20261020))
	for i := 0; i < 1000; i++ {
		head := weakenAcme(base, rng)
		got := WeakensApprovals(treeWithApprovals(base), treeWithApprovals(head))
		if len(got) == 0 {
			t.Fatalf("mutation %d not flagged: %+v", i, head)
		}
	}
	rng = rand.New(rand.NewSource(20261021))
	for i := 0; i < 1000; i++ {
		head := acmeApprovals()
		head[fmt.Sprintf("extra-%d", i)] = ApprovalGate{
			Required:  rng.Intn(2) == 0,
			Approvers: []string{"role:editor", "role:user", "", "anyone"}[rng.Intn(4)],
		}
		if got := WeakensApprovals(treeWithApprovals(base), treeWithApprovals(head)); got != nil {
			t.Fatalf("added gate flagged: %+v", got)
		}
	}
}

func TestApprovalLinesFromFile(t *testing.T) {
	dir := t.TempDir()
	copyDir(t, filepath.Join("testdata", "valid", "m2"), dir)
	const factory = `schemaVersion: v1alpha1
name: gates
description: gates
agentDefaults:
  model: auto
  runner: local
pipeline:
  stages: [building]
  agents:
    building: implementer
  approvals:
    spec: { required: true, approvers: role:editor }
    handoff: { required: true, approvers: role:user }
    merge: { required: false }
`
	writeFile(t, filepath.Join(dir, "factory.yaml"), factory)
	base, _ := LoadDir(dir)
	if base.approvalLines["spec"] == 0 || base.approvalsLine == 0 {
		t.Fatalf("lines spec=%d approvals=%d", base.approvalLines["spec"], base.approvalsLine)
	}
	specLine := lineText(t, filepath.Join(dir, "factory.yaml"), base.approvalLines["spec"])
	if !strings.Contains(specLine, "spec:") {
		t.Fatalf("spec line %q", specLine)
	}

	headDir := t.TempDir()
	copyDir(t, dir, headDir)
	removed := strings.Replace(factory, "    spec: { required: true, approvers: role:editor }\n", "", 1)
	writeFile(t, filepath.Join(headDir, "factory.yaml"), removed)
	head, _ := LoadDir(headDir)
	got := WeakensApprovals(base, head)
	if len(got) != 1 || !strings.Contains(got[0].Message, `"spec" removed`) {
		t.Fatalf("%#v", got)
	}
	if got[0].Line != head.approvalsLine {
		t.Fatalf("line %d want approvals line %d", got[0].Line, head.approvalsLine)
	}
	if !strings.Contains(lineText(t, filepath.Join(headDir, "factory.yaml"), got[0].Line), "approvals:") {
		t.Fatalf("line %d is %q", got[0].Line, lineText(t, filepath.Join(headDir, "factory.yaml"), got[0].Line))
	}

	widened := strings.Replace(factory, `approvers: role:editor`, `approvers: role:user`, 1)
	writeFile(t, filepath.Join(headDir, "factory.yaml"), widened)
	head, _ = LoadDir(headDir)
	got = WeakensApprovals(base, head)
	if len(got) != 1 || !strings.Contains(got[0].Message, "widened") {
		t.Fatalf("%#v", got)
	}
	if got[0].Line != head.approvalLines["spec"] {
		t.Fatalf("line %d want %d", got[0].Line, head.approvalLines["spec"])
	}
}

func TestGuardRails(t *testing.T) {
	acme, _ := LoadDir(filepath.Join("..", "..", "examples", "acme-factory"))
	sc, ok := acme.Scorers["tests-run"]
	if !ok || !sc.SelfImprovement || sc.selfImprovementLine == 0 {
		t.Fatalf("scorer %+v", sc)
	}
	text := lineText(t, filepath.Join("..", "..", "examples", "acme-factory", "scorers", "tests-run", "scorer.md"), sc.selfImprovementLine)
	if !strings.Contains(text, "selfImprovement:") {
		t.Fatalf("line %d is %q", sc.selfImprovementLine, text)
	}
	if got := GuardRails(acme, acme); got != nil {
		t.Fatalf("same tree %#v", got)
	}
	bare := *acme
	bare.Scorers = map[string]Scorer{}
	got := GuardRails(&bare, acme)
	if len(got) != 1 {
		t.Fatalf("%#v", got)
	}
	d := got[0]
	if d.Severity != SeverityWarning || d.File != "scorers/tests-run/scorer.md" || d.Line != sc.selfImprovementLine {
		t.Fatalf("%+v", d)
	}
	if !strings.HasPrefix(d.Message, "privilege escalation:") {
		t.Fatalf("message %q", d.Message)
	}
	if got := GuardRails(nil, acme); len(got) != 1 || !strings.HasPrefix(got[0].Message, "privilege escalation:") {
		t.Fatalf("nil base %#v", got)
	}

	flippedBase := *acme
	flipped := sc
	flipped.SelfImprovement = false
	flippedBase.Scorers = map[string]Scorer{"tests-run": flipped}
	if got := GuardRails(&flippedBase, acme); len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("flip %#v", got)
	}
	staying := *acme
	staying.Scorers = map[string]Scorer{"tests-run": sc}
	if got := GuardRails(&staying, acme); got != nil {
		t.Fatalf("already true %#v", got)
	}
	off := sc
	off.SelfImprovement = false
	headOff := *acme
	headOff.Scorers = map[string]Scorer{"tests-run": off}
	if got := GuardRails(&bare, &headOff); got != nil {
		t.Fatalf("new false %#v", got)
	}

	base := treeWithApprovals(acmeApprovals())
	if got := GuardRails(base, nil); len(got) != 3 {
		t.Fatalf("nil head %#v", got)
	}
	if got := GuardRails(nil, nil); got != nil {
		t.Fatalf("nil nil %#v", got)
	}
}

func acmeApprovals() map[string]ApprovalGate {
	return map[string]ApprovalGate{
		"spec":    {Required: true, Approvers: "role:editor"},
		"handoff": {Required: true, Approvers: "role:user"},
		"merge":   {Required: false},
	}
}

func treeWithApprovals(gates map[string]ApprovalGate) *Tree {
	return &Tree{Factory: Factory{Pipeline: &Pipeline{Approvals: gates}}}
}

func withoutGate(gates map[string]ApprovalGate, name string) map[string]ApprovalGate {
	out := map[string]ApprovalGate{}
	for k, v := range gates {
		if k == name {
			continue
		}
		out[k] = v
	}
	return out
}

func weakenAcme(base map[string]ApprovalGate, rng *rand.Rand) map[string]ApprovalGate {
	head := map[string]ApprovalGate{}
	for k, v := range base {
		head[k] = v
	}
	name := []string{"spec", "handoff", "merge"}[rng.Intn(3)]
	if rng.Intn(2) == 0 {
		delete(head, name)
		return head
	}
	g := head[name]
	if g.Required && rng.Intn(2) == 0 {
		g.Required = false
	} else {
		g.Approvers = widerApprover(g.Approvers, rng)
	}
	head[name] = g
	return head
}

func widerApprover(cur string, rng *rand.Rand) string {
	switch strings.TrimSpace(cur) {
	case "role:editor":
		return []string{"role:user", "", "anyone"}[rng.Intn(3)]
	case "role:user":
		return []string{"", "anyone"}[rng.Intn(2)]
	default:
		if strings.TrimSpace(cur) == "" {
			return "anyone"
		}
		return ""
	}
}

func lineText(t *testing.T, path string, line int) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	if line < 1 || line > len(lines) {
		t.Fatalf("line %d of %d in %s", line, len(lines), path)
	}
	return lines[line-1]
}
