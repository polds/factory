package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polds/factory/internal/store"
)

func TestWorkItemCreateAndList(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	if _, err := h.st.ApplyDefinition(ctx, "beta", validDefinition()); err != nil {
		t.Fatal(err)
	}
	beta, err := h.st.GetFactory(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}

	item := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source:         store.SourceIssue,
		SourceRef:      "acme/payments#42",
		Title:          "Fix the link",
		Body:           "The docs link 404s.",
		Stage:          store.StagePlanning,
		Status:         store.WorkItemOpen,
		CreatedBy:      "editor",
		ConversationID: "conv_given",
		RevisionCount:  1,
	})
	if !strings.HasPrefix(item.ID, store.PrefixWorkItem) || len(item.ID) != len(store.PrefixWorkItem)+26 {
		t.Fatalf("id %q", item.ID)
	}
	if item.FactoryID != factoryID || item.Source != store.SourceIssue || item.SourceRef != "acme/payments#42" {
		t.Fatalf("identity %+v", item)
	}
	if item.Title != "Fix the link" || item.Body != "The docs link 404s." || item.CreatedBy != "editor" {
		t.Fatalf("text %+v", item)
	}
	if item.Stage != store.StagePlanning || item.Status != store.WorkItemOpen || item.RevisionCount != 1 {
		t.Fatalf("state %+v", item)
	}
	if item.ConversationID != "conv_given" || item.CreatedAt.IsZero() {
		t.Fatalf("conversation %+v", item)
	}
	got, err := h.st.GetWorkItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != item {
		t.Fatalf("get %+v want %+v", got, item)
	}
	if _, err := h.st.GetWorkItem(ctx, store.PrefixWorkItem+"missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing err %v", err)
	}

	def := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceManual, Title: "defaults"})
	if !strings.HasPrefix(def.ConversationID, store.PrefixConversation) || len(def.ConversationID) != len(store.PrefixConversation)+26 {
		t.Fatalf("conversation id %q", def.ConversationID)
	}
	if def.Stage != store.StageTriage || def.Status != store.WorkItemOpen || def.RevisionCount != 0 {
		t.Fatalf("defaults %+v", def)
	}
	other := createWorkItem(t, h, beta.ID, store.NewWorkItem{
		Source: store.SourceAPI,
		Title:  "beta",
		Stage:  store.StageBuilding,
		Status: store.WorkItemAwaitingInput,
	})

	listed, err := h.st.ListWorkItems(ctx, store.WorkItemFilter{FactoryID: factoryID})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || !containsID(listed, item.ID) || !containsID(listed, def.ID) || containsID(listed, other.ID) {
		t.Fatalf("factory list %+v", idsOfItems(listed))
	}
	byStage, err := h.st.ListWorkItems(ctx, store.WorkItemFilter{FactoryID: factoryID, Stage: store.StagePlanning})
	if err != nil {
		t.Fatal(err)
	}
	if len(byStage) != 1 || byStage[0].ID != item.ID {
		t.Fatalf("stage filter %+v", idsOfItems(byStage))
	}
	byStatus, err := h.st.ListWorkItems(ctx, store.WorkItemFilter{FactoryID: beta.ID, Status: store.WorkItemAwaitingInput})
	if err != nil {
		t.Fatal(err)
	}
	if len(byStatus) != 1 || byStatus[0].ID != other.ID {
		t.Fatalf("status filter %+v", idsOfItems(byStatus))
	}
	bySource, err := h.st.ListWorkItems(ctx, store.WorkItemFilter{FactoryID: factoryID, Source: store.SourceIssue})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySource) != 1 || bySource[0].ID != item.ID {
		t.Fatalf("source filter %+v", idsOfItems(bySource))
	}

	if _, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{FactoryID: factoryID, Source: "intake", Title: "nope"}); err == nil {
		t.Fatal("accepted intake as a source")
	}
	if _, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{FactoryID: factoryID, Source: store.SourceManual, Stage: "intake"}); err == nil {
		t.Fatal("accepted intake as a stage")
	}
	if _, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{Source: store.SourceManual, Title: "no factory"}); err == nil {
		t.Fatal("accepted missing factory")
	}
}

