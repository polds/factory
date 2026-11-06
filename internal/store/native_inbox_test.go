package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/store"
)

func TestAppendLedgerLateCommitResultBeforeCompletion(t *testing.T) {
	h := newHarness(t, true)
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "late-result")
	first := store.LedgerEntry{Kind: store.LedgerKindResult, Payload: json.RawMessage(`{"part":"result"}`)}
	second := store.LedgerEntry{Kind: store.LedgerKindCompletion, Payload: json.RawMessage(`{"part":"completion"}`)}
	gotFirst, gotSecond := holdLedgerWriters(t, h, factoryID, invocationID, first, second)
	if gotFirst.LedgerSeq >= gotSecond.LedgerSeq {
		t.Fatalf("result seq %d completion seq %d", gotFirst.LedgerSeq, gotSecond.LedgerSeq)
	}
	assertOutboxOrder(t, h, invocationID, []string{store.LedgerKindResult, store.LedgerKindCompletion})
}

func TestAppendLedgerLateCommitCommentBeforeCompletion(t *testing.T) {
	h := newHarness(t, true)
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "late-comment")
	task := insertNative(t, h, factoryID, invocationID, "lead")
	first := store.LedgerEntry{
		Kind: store.LedgerKindComment, TaskID: task.ID, Assignee: "lead", Body: "shipped", Payload: json.RawMessage(`{}`),
	}
	second := store.LedgerEntry{Kind: store.LedgerKindCompletion, Payload: json.RawMessage(`{"part":"completion"}`)}
	gotFirst, gotSecond := holdLedgerWriters(t, h, factoryID, invocationID, first, second)
	if gotFirst.LedgerSeq >= gotSecond.LedgerSeq {
		t.Fatalf("comment seq %d completion seq %d", gotFirst.LedgerSeq, gotSecond.LedgerSeq)
	}
	assertOutboxOrder(t, h, invocationID, []string{store.LedgerKindComment, store.LedgerKindCompletion})
}

