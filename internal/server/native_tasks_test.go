package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/store"
)

func TestNativeCoordinationHTTP(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	inv := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": "native:spine:1",
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusCreated)
	invocationID, _ := inv["invocationId"].(string)
	if invocationID == "" {
		t.Fatalf("invocation %+v", inv)
	}
	var runsBefore int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs`).Scan(&runsBefore); err != nil {
		t.Fatal(err)
	}
	c := &client.Client{BaseURL: env.base, APIKey: env.apiKey}
	ctx := context.Background()
	task, err := c.CreateNativeTask(ctx, "pipe", client.NativeTaskRequest{
		InvocationID: invocationID,
		Assignee:     "spec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskRef != task.ID || task.LinearIssue != nil || task.ActivationID != "" || task.ExecutionID != "" || task.Status != store.NativeTaskPending {
		t.Fatalf("task %+v", task)
	}
	if !strings.HasPrefix(task.ID, "ntask_") {
		t.Fatalf("id %s", task.ID)
	}
	mention, err := c.CommentNativeTask(ctx, "pipe", task.ID, client.NativeCommentRequest{Body: "fyi", Kind: "mention"})
	if err != nil {
		t.Fatal(err)
	}
	if mention.WakePending {
		t.Fatal("mention woke")
	}
	comment, err := c.CommentNativeTask(ctx, "pipe", task.ID, client.NativeCommentRequest{Body: "see Bearer supersecretvalue"})
	if err != nil {
		t.Fatal(err)
	}
	if !comment.WakePending || comment.DeliveryID == "" {
		t.Fatalf("comment %+v", comment)
	}
	page, err := c.ReadNativeInbox(ctx, "pipe", "spec", "", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Deliveries) != 1 || page.NextCursor == "" {
		t.Fatalf("page %+v", page)
	}
	next, err := c.ReadNativeInbox(ctx, "pipe", "spec", page.NextCursor, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Deliveries) != 1 || strings.Contains(next.Deliveries[0].Body, "supersecretvalue") || !strings.Contains(next.Deliveries[0].Body, "Bearer [REDACTED]") {
		t.Fatalf("redacted page %+v", next)
	}
	var acks int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM native_acks`).Scan(&acks); err != nil {
		t.Fatal(err)
	}
	if acks != 0 {
		t.Fatalf("read acked %d", acks)
	}
	applied, err := c.ApplyNativeMessage(ctx, "pipe", task.ID, comment.MessageID)
	if err != nil || !applied.Applied {
		t.Fatalf("apply %+v %v", applied, err)
	}
	ack, err := c.AckNativeDelivery(ctx, "pipe", task.ID, comment.DeliveryID)
	if err != nil || ack.Duplicate {
		t.Fatalf("ack %+v %v", ack, err)
	}
	again, err := c.AckNativeDelivery(ctx, "pipe", task.ID, comment.DeliveryID)
	if err != nil || !again.Duplicate {
		t.Fatalf("dup ack %+v %v", again, err)
	}
	redelivery, err := c.RedeliverNativeMessage(ctx, "pipe", task.ID, comment.MessageID)
	if err != nil || redelivery.DeliveryID == comment.DeliveryID {
		t.Fatalf("redeliver %+v %v", redelivery, err)
	}
	secondApply, err := c.ApplyNativeMessage(ctx, "pipe", task.ID, comment.MessageID)
	if err != nil || secondApply.Applied {
		t.Fatalf("second apply %+v %v", secondApply, err)
	}
	if _, err := c.AckNativeDelivery(ctx, "pipe", task.ID, redelivery.DeliveryID); err != nil {
		t.Fatal(err)
	}
	var applyCount int64
	if err := env.raw.QueryRow(context.Background(), `SELECT apply_count FROM native_applies WHERE message_id = $1`, comment.MessageID).Scan(&applyCount); err != nil {
		t.Fatal(err)
	}
	if applyCount != 1 {
		t.Fatalf("apply count %d", applyCount)
	}
	assigned, err := c.AssignNativeTask(ctx, "pipe", task.ID, "lead")
	if err != nil || !assigned.WakePending {
		t.Fatalf("assign %+v %v", assigned, err)
	}
	if _, err := c.AssignNativeTask(ctx, "pipe", task.ID, "specialist"); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetNativeTask(ctx, "pipe", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignee != "specialist" || got.ActivationID != "" || got.ExecutionID != "" {
		t.Fatalf("after assign %+v", got)
	}
	wake, err := c.GetNativeWake(ctx, "pipe", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wake.Pending || wake.Assignee != "specialist" || wake.CoalesceCount != 3 {
		t.Fatalf("wake %+v", wake)
	}
	var wakeRows int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM native_wake_intents WHERE task_id = $1`, task.ID).Scan(&wakeRows); err != nil {
		t.Fatal(err)
	}
	if wakeRows != 1 {
		t.Fatalf("wake rows %d", wakeRows)
	}
	claim, err := c.ClaimNativeInbox(ctx, "pipe", task.ID, "lead")
	if err != nil || !claim.Accepted || claim.Fence != 1 || claim.Holder != "lead" {
		t.Fatalf("claim %+v %v", claim, err)
	}
	if _, err := c.ClaimNativeInbox(ctx, "pipe", task.ID, "other"); err == nil || !strings.Contains(err.Error(), "claim_fenced") {
		t.Fatalf("second claim %v", err)
	}
	var holder string
	var fence int64
	if err := env.raw.QueryRow(context.Background(), `SELECT holder, fence FROM native_inbox_claims WHERE task_id = $1`, task.ID).Scan(&holder, &fence); err != nil {
		t.Fatal(err)
	}
	if holder != "lead" || fence != 1 {
		t.Fatalf("holder %s fence %d", holder, fence)
	}
	if _, err := env.raw.Exec(context.Background(), `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ('fac_otherhttp', 'other', 'v1alpha1', 'test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetNativeTask(ctx, "other", task.ID); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("cross factory %v", err)
	}
	empty, err := c.ReadNativeInbox(ctx, "other", "specialist", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Deliveries) != 0 {
		t.Fatalf("foreign inbox %+v", empty)
	}
	req, err := http.NewRequest(http.MethodGet, env.base+"/api/v1/factory/pipe/native-tasks/"+task.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("auth status %d", resp.StatusCode)
	}
	over, err := c.CommentNativeTask(ctx, "pipe", task.ID, client.NativeCommentRequest{Body: strings.Repeat("a", store.NativeBodyCap+1)})
	if err == nil || !strings.Contains(err.Error(), "input_invalid") {
		t.Fatalf("oversize %+v %v", over, err)
	}
	rawReq, err := http.NewRequest(http.MethodPost, env.base+"/api/v1/factory/pipe/native-tasks/"+task.ID+"/comments", bytes.NewReader([]byte(`{"body":"x","principal":"other"}`)))
	if err != nil {
		t.Fatal(err)
	}
	rawReq.Header.Set("Authorization", "Bearer "+env.apiKey)
	rawReq.Header.Set("Content-Type", "application/json")
	rawResp, err := http.DefaultClient.Do(rawReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rawResp.Body.Close() }()
	rawBody, _ := io.ReadAll(rawResp.Body)
	if rawResp.StatusCode != http.StatusBadRequest || !strings.Contains(string(rawBody), "input_invalid") {
		t.Fatalf("principal body %d %s", rawResp.StatusCode, rawBody)
	}
	var runsAfter int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs`).Scan(&runsAfter); err != nil {
		t.Fatal(err)
	}
	if runsAfter != runsBefore {
		t.Fatalf("native routes started runs %d -> %d", runsBefore, runsAfter)
	}
}

func TestRedeliverCrossTaskLeavesDeliveries(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	inv := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": "native:redeliver:cross",
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusCreated)
	invocationID, _ := inv["invocationId"].(string)
	c := &client.Client{BaseURL: env.base, APIKey: env.apiKey}
	ctx := context.Background()
	taskA, err := c.CreateNativeTask(ctx, "pipe", client.NativeTaskRequest{InvocationID: invocationID, Assignee: "spec"})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := c.CreateNativeTask(ctx, "pipe", client.NativeTaskRequest{InvocationID: invocationID, Assignee: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := c.CommentNativeTask(ctx, "pipe", taskA.ID, client.NativeCommentRequest{Body: "for spec"})
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM native_deliveries`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RedeliverNativeMessage(ctx, "pipe", taskB.ID, comment.MessageID); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("cross task %v", err)
	}
	var after int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM native_deliveries`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("native_deliveries %d -> %d", before, after)
	}
	if _, err := c.RedeliverNativeMessage(ctx, "pipe", taskA.ID, comment.MessageID); err != nil {
		t.Fatal(err)
	}
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM native_deliveries`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("matching redeliver count %d", after)
	}
}