func TestWorkItemUpdateStage(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source:        store.SourceManual,
		Title:         "plan",
		RevisionCount: 2,
	})

	updated, err := h.st.UpdateWorkItemStage(ctx, item.ID, store.StagePlanning, store.WorkItemAwaitingApproval)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Stage != store.StagePlanning || updated.Status != store.WorkItemAwaitingApproval || updated.RevisionCount != 2 {
		t.Fatalf("updated %+v", updated)
	}
	if _, err := h.st.UpdateWorkItemStage(ctx, item.ID, "intake", store.WorkItemOpen); err == nil {
		t.Fatal("accepted invalid stage")
	}
	if _, err := h.st.UpdateWorkItemStage(ctx, item.ID, store.StageBuilding, "done"); err == nil {
		t.Fatal("accepted invalid status")
	}
	still, err := h.st.GetWorkItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Stage != store.StagePlanning || still.Status != store.WorkItemAwaitingApproval {
		t.Fatalf("stage changed after invalid update %+v", still)
	}
	if _, err := h.st.UpdateWorkItemStage(ctx, store.PrefixWorkItem+"missing", store.StageTriage, store.WorkItemOpen); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing err %v", err)
	}

	bumped, err := h.st.SetWorkItemRevision(ctx, item.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if bumped.RevisionCount != 3 || bumped.Stage != store.StagePlanning {
		t.Fatalf("revision %+v", bumped)
	}
	if _, err := h.st.SetWorkItemRevision(ctx, item.ID, -1); err == nil {
		t.Fatal("accepted negative revision")
	}
	if _, err := h.st.SetWorkItemRevision(ctx, store.PrefixWorkItem+"missing", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing revision err %v", err)
	}
}