func TestReadInboxCursorAndDefaultLimit(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "pages")
	task := insertNative(t, h, factoryID, invocationID, "lead")
	if limit, err := store.NormalizeInboxLimit(0); err != nil || limit != store.InboxDefaultLimit {
		t.Fatalf("default %d %v", limit, err)
	}
	if _, err := store.NormalizeInboxLimit(101); !errors.Is(err, store.ErrInputInvalid) {
		t.Fatalf("limit 101 err %v", err)
	}
	if _, err := store.NormalizeInboxLimit(0); err != nil {
		t.Fatal(err)
	}
	if limit, err := store.NormalizeInboxLimit(1); err != nil || limit != 1 {
		t.Fatalf("limit 1 %d %v", limit, err)
	}
	if limit, err := store.NormalizeInboxLimit(store.InboxMaxLimit); err != nil || limit != store.InboxMaxLimit {
		t.Fatalf("max %d %v", limit, err)
	}
	var ids []string
	for i := range 3 {
		rec, err := h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
			Kind: store.LedgerKindComment, TaskID: task.ID, Assignee: "lead",
			Body: fmt.Sprintf("m%d", i), Payload: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.DeliveryID)
	}
	var seen []string
	cursor := ""
	for range 3 {
		page, err := h.st.ReadInbox(ctx, store.InboxQuery{FactoryID: factoryID, Assignee: "lead", Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Deliveries) != 1 || page.NextCursor == "" {
			t.Fatalf("page %+v", page)
		}
		if page.NextCursor == strconv.FormatInt(page.Deliveries[0].LedgerSeq, 10) {
			t.Fatalf("cursor is the raw sequence %q", page.NextCursor)
		}
		if _, _, err := store.DecodeInboxCursor(page.NextCursor); err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.ReadInbox(ctx, store.InboxQuery{FactoryID: factoryID, Assignee: "lead", Cursor: "1", Limit: 1}); !errors.Is(err, store.ErrInputInvalid) {
			t.Fatalf("numeric cursor err %v", err)
		}
		seen = append(seen, page.Deliveries[0].DeliveryID)
		cursor = page.NextCursor
	}
	if len(seen) != 3 || seen[0] != ids[0] || seen[1] != ids[1] || seen[2] != ids[2] {
		t.Fatalf("seen %v ids %v", seen, ids)
	}
	acks, err := h.st.CountAcks(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if acks != 0 {
		t.Fatalf("read acked %d", acks)
	}
}

func TestReadInboxLongPollDoesNotAck(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "poll")
	task := insertNative(t, h, factoryID, invocationID, "lead")
	done := make(chan error, 1)
	go func() {
		time.Sleep(40 * time.Millisecond)
		_, err := h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
			Kind: store.LedgerKindComment, TaskID: task.ID, Assignee: "lead", Body: "later", Payload: json.RawMessage(`{}`),
		})
		done <- err
	}()
	page, err := h.st.ReadInbox(ctx, store.InboxQuery{FactoryID: factoryID, Assignee: "lead", Wait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(page.Deliveries) != 1 || page.Deliveries[0].Body != "later" {
		t.Fatalf("page %+v", page)
	}
	acks, err := h.st.CountAcks(ctx, page.Deliveries[0].DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if acks != 0 {
		t.Fatalf("long-poll acked %d", acks)
	}
}

func TestAckDeliveryDuplicateDoesNotReapply(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	rec := commentForAck(t, h, "dup")
	if _, err := h.st.AckDelivery(ctx, rec.DeliveryID, "p1", rec.TaskID); !errors.Is(err, store.ErrNotApplied) {
		t.Fatalf("ack before apply %v", err)
	}
	applied, err := h.st.ApplyMessage(ctx, rec.MessageID, rec.TaskID, "p1")
	if err != nil || !applied {
		t.Fatalf("apply %v %v", applied, err)
	}
	first, err := h.st.AckDelivery(ctx, rec.DeliveryID, "p1", rec.TaskID)
	if err != nil || first.Duplicate {
		t.Fatalf("first ack %+v %v", first, err)
	}
	second, err := h.st.AckDelivery(ctx, rec.DeliveryID, "p1", rec.TaskID)
	if err != nil || !second.Duplicate {
		t.Fatalf("second ack %+v %v", second, err)
	}
	count, err := h.st.ApplyCount(ctx, rec.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("apply count %d", count)
	}
	acks, err := h.st.CountAcks(ctx, rec.DeliveryID)
	if err != nil || acks != 1 {
		t.Fatalf("acks %d %v", acks, err)
	}
	if _, err := h.st.AckDelivery(ctx, rec.DeliveryID, "p2", rec.TaskID); !errors.Is(err, store.ErrPrincipalRejected) {
		t.Fatalf("other principal %v", err)
	}
	if _, err := h.st.AckDelivery(ctx, rec.DeliveryID, "p1", "ntask_OTHER00000000000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other task %v", err)
	}
}

func TestAckDeliveryConcurrentDuplicate(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	rec := commentForAck(t, h, "ack-race")
	if _, err := h.st.ApplyMessage(ctx, rec.MessageID, rec.TaskID, "p1"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var fresh, dup int
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ack, err := h.st.AckDelivery(ctx, rec.DeliveryID, "p1", rec.TaskID)
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			if ack.Duplicate {
				dup++
			} else {
				fresh++
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if fresh != 1 || dup != 1 {
		t.Fatalf("fresh %d dup %d", fresh, dup)
	}
	count, err := h.st.ApplyCount(ctx, rec.MessageID)
	if err != nil || count != 1 {
		t.Fatalf("apply count %d %v", count, err)
	}
}

func TestApplyMessageConcurrentOnce(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	rec := commentForAck(t, h, "apply-race")
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var applied int
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := h.st.ApplyMessage(ctx, rec.MessageID, rec.TaskID, "p1")
			if err != nil {
				errCh <- err
				return
			}
			if ok {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied %d", applied)
	}
	count, err := h.st.ApplyCount(ctx, rec.MessageID)
	if err != nil || count != 1 {
		t.Fatalf("count %d %v", count, err)
	}
}

func TestRedeliverUnackedAppliesOnce(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	rec := commentForAck(t, h, "drop")
	page, err := h.st.ReadInbox(ctx, store.InboxQuery{FactoryID: rec.FactoryID, Assignee: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Deliveries) != 1 {
		t.Fatalf("page %+v", page)
	}
	acks, err := h.st.CountAcks(ctx, rec.DeliveryID)
	if err != nil || acks != 0 {
		t.Fatalf("dropped read acks %d %v", acks, err)
	}
	again, err := h.st.Redeliver(ctx, rec.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if again.DeliveryID == rec.DeliveryID || again.MessageID != rec.MessageID {
		t.Fatalf("redelivery %+v original %s", again, rec.DeliveryID)
	}
	applied, err := h.st.ApplyMessage(ctx, rec.MessageID, rec.TaskID, "p1")
	if err != nil || !applied {
		t.Fatalf("apply %v %v", applied, err)
	}
	if _, err := h.st.AckDelivery(ctx, again.DeliveryID, "p1", rec.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AckDelivery(ctx, rec.DeliveryID, "p1", rec.TaskID); err != nil {
		t.Fatal(err)
	}
	count, err := h.st.ApplyCount(ctx, rec.MessageID)
	if err != nil || count != 1 {
		t.Fatalf("apply count %d %v", count, err)
	}
	page, err = h.st.ReadInbox(ctx, store.InboxQuery{FactoryID: rec.FactoryID, Assignee: "lead", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Deliveries) != 2 {
		t.Fatalf("deliveries %+v", page.Deliveries)
	}
}

func TestClaimInboxConcurrentFence(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "claim")
	task := insertNative(t, h, factoryID, invocationID, "lead")
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins int
	var winner string
	errCh := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		holder := fmt.Sprintf("holder-%d", i)
		go func() {
			defer wg.Done()
			<-start
			claim, err := h.st.ClaimInbox(ctx, task.ID, holder)
			if err != nil {
				var lost *store.ClaimLost
				if errors.As(err, &lost) {
					if lost.Fence != 1 {
						errCh <- fmt.Errorf("fence moved to %d", lost.Fence)
					}
					return
				}
				errCh <- err
				return
			}
			if !claim.Accepted || claim.Fence != 1 {
				errCh <- fmt.Errorf("win %+v", claim)
				return
			}
			mu.Lock()
			wins++
			winner = claim.Holder
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if wins != 1 || winner == "" {
		t.Fatalf("wins %d holder %q", wins, winner)
	}
	got, err := h.st.GetInboxClaim(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Holder != winner || got.Fence != 1 {
		t.Fatalf("claim %+v winner %s", got, winner)
	}
	if _, err := h.st.ClaimInbox(ctx, task.ID, "other"); !errors.Is(err, store.ErrClaimFenced) {
		t.Fatalf("second claim %v", err)
	}
	got, err = h.st.GetInboxClaim(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Holder != winner || got.Fence != 1 {
		t.Fatalf("fence changed %+v", got)
	}
}

func TestCoalesceWakeUnderLoad(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "wake")
	task := insertNative(t, h, factoryID, invocationID, "spec")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		assignee := "lead"
		if i%2 == 0 {
			assignee = "spec"
		}
		go func() {
			defer wg.Done()
			<-start
			_, err := h.st.CoalesceWake(ctx, store.WakeIntent{
				TaskID: task.ID, InvocationID: invocationID, Assignee: assignee, Reason: "assign",
			})
			if err != nil {
				errCh <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	n, err := h.st.CountWakeIntents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("wake rows %d", n)
	}
	wake, err := h.st.GetWake(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wake.Pending || wake.CoalesceCount != 20 {
		t.Fatalf("wake %+v", wake)
	}
	if wake.Assignee != "lead" && wake.Assignee != "spec" {
		t.Fatalf("assignee %s", wake.Assignee)
	}
	st2 := reopenStore(t, h)
	again, err := st2.GetWake(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.CoalesceCount != wake.CoalesceCount || again.TaskID != task.ID || !again.Pending {
		t.Fatalf("restart wake %+v", again)
	}
}

func TestAssignRoutesWakeToCurrentAssignee(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, "route")
	task := insertNative(t, h, factoryID, invocationID, "spec")
	if _, err := h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
		Kind: store.LedgerKindMention, TaskID: task.ID, Assignee: "spec", Body: "fyi", Payload: json.RawMessage(`{}`), Wake: false,
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := h.st.CountWakeIntents(ctx, task.ID); err != nil || n != 0 {
		t.Fatalf("mention wakes %d %v", n, err)
	}
	if _, err := h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
		Kind: store.LedgerKindAssign, TaskID: task.ID, Assignee: "lead", Wake: true, WakeReason: "assign",
		Payload: json.RawMessage(`{"assignee":"lead"}`), TaskUpdate: &store.NativeTaskUpdate{Assignee: "lead"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendLedger(ctx, invocationID, store.LedgerEntry{
		Kind: store.LedgerKindAssign, TaskID: task.ID, Assignee: "specialist", Wake: true, WakeReason: "assign",
		Payload: json.RawMessage(`{"assignee":"specialist"}`), TaskUpdate: &store.NativeTaskUpdate{Assignee: "specialist"},
	}); err != nil {
		t.Fatal(err)
	}
	task, err := h.st.GetNativeTask(ctx, factoryID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wake, err := h.st.GetWake(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Assignee != "specialist" || wake.Assignee != "specialist" || !wake.Pending || wake.CoalesceCount != 2 {
		t.Fatalf("task %s wake %+v", task.Assignee, wake)
	}
	if task.ActivationID != "" || task.ExecutionID != "" {
		t.Fatalf("assignment started an execution %+v", task)
	}
	n, err := h.st.CountWakeIntents(ctx, task.ID)
	if err != nil || n != 1 {
		t.Fatalf("wake rows %d %v", n, err)
	}
}

func holdLedgerWriters(t *testing.T, h *harness, factoryID, invocationID string, first, second store.LedgerEntry) (store.LedgerRecord, store.LedgerRecord) {
	t.Helper()
	ctx := context.Background()
	foreign := insertNativeInvocation(t, h, factoryID, invocationID+"-foreign")
	locked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	defer letGo()
	ctxA := store.WithLedgerHold(ctx, func() {
		close(locked)
		<-release
	})
	var firstRec store.LedgerRecord
	var firstErr error
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		firstRec, firstErr = h.st.AppendLedger(ctxA, invocationID, first)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("first writer did not lock the ledger")
	}
	foreignDone := make(chan error, 1)
	go func() {
		var err error
		for range 3 {
			_, err = h.st.AppendLedger(ctx, foreign, store.LedgerEntry{Kind: store.LedgerKindResult, Payload: json.RawMessage(`{"foreign":true}`)})
			if err != nil {
				break
			}
		}
		foreignDone <- err
	}()
	doneB := make(chan struct{})
	var secondRec store.LedgerRecord
	var secondErr error
	go func() {
		defer close(doneB)
		bctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		secondRec, secondErr = h.st.AppendLedger(bctx, invocationID, second)
	}()
	waitForLedgerWaiter(t, h)
	letGo()
	<-doneA
	<-doneB
	if err := <-foreignDone; err != nil {
		t.Fatal(err)
	}
	if firstErr != nil || secondErr != nil {
		t.Fatalf("writers %v %v", firstErr, secondErr)
	}
	rows, err := h.st.ReadOutbox(ctx, foreign, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("foreign outbox %d", len(rows))
	}
	return firstRec, secondRec
}

func waitForLedgerWaiter(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		err := h.raw.QueryRow(context.Background(), `
			SELECT count(*)
			FROM pg_locks l
			JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE NOT l.granted
			  AND a.datname = current_database()
			  AND a.query LIKE '%invocation_ledgers%'
			  AND a.query NOT LIKE '%pg_stat_activity%'`).Scan(&n)
		if err == nil && n > 0 {
			return
		}
		if time.Now().After(deadline) {
			var activity string
			_ = h.raw.QueryRow(context.Background(), `
				SELECT coalesce(string_agg(state || ' ' || coalesce(wait_event_type, '') || ' ' || left(query, 160), ' | '), '')
				FROM pg_stat_activity
				WHERE datname = current_database()`).Scan(&activity)
			t.Fatalf("completion writer did not wait on the ledger lock: %v activity %s", err, activity)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertOutboxOrder(t *testing.T, h *harness, invocationID string, kinds []string) {
	t.Helper()
	rows, err := h.st.ReadOutbox(context.Background(), invocationID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(kinds) {
		t.Fatalf("outbox %+v", rows)
	}
	var prev int64
	for i, kind := range kinds {
		if rows[i].Kind != kind || rows[i].LedgerSeq <= prev {
			t.Fatalf("row %d %+v", i, rows[i])
		}
		var events, state int
		if err := h.raw.QueryRow(context.Background(), `
			SELECT
			  (SELECT count(*) FROM invocation_events WHERE invocation_id = $1 AND ledger_seq = $2),
			  (SELECT count(*) FROM invocation_ledger_state WHERE invocation_id = $1 AND ledger_seq = $2)`,
			invocationID, rows[i].LedgerSeq).Scan(&events, &state); err != nil {
			t.Fatal(err)
		}
		if events != 1 || state != 1 {
			t.Fatalf("seq %d events %d state %d", rows[i].LedgerSeq, events, state)
		}
		prev = rows[i].LedgerSeq
	}
}

type ackFixture struct {
	FactoryID  string
	TaskID     string
	MessageID  string
	DeliveryID string
}

func commentForAck(t *testing.T, h *harness, key string) ackFixture {
	t.Helper()
	factoryID, invocationID, _ := seedNativeInvocation(t, h, key)
	task := insertNative(t, h, factoryID, invocationID, "lead")
	rec, err := h.st.AppendLedger(context.Background(), invocationID, store.LedgerEntry{
		Kind: store.LedgerKindComment, TaskID: task.ID, Assignee: "lead", Body: "hello", Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ackFixture{FactoryID: factoryID, TaskID: task.ID, MessageID: rec.MessageID, DeliveryID: rec.DeliveryID}
}

func reopenStore(t *testing.T, h *harness) *store.Store {
	t.Helper()
	var schema string
	if err := h.raw.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn, err := withSearchPath(os.Getenv("FACTORY_TEST_DATABASE_URL"), schema, true)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}
