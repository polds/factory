package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func TestNativeBundleEnvelopeAndGoal(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID := seedNativeInvocation(t, h, "bundle-envelope")
	legacyBefore := countWhere(t, h, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, invocationID)
	if _, _, err := h.st.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationID, AgentID: agentID,
		Tree: emptyProvenance(proofTree()), Slots: 1, MaxLive: 4, CeilingCents: 2500,
	}); err == nil || legacyBefore != countWhere(t, h, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, invocationID) {
		t.Fatal("all-empty provenance inserted a policy")
	}
	unknownID := insertNativeInvocation(t, h, factoryID, "bundle-unknown")
	bad := bundledProof()
	bad.BundleMode = "other"
	if _, _, err := h.st.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: unknownID, AgentID: agentID,
		Tree: bad, Slots: 1, MaxLive: 4, CeilingCents: 2500,
	}); err == nil || countWhere(t, h, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, unknownID) != 0 {
		t.Fatal("unknown mode inserted a policy")
	}
	policy, task, err := h.st.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationID, AgentID: agentID,
		Tree: bundledProof(), Slots: 1, MaxLive: 4, CeilingCents: 2500,
	})
	if err != nil || policy.BundleMode != definition.BundleModeRoleFiles {
		t.Fatalf("open %+v %v", policy.BundleMode, err)
	}
	var raw string
	if err := h.raw.QueryRow(ctx, `SELECT personas::text FROM native_policies WHERE invocation_id = $1`, invocationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "bundleMode") || !strings.Contains(raw, "role_files") || strings.HasPrefix(strings.TrimSpace(raw), "[") {
		t.Fatalf("new envelope dropped bundleMode: %s", raw)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE native_policies SET personas = personas - 'bundleMode' WHERE invocation_id = $1`, invocationID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := h.st.GetNativePolicy(ctx, invocationID)
	if err != nil || reloaded.BundleMode != definition.BundleModeRoleFiles || reloaded.Digest != policy.Digest {
		t.Fatalf("infer %+v %v", reloaded.BundleMode, err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT personas::text FROM native_policies WHERE invocation_id = $1`, invocationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "bundleMode") {
		t.Fatal("read rewrote a stored envelope")
	}
	if !personaBytesEqual(reloaded.Personas, policy.Personas) {
		t.Fatalf("inferred reload lost file bytes got=%d want=%d", len(reloaded.Personas), len(policy.Personas))
	}
	body := "# File AGENTS.md\nbytes: 4\nNOPE"
	item, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID, Source: store.SourceManual, Title: "bundle title", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("a", 40)
	if _, err := h.raw.Exec(ctx, `
		UPDATE invocations
		SET work_item_id = $2, pinned_head_sha = $3, target_repository = 'example/fixture'
		WHERE id = $1`, invocationID, item.ID, pin); err != nil {
		t.Fatal(err)
	}
	queueBefore := countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id IN (SELECT id FROM runs WHERE factory_id = $1)`, factoryID)
	if _, err := h.raw.Exec(ctx, `UPDATE work_items SET body = '' WHERE id = $1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.ClaimActivation(ctx, h.st, task.ID, store.ClaimInput{AgentID: agentID}); err == nil {
		t.Fatal("empty body queued")
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id IN (SELECT id FROM runs WHERE factory_id = $1)`, factoryID) != queueBefore {
		t.Fatal("empty body inserted a queue row")
	}
	if _, err := h.raw.Exec(ctx, `UPDATE work_items SET body = $2 WHERE id = $1`, item.ID, strings.Repeat("x", definition.NativeGoalBodyCap+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.ClaimActivation(ctx, h.st, task.ID, store.ClaimInput{AgentID: agentID}); err == nil {
		t.Fatal("overflow queued")
	}
	if _, err := h.raw.Exec(ctx, `UPDATE work_items SET body = $2, title = $3 WHERE id = $1`, item.ID, body, strings.Repeat("t", definition.NativeGoalBodyCap)); err != nil {
		t.Fatal(err)
	}
	act, err := coordination.ClaimActivation(ctx, h.st, task.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.st.GetRun(ctx, act.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ParentRunID != "" || !strings.Contains(run.Prompt, "task: "+task.ID+"\n") || strings.Contains(run.Prompt, "task: "+run.ID+"\n") {
		t.Fatalf("task identity prompt parent %q", run.ParentRunID)
	}
	if goalBody(t, run.Prompt) != body {
		t.Fatal("body bytes")
	}
	again, err := h.st.GetNativePolicy(ctx, invocationID)
	if err != nil || again.BundleMode != definition.BundleModeRoleFiles {
		t.Fatal(again.BundleMode)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE native_policies SET personas = '{"personas":null}' WHERE invocation_id = $1`, invocationID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetNativePolicy(ctx, invocationID); !errors.Is(err, store.ErrInputInvalid) {
		t.Fatalf("corrupt %v", err)
	}
}

func bundledProof() definition.NativeTree {
	tree := proofTree()
	tree.SourceCommit = "source-commit"
	tree.RosterSHA256 = strings.Repeat("ab", 32)
	tree.RosterGitBlob = strings.Repeat("cd", 20)
	tree.BundleMode = definition.BundleModeRoleFiles
	for i := range tree.Personas {
		name := tree.Personas[i].Name
		tree.Personas[i].Files = []definition.RoleFile{
			{Name: "AGENTS.md", Bytes: []byte("A:" + name)},
			{Name: "HEARTBEAT.md", Bytes: []byte("H:" + name)},
			{Name: "SOUL.md", Bytes: []byte("S:" + name)},
			{Name: "TOOLS.md", Bytes: []byte("T:" + name)},
		}
	}
	return tree
}

func emptyProvenance(tree definition.NativeTree) definition.NativeTree {
	tree.SourceCommit = "source-commit"
	tree.RosterSHA256 = strings.Repeat("ab", 32)
	tree.RosterGitBlob = strings.Repeat("cd", 20)
	return tree
}

func personaBytesEqual(got, want []definition.NativePersona) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if len(got[i].Files) != len(want[i].Files) {
			return false
		}
		for j := range got[i].Files {
			if string(got[i].Files[j].Bytes) != string(want[i].Files[j].Bytes) || got[i].Files[j].Name != want[i].Files[j].Name {
				return false
			}
		}
	}
	return true
}

func goalBody(t *testing.T, prompt string) string {
	t.Helper()
	const header = "# Body\nbytes: "
	i := strings.LastIndex(prompt, header)
	if i < 0 {
		t.Fatal("body header")
	}
	rest := prompt[i+len(header):]
	nl := strings.IndexByte(rest, '\n')
	var n int
	for _, c := range rest[:nl] {
		n = n*10 + int(c-'0')
	}
	return rest[nl+1 : nl+1+n]
}