func TestWorkItemCreateRunParent(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	if _, err := h.st.ApplyDefinition(ctx, "beta", validDefinition()); err != nil {
		t.Fatal(err)
	}
	beta, err := h.st.GetFactory(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source: store.SourceAPI,
		Title:  "implement",
		Stage:  store.StageBuilding,
	})
	parent := h.createRun(t, factoryID, agentID, "default", store.PriorityPipeline)
	if parent.WorkItemID != "" || parent.ParentRunID != "" || parent.ConversationID != "" || parent.Stage != "" || parent.ModelID != "" {
		t.Fatalf("m2 run gained linkage %+v", parent)
	}

	child, err := h.st.CreateRun(ctx, store.NewRun{
		FactoryID:      factoryID,
		AgentID:        agentID,
		Pool:           "default",
		Priority:       store.PriorityPipeline,
		Prompt:         "implement the fix",
		Title:          "implement",
		WorkItemID:     item.ID,
		ParentRunID:    parent.ID,
		ConversationID: item.ConversationID,
		Stage:          store.StageBuilding,
		ModelID:        "model-build",
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.WorkItemID != item.ID || child.ParentRunID != parent.ID {
		t.Fatalf("linkage %+v", child)
	}
	if child.ConversationID != item.ConversationID || child.Stage != store.StageBuilding || child.ModelID != "model-build" {
		t.Fatalf("handoff %+v", child)
	}
	if child.Status != store.StatusQueued || child.Prompt != "implement the fix" {
		t.Fatalf("run %+v", child)
	}
	loaded, err := h.st.GetRun(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != child {
		t.Fatalf("get %+v want %+v", loaded, child)
	}

	_, err = h.st.CreateRun(ctx, store.NewRun{
		FactoryID:   factoryID,
		Pool:        "default",
		Priority:    store.PriorityBatch,
		ParentRunID: store.PrefixRun + "missing",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing parent err %v", err)
	}
	_, err = h.st.CreateRun(ctx, store.NewRun{
		FactoryID:  factoryID,
		Pool:       "default",
		Priority:   store.PriorityBatch,
		WorkItemID: store.PrefixWorkItem + "missing",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing work item err %v", err)
	}
	_, err = h.st.CreateRun(ctx, store.NewRun{
		FactoryID:  beta.ID,
		Pool:       "default",
		Priority:   store.PriorityBatch,
		WorkItemID: item.ID,
	})
	if err == nil {
		t.Fatal("accepted a work item from another factory")
	}

	betaRun, err := h.st.CreateRun(ctx, store.NewRun{
		FactoryID: beta.ID,
		Pool:      "default",
		Priority:  store.PriorityBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.st.CreateRun(ctx, store.NewRun{
		FactoryID:   factoryID,
		Pool:        "default",
		Priority:    store.PriorityBatch,
		ParentRunID: betaRun.ID,
	})
	if err == nil {
		t.Fatal("accepted a parent run from another factory")
	}
}

func TestApprovalRequestAndDecideIdempotent(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	if _, err := h.st.ApplyDefinition(ctx, "beta", validDefinition()); err != nil {
		t.Fatal(err)
	}
	beta, err := h.st.GetFactory(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "needs spec"})
	other := createWorkItem(t, h, beta.ID, store.NewWorkItem{Source: store.SourceManual, Title: "other"})

	spec, err := h.st.RequestApproval(ctx, item.ID, "spec")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(spec.ID, store.PrefixApproval) || len(spec.ID) != len(store.PrefixApproval)+26 {
		t.Fatalf("id %q", spec.ID)
	}
	if spec.WorkItemID != item.ID || spec.Gate != "spec" || spec.Decision != "" || spec.DecidedAt != nil {
		t.Fatalf("pending %+v", spec)
	}
	if spec.RequestedAt.IsZero() {
		t.Fatal("requested_at is zero")
	}
	again, err := h.st.RequestApproval(ctx, item.ID, "spec")
	if err != nil {
		t.Fatal(err)
	}
	if !approvalsEqual(again, spec) {
		t.Fatalf("second request %+v want %+v", again, spec)
	}
	handoff, err := h.st.RequestApproval(ctx, item.ID, "handoff")
	if err != nil {
		t.Fatal(err)
	}
	if handoff.ID == spec.ID || handoff.Gate != "handoff" {
		t.Fatalf("handoff %+v", handoff)
	}
	betaGate, err := h.st.RequestApproval(ctx, other.ID, "spec")
	if err != nil {
		t.Fatal(err)
	}

	decided, err := h.st.DecideApproval(ctx, spec.ID, store.DecisionApprove, "editor", "looks right")
	if err != nil {
		t.Fatal(err)
	}
	if decided.Decision != store.DecisionApprove || decided.DecidedBy != "editor" || decided.Note != "looks right" || decided.DecidedAt == nil {
		t.Fatalf("decided %+v", decided)
	}
	replay, err := h.st.DecideApproval(ctx, spec.ID, store.DecisionApprove, "someone", "different note")
	if err != nil {
		t.Fatal(err)
	}
	if !approvalsEqual(replay, decided) {
		t.Fatalf("replay %+v want %+v", replay, decided)
	}
	if _, err := h.st.DecideApproval(ctx, spec.ID, store.DecisionReject, "editor", "no"); !errors.Is(err, store.ErrAlreadyDecided) {
		t.Fatalf("conflicting decide err %v", err)
	}

	pending, err := h.st.ListApprovals(ctx, factoryID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != handoff.ID {
		t.Fatalf("pending %+v", pending)
	}
	all, err := h.st.ListApprovals(ctx, factoryID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all %+v", all)
	}
	var sawApproved bool
	for _, row := range all {
		if row.ID == betaGate.ID {
			t.Fatal("listed another factory's approval")
		}
		if row.ID == spec.ID {
			sawApproved = row.Decision == store.DecisionApprove && row.Note == "looks right"
		}
	}
	if !sawApproved {
		t.Fatalf("approved row missing %+v", all)
	}

	reopened, err := h.st.RequestApproval(ctx, item.ID, "spec")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID == spec.ID || reopened.Decision != "" {
		t.Fatalf("re-request %+v", reopened)
	}
	pending, err = h.st.ListApprovals(ctx, factoryID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || !containsApproval(pending, handoff.ID) || !containsApproval(pending, reopened.ID) {
		t.Fatalf("pending after re-request %+v", pending)
	}
}

func TestApprovalDecideCommitsBeforeRequestReturns(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "race"})
	pending, err := h.st.RequestApproval(ctx, item.ID, "spec")
	if err != nil {
		t.Fatal(err)
	}

	var decideErr error
	h.st.SetPendingLookupHookForTest(func(hookCtx context.Context) {
		_, decideErr = h.st.DecideApproval(hookCtx, pending.ID, store.DecisionApprove, "editor", "committed first")
	})
	t.Cleanup(func() { h.st.SetPendingLookupHookForTest(nil) })

	got, err := h.st.RequestApproval(ctx, item.ID, "spec")
	if decideErr != nil {
		t.Fatal(decideErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := h.raw.QueryRow(ctx, `SELECT decision FROM approvals WHERE id = $1`, got.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if got.Decision != stored {
		t.Fatalf("returned decision %q, stored decision %q for %s", got.Decision, stored, got.ID)
	}
	if got.ID == pending.ID || got.Decision != "" {
		t.Fatalf("returned decided row %+v", got)
	}
	var original string
	if err := h.raw.QueryRow(ctx, `SELECT decision FROM approvals WHERE id = $1`, pending.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if original != store.DecisionApprove {
		t.Fatalf("original decision %q", original)
	}
}

func TestApprovalDecideUnknown(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceManual, Title: "gate"})
	pending, err := h.st.RequestApproval(ctx, item.ID, "spec")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DecideApproval(ctx, store.PrefixApproval+"missing", store.DecisionApprove, "editor", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown err %v", err)
	}
	if _, err := h.st.DecideApproval(ctx, pending.ID, "", "editor", ""); err == nil {
		t.Fatal("accepted empty decision")
	}
	if _, err := h.st.DecideApproval(ctx, pending.ID, "yes", "editor", ""); err == nil {
		t.Fatal("accepted invalid decision")
	}
	still, err := h.st.ListApprovals(ctx, factoryID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 || still[0] != pending {
		t.Fatalf("pending changed %+v", still)
	}
}

func TestOpenApprovalGateCommitsWithStatus(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source: store.SourceManual,
		Title:  "gate",
		Stage:  store.StagePlanning,
		Status: store.WorkItemOpen,
	})

	var duringStatus string
	var duringPending int
	h.st.SetOpenGateHookForTest(func(hookCtx context.Context) error {
		if err := h.raw.QueryRow(hookCtx, `SELECT status FROM work_items WHERE id = $1`, item.ID).Scan(&duringStatus); err != nil {
			return err
		}
		return h.raw.QueryRow(hookCtx, `
			SELECT count(*) FROM approvals
			WHERE work_item_id = $1 AND decision = ''`, item.ID).Scan(&duringPending)
	})
	t.Cleanup(func() { h.st.SetOpenGateHookForTest(nil) })

	approval, updated, err := h.st.OpenApprovalGate(ctx, item.ID, "spec", store.StagePlanning, store.WorkItemAwaitingApproval, 3)
	if err != nil {
		t.Fatal(err)
	}
	if duringStatus != store.WorkItemOpen || duringPending != 0 {
		t.Fatalf("uncommitted gate was visible: status %q pending %d", duringStatus, duringPending)
	}
	if approval.Gate != "spec" || approval.Decision != "" || approval.WorkItemID != item.ID {
		t.Fatalf("approval %+v", approval)
	}
	if updated.Status != store.WorkItemAwaitingApproval || updated.Stage != store.StagePlanning || updated.RevisionCount != 3 {
		t.Fatalf("updated %+v", updated)
	}
	var status string
	var pending int
	if err := h.raw.QueryRow(ctx, `SELECT status FROM work_items WHERE id = $1`, item.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE work_item_id = $1 AND decision = ''`, item.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if status != store.WorkItemAwaitingApproval || pending != 1 {
		t.Fatalf("after commit status %q pending %d", status, pending)
	}

	again, still, err := h.st.OpenApprovalGate(ctx, item.ID, "spec", store.StagePlanning, store.WorkItemAwaitingApproval, 3)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != approval.ID || still.Status != store.WorkItemAwaitingApproval {
		t.Fatalf("second open approval %+v item %+v", again, still)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE work_item_id = $1`, item.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending rows %d", pending)
	}

	other := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceManual, Title: "rollback"})
	h.st.SetOpenGateHookForTest(func(context.Context) error { return errors.New("boom") })
	if _, _, err := h.st.OpenApprovalGate(ctx, other.ID, "spec", store.StagePlanning, store.WorkItemAwaitingApproval, 0); err == nil {
		t.Fatal("hook error committed")
	}
	if err := h.raw.QueryRow(ctx, `SELECT status FROM work_items WHERE id = $1`, other.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE work_item_id = $1`, other.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if status != store.WorkItemOpen || pending != 0 {
		t.Fatalf("rolled back status %q pending %d", status, pending)
	}
}

func TestApprovalRequiresWorkItem(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	if _, err := h.st.RequestApproval(ctx, store.PrefixWorkItem+"missing", "spec"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing work item err %v", err)
	}
	if _, err := h.st.RequestApproval(ctx, "", "spec"); err == nil {
		t.Fatal("accepted empty work item")
	}
	_, err := h.raw.Exec(ctx, `
		INSERT INTO approvals (id, work_item_id, gate)
		VALUES ('apr_rawmissing', 'task_missing', 'spec')`)
	if err == nil {
		t.Fatal("fk accepted an approval without a work item")
	}
}

func createWorkItem(t *testing.T, h *harness, factoryID string, w store.NewWorkItem) store.WorkItem {
	t.Helper()
	if w.FactoryID == "" {
		w.FactoryID = factoryID
	}
	item, err := h.st.CreateWorkItem(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func idsOfItems(items []store.WorkItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}

func containsID(items []store.WorkItem, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func approvalsEqual(a, b store.Approval) bool {
	if a.DecidedAt == nil || b.DecidedAt == nil {
		if a.DecidedAt != b.DecidedAt {
			return false
		}
	} else if !a.DecidedAt.Equal(*b.DecidedAt) {
		return false
	}
	a.DecidedAt = nil
	b.DecidedAt = nil
	return a == b
}

func containsApproval(rows []store.Approval, id string) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}
