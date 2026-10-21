package definition

import (
	"fmt"
	"sort"
	"strings"
)

// approverRank is the docs/03 ordering. role:editor outranks role:user.
// Anything else is unranked. A change to a higher rank is not a widening.
func approverRank(s string) (rank int, known bool) {
	switch strings.TrimSpace(s) {
	case "role:editor":
		return 2, true
	case "role:user":
		return 1, true
	default:
		return 0, false
	}
}

// approversWidened reports whether new approvers are a wider set than old.
// Empty, a lower documented rank, and any change to an unranked value are
// wider. A change to a higher documented rank is not.
func approversWidened(old, new string) bool {
	old = strings.TrimSpace(old)
	new = strings.TrimSpace(new)
	if old == new {
		return false
	}
	if new == "" {
		return true
	}
	newRank, newKnown := approverRank(new)
	if !newKnown {
		return true
	}
	oldRank, oldKnown := approverRank(old)
	if !oldKnown {
		return false
	}
	return newRank < oldRank
}

func approvalsOf(t *Tree) map[string]ApprovalGate {
	if t == nil || t.Factory.Pipeline == nil {
		return nil
	}
	return t.Factory.Pipeline.Approvals
}

func recordApprovalLines(tree *Tree, lines map[string]int) {
	if tree == nil {
		return
	}
	tree.approvalLines = map[string]int{}
	if lines == nil {
		return
	}
	tree.approvalsLine = lineAt(lines, "pipeline.approvals")
	p := tree.Factory.Pipeline
	if p == nil {
		return
	}
	for name := range p.Approvals {
		tree.approvalLines[name] = lineAt(lines, "pipeline.approvals."+name)
	}
}

func gateLine(head *Tree, name string) int {
	if head == nil {
		return 1
	}
	if n := head.approvalLines[name]; n > 0 {
		return n
	}
	if head.approvalsLine > 0 {
		return head.approvalsLine
	}
	return 1
}

// WeakensApprovals reports approval gates that head weakens relative to base.
// A gate key present in base and absent from head is removed or renamed.
// required true to false, and approversWidened, are weakenings. Adding a
// gate is not. Findings are SeverityError on factory.yaml, at the head line
// when that line was recorded.
func WeakensApprovals(base, head *Tree) []Diagnostic {
	baseGates := approvalsOf(base)
	if len(baseGates) == 0 {
		return nil
	}
	headGates := approvalsOf(head)
	names := make([]string, 0, len(baseGates))
	for name := range baseGates {
		names = append(names, name)
	}
	sort.Strings(names)
	var diags []Diagnostic
	for _, name := range names {
		before := baseGates[name]
		after, ok := headGates[name]
		if !ok {
			addDiag(&diags, "factory.yaml", gateLine(head, name), SeverityError, fmt.Sprintf("approval gate %q removed", name))
			continue
		}
		if before.Required && !after.Required {
			addDiag(&diags, "factory.yaml", gateLine(head, name), SeverityError, fmt.Sprintf("approval gate %q required weakened from true to false", name))
		}
		if approversWidened(before.Approvers, after.Approvers) {
			addDiag(&diags, "factory.yaml", gateLine(head, name), SeverityError, fmt.Sprintf("approval gate %q approvers widened from %q to %q", name, before.Approvers, after.Approvers))
		}
	}
	if len(diags) == 0 {
		return nil
	}
	sortDiags(diags)
	return diags
}

// GuardRails is the single guard-rail entry point. It reports WeakensApprovals
// (rail 7, SeverityError) and a scorer that is new with selfImprovement true
// or flipped from false to true (rail 5, SeverityWarning). A nil base is an
// empty tree, so every head scorer with selfImprovement true is then new.
// A nil head is an empty tree. T63 rejects on errors and flags warnings.
func GuardRails(base, head *Tree) []Diagnostic {
	if base == nil {
		base = &Tree{}
	}
	if head == nil {
		head = &Tree{}
	}
	out := append([]Diagnostic{}, WeakensApprovals(base, head)...)
	out = append(out, selfImprovementEscalations(base, head)...)
	if len(out) == 0 {
		return nil
	}
	sortDiags(out)
	return out
}

func selfImprovementEscalations(base, head *Tree) []Diagnostic {
	if head == nil || len(head.Scorers) == 0 {
		return nil
	}
	var names []string
	for name, sc := range head.Scorers {
		if !sc.SelfImprovement {
			continue
		}
		prev, existed := base.Scorers[name]
		if existed && prev.SelfImprovement {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var diags []Diagnostic
	for _, name := range names {
		sc := head.Scorers[name]
		addDiag(&diags, "scorers/"+name+"/scorer.md", sc.selfImprovementLine, SeverityWarning, fmt.Sprintf("privilege escalation: scorer %q sets selfImprovement true", name))
	}
	return diags
}
