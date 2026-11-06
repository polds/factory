package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

func TestNativeTaskIdentitiesAreDistinct(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID := seedNativeInvocation(t, h, "identities")
	activationID, err := store.NewID(store.PrefixNativeActivation)
	if err != nil {
		t.Fatal(err)
	}
	executionID, err := store.NewID(store.PrefixExec)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := h.st.InsertNativeTask(ctx, store.NativeTaskInput{
		FactoryID: factoryID, InvocationID: invocationID, Assignee: "blocker",
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := h.st.InsertNativeTask(ctx, store.NativeTaskInput{
		FactoryID: factoryID, InvocationID: invocationID, Assignee: "parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := h.st.InsertNativeTask(ctx, store.NativeTaskInput{
		FactoryID:    factoryID,
		InvocationID: invocationID,
		ParentTaskID: parent.ID,
		Assignee:     "lead",
		BlockerIDs:   []string{blocker.ID},
		ActivationID: activationID,
		ExecutionID:  executionID,
		BudgetScope:  "factory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskRef != task.ID || !strings.HasPrefix(task.ID, store.PrefixNativeTask) {
		t.Fatalf("task ref %+v", task)
	}
	fields := map[string]string{
		"task": task.ID, "assignee": task.Assignee, "parent": task.ParentTaskID,
		"blocker": task.BlockerIDs[0], "activation": task.ActivationID, "execution": task.ExecutionID,
	}
	seen := map[string]string{}
	for name, value := range fields {
		if value == "" {
			t.Fatalf("empty %s", name)
		}
		if prev, ok := seen[value]; ok {
			t.Fatalf("%s and %s share %s", name, prev, value)
		}
		seen[value] = name
	}
	if task.Status != store.NativeTaskPending {
		t.Fatalf("status %s", task.Status)
	}
	var parentRunCols, linearCols int
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'native_tasks' AND column_name = 'parent_run_id'`).Scan(&parentRunCols); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'native_tasks' AND column_name = 'linear_issue'`).Scan(&linearCols); err != nil {
		t.Fatal(err)
	}
	if parentRunCols != 0 || linearCols != 0 {
		t.Fatalf("parent_run_id cols %d linear_issue cols %d", parentRunCols, linearCols)
	}
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityInteractive)
	_, err = h.st.InsertNativeTask(ctx, store.NativeTaskInput{
		FactoryID: factoryID, InvocationID: invocationID, Assignee: "lead", ParentTaskID: run.ID,
	})
	if !errors.Is(err, store.ErrInputInvalid) {
		t.Fatalf("parent run id err %v", err)
	}
}

func TestNativeTaskFactoryIsolation(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "iso")
	task, err := h.st.InsertNativeTask(ctx, store.NativeTaskInput{
		FactoryID: factoryID, InvocationID: invocationID, Assignee: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ('fac_other', 'other', 'v1alpha1', 'test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetNativeTask(ctx, "fac_other", task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross factory err %v", err)
	}
	page, err := h.st.ReadInbox(ctx, store.InboxQuery{FactoryID: "fac_other", Assignee: "lead", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Deliveries) != 0 {
		t.Fatalf("foreign inbox %+v", page.Deliveries)
	}
}

func TestAppendLedgerSequenceIsPerInvocation(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, left, _ := seedNativeInvocation(t, h, "seq-left")
	right := insertNativeInvocation(t, h, factoryID, "seq-right")
	var before int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM run_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	first, err := h.st.AppendLedger(ctx, left, store.LedgerEntry{Kind: store.LedgerKindResult, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.st.AppendLedger(ctx, right, store.LedgerEntry{Kind: store.LedgerKindResult, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if first.LedgerSeq != 1 || second.LedgerSeq != 1 {
		t.Fatalf("seqs %d %d", first.LedgerSeq, second.LedgerSeq)
	}
	var after int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM run_events`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("run_events %d -> %d", before, after)
	}
}

func TestAppendLedgerBodyCap(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	_, invocationID, _ := seedNativeInvocation(t, h, "body")
	_, err := h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
		Kind: store.LedgerKindResult, Body: strings.Repeat("a", store.NativeBodyCap), Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
		Kind: store.LedgerKindResult, Body: strings.Repeat("a", store.NativeBodyCap+1),
	})
	if !errors.Is(err, store.ErrInputInvalid) {
		t.Fatalf("oversize err %v", err)
	}
}

func TestAppendLedgerRollbackLeavesNoOutbox(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	_, invocationID, _ := seedNativeInvocation(t, h, "rollback")
	tx, err := h.raw.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendLedgerTxForTest(ctx, tx, invocationID, store.LedgerEntry{Kind: store.LedgerKindResult, Payload: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var events, outbox, state, ledgers int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM invocation_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM invocation_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM invocation_ledger_state`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM invocation_ledgers`).Scan(&ledgers); err != nil {
		t.Fatal(err)
	}
	if events != 0 || outbox != 0 || state != 0 || ledgers != 0 {
		t.Fatalf("events %d outbox %d state %d ledgers %d", events, outbox, state, ledgers)
	}
}

func TestInboxProjectionHasNoSecondBody(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "projection")
	task := insertNative(t, h, factoryID, invocationID, "lead")
	rec, err := h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
		Kind: store.LedgerKindComment, TaskID: task.ID, Assignee: "lead", Body: "canonical", Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var bodyCols int
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'native_deliveries' AND column_name = 'body'`).Scan(&bodyCols); err != nil {
		t.Fatal(err)
	}
	if bodyCols != 0 {
		t.Fatalf("delivery body columns %d", bodyCols)
	}
	page, err := h.st.ReadInbox(ctx, store.InboxQuery{FactoryID: factoryID, Assignee: "lead", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Deliveries) != 1 || page.Deliveries[0].Body != "canonical" || page.Deliveries[0].MessageID != rec.MessageID {
		t.Fatalf("page %+v", page)
	}
	var eventBody string
	if err := h.raw.QueryRow(ctx, `SELECT body FROM invocation_events WHERE message_id = $1`, rec.MessageID).Scan(&eventBody); err != nil {
		t.Fatal(err)
	}
	if eventBody != page.Deliveries[0].Body {
		t.Fatalf("projection %q event %q", page.Deliveries[0].Body, eventBody)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO native_deliveries (id, message_id, invocation_id, ledger_seq, task_id, assignee)
		VALUES ('ndlv_orphan', 'nmsg_missing', $1, 99, $2, 'lead')`, invocationID, task.ID); err == nil {
		t.Fatal("orphan delivery inserted")
	}
}

func seedNativeInvocation(t *testing.T, h *harness, key string) (factoryID, invocationID, agentID string) {
	t.Helper()
	factoryID, agentID = h.seed(t)
	row, err := h.st.InsertInvocation(context.Background(), store.InsertInvocationParams{
		FactoryID:           factoryID,
		ProfileName:         "scan",
		SourceOccurrenceKey: key,
		CanonicalDigest:     "digest-" + key,
		DefinitionDigest:    "def",
		State:               invocation.StateAccepted,
		Intent:              "new",
		Priority:            "interactive",
		PrincipalJSON:       json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
	})
	if err != nil || !row.Created {
		t.Fatalf("invocation %+v err %v", row, err)
	}
	return factoryID, row.Row.ID, agentID
}

func insertNativeInvocation(t *testing.T, h *harness, factoryID, key string) string {
	t.Helper()
	row, err := h.st.InsertInvocation(context.Background(), store.InsertInvocationParams{
		FactoryID:           factoryID,
		ProfileName:         "scan",
		SourceOccurrenceKey: key,
		CanonicalDigest:     "digest-" + key,
		DefinitionDigest:    "def",
		State:               invocation.StateAccepted,
		Intent:              "new",
		Priority:            "interactive",
		PrincipalJSON:       json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
	})
	if err != nil || !row.Created {
		t.Fatalf("invocation %+v err %v", row, err)
	}
	return row.Row.ID
}

func insertNative(t *testing.T, h *harness, factoryID, invocationID, assignee string) store.NativeTask {
	t.Helper()
	task, err := h.st.InsertNativeTask(context.Background(), store.NativeTaskInput{
		FactoryID: factoryID, InvocationID: invocationID, Assignee: assignee,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
