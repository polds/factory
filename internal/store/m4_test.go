package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/store"
)

func TestDeliveryInsertDedupListAndPurge(t *testing.T) {
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
	received := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := received.Add(7 * 24 * time.Hour)
	body := bytes.Repeat([]byte("a"), store.DeliveryBodyCap+10)
	in := store.Delivery{
		FactoryID:  factoryID,
		Provider:   store.ProviderGitHub,
		DeliveryID: "gh-1",
		ReceivedAt: received,
		ExpiresAt:  expires,
		Headers: map[string]string{
			"X-GitHub-Event":      "issues",
			"Authorization":       "Bearer secret-value",
			"Cookie":              "session=secret",
			"X-Hub-Signature-256": "sha256=abcd",
		},
		Body: body,
	}
	got, dup, err := h.st.InsertDelivery(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if dup || got.Status != store.DeliveryReceived || !got.Truncated || len(got.Body) != store.DeliveryBodyCap {
		t.Fatalf("dup %v status %s truncated %v len %d", dup, got.Status, got.Truncated, len(got.Body))
	}
	if _, ok := got.Headers["Authorization"]; ok {
		t.Fatal("stored Authorization")
	}
	if _, ok := got.Headers["Cookie"]; ok {
		t.Fatal("stored Cookie")
	}
	if _, ok := got.Headers["X-Hub-Signature-256"]; ok {
		t.Fatal("stored signature")
	}
	if got.Headers["X-GitHub-Event"] != "issues" {
		t.Fatalf("headers %#v", got.Headers)
	}
	if !got.ReceivedAt.Equal(received) || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("times %s %s", got.ReceivedAt, got.ExpiresAt)
	}

	again, dup, err := h.st.InsertDelivery(ctx, in)
	if err != nil || !dup || again.ID != got.ID {
		t.Fatalf("dup %v id %s err %v", dup, again.ID, err)
	}
	otherHook := in
	otherHook.WebhookUID = "whk_other"
	otherHook.DeliveryID = "gh-1"
	if _, dup, err := h.st.InsertDelivery(ctx, otherHook); err != nil || dup {
		t.Fatalf("other webhook dup %v err %v", dup, err)
	}

	labeled, err := h.st.SetMatchedAutomations(ctx, got.ID, []string{"labeled-intake"})
	if err != nil {
		t.Fatal(err)
	}
	if len(labeled.MatchedAutomations) != 1 || labeled.MatchedAutomations[0] != "labeled-intake" {
		t.Fatalf("labeled %+v", labeled.MatchedAutomations)
	}
	kept, err := h.st.SetMatchedAutomations(ctx, got.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.MatchedAutomations) != 1 || kept.MatchedAutomations[0] != "labeled-intake" {
		t.Fatalf("nil list cleared automations: %+v", kept.MatchedAutomations)
	}
	if err := h.st.MarkDelivery(ctx, got.ID, store.DeliveryMatched); err != nil {
		t.Fatal(err)
	}
	marked, err := h.st.GetDelivery(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.Status != store.DeliveryMatched || len(marked.MatchedAutomations) != 1 || marked.MatchedAutomations[0] != "labeled-intake" {
		t.Fatalf("marked %+v status %s", marked.MatchedAutomations, marked.Status)
	}
	if !marked.ExpiresAt.Equal(expires) {
		t.Fatalf("matched expiry changed %s", marked.ExpiresAt)
	}
	listed, err := h.st.ListDeliveries(ctx, factoryID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d", len(listed))
	}
	var second store.Delivery
	for _, row := range listed {
		if row.ID != got.ID {
			second = row
		}
	}
	if err := h.st.MarkDelivery(ctx, second.ID, store.DeliveryUnmatched); err != nil {
		t.Fatal(err)
	}
	unmatched, err := h.st.GetDelivery(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !unmatched.ExpiresAt.Equal(received.Add(72 * time.Hour)) {
		t.Fatalf("unmatched expiry %s", unmatched.ExpiresAt)
	}
	only, err := h.st.ListDeliveries(ctx, factoryID, store.DeliveryUnmatched, 10)
	if err != nil || len(only) != 1 || only[0].ID != second.ID {
		t.Fatalf("unmatched list %+v err %v", only, err)
	}
	if _, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID:  beta.ID,
		Provider:   store.ProviderSlack,
		DeliveryID: "sl-1",
		ReceivedAt: received,
		ExpiresAt:  received.Add(time.Hour),
		Body:       []byte("beta"),
	}); err != nil {
		t.Fatal(err)
	}
	if rows, err := h.st.ListDeliveries(ctx, factoryID, "", 10); err != nil || len(rows) != 2 {
		t.Fatalf("factory filter %d %v", len(rows), err)
	}
	n, err := h.st.PurgeExpiredDeliveries(ctx, received.Add(2*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("purge %d %v", n, err)
	}
	if err := h.st.MarkDelivery(ctx, "dlv_missing", store.DeliveryFailed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing mark %v", err)
	}
	if err := h.st.MarkDelivery(ctx, got.ID, "nope"); err == nil {
		t.Fatal("accepted bad status")
	}
	cleared, err := h.st.SetMatchedAutomations(ctx, got.ID, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.MatchedAutomations) != 0 {
		t.Fatalf("empty slice did not clear %+v", cleared.MatchedAutomations)
	}
	clocked, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID:  factoryID,
		Provider:   store.ProviderLinear,
		DeliveryID: "clock-once",
		Body:       []byte("t"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !clocked.ExpiresAt.Equal(clocked.ReceivedAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("one clock expiry %s received %s", clocked.ExpiresAt, clocked.ReceivedAt)
	}
}

func TestDeliveryConcurrentInsertOneRow(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	const n = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	dups := make([]bool, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			_, dup, err := h.st.InsertDelivery(ctx, store.Delivery{
				FactoryID:  factoryID,
				Provider:   store.ProviderGitHub,
				DeliveryID: "same-delivery",
				ReceivedAt: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
				ExpiresAt:  time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
				Body:       []byte{byte(i)},
			})
			dups[i] = dup
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()
	inserted := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("insert %d: %v", i, errs[i])
		}
		if !dups[i] {
			inserted++
		}
	}
	if inserted != 1 {
		t.Fatalf("inserts that won %d", inserted)
	}
	var rows int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM deliveries`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows %d", rows)
	}
}

func TestDeliveryClaimHoldAndRelease(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, 3)
	for i, name := range []string{"a", "b", "c"} {
		row, _, err := h.st.InsertDelivery(ctx, store.Delivery{
			FactoryID:  factoryID,
			Provider:   store.ProviderWebhook,
			DeliveryID: name,
			ReceivedAt: base.Add(time.Duration(i) * time.Second),
			ExpiresAt:  base.Add(7 * 24 * time.Hour),
			Body:       []byte(name),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = row.ID
	}
	if claimed, err := h.st.ClaimReceived(ctx, 0); err != nil || len(claimed) != 0 {
		t.Fatalf("limit 0 claimed %d err %v", len(claimed), err)
	}
	first, err := h.st.ClaimReceived(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != ids[0] || first[1].ID != ids[1] {
		t.Fatalf("first claim %#v", idsOf(first))
	}
	var status string
	if err := h.raw.QueryRow(ctx, `SELECT status FROM deliveries WHERE id = $1`, ids[0]).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != store.DeliveryReceived {
		t.Fatalf("status during claim %s", status)
	}
	labeled, err := h.st.SetMatchedAutomations(ctx, ids[0], []string{"labeled-intake"})
	if err != nil {
		t.Fatal(err)
	}
	if len(labeled.MatchedAutomations) != 1 || labeled.MatchedAutomations[0] != "labeled-intake" {
		t.Fatalf("labeled %+v", labeled.MatchedAutomations)
	}
	kept, err := h.st.SetMatchedAutomations(ctx, ids[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.MatchedAutomations) != 1 || kept.MatchedAutomations[0] != "labeled-intake" {
		t.Fatalf("nil list cleared automations: %+v", kept.MatchedAutomations)
	}
	second, err := h.st.ClaimReceived(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != ids[2] {
		t.Fatalf("second claim %#v", idsOf(second))
	}
	if err := h.st.MarkDelivery(ctx, ids[0], store.DeliveryReceived); err != nil {
		t.Fatal(err)
	}
	held, err := h.st.ClaimReceived(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("claimed a locked row %#v", idsOf(held))
	}
	if err := h.st.MarkDelivery(ctx, ids[1], store.DeliveryMatched); err != nil {
		t.Fatal(err)
	}
	released, err := h.st.GetDelivery(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != store.DeliveryReceived || len(released.MatchedAutomations) != 1 || released.MatchedAutomations[0] != "labeled-intake" {
		t.Fatalf("release %+v status %s", released.MatchedAutomations, released.Status)
	}
	finished, err := h.st.GetDelivery(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != store.DeliveryMatched {
		t.Fatalf("batch status %s", finished.Status)
	}
	if err := h.st.MarkDelivery(ctx, ids[2], store.DeliveryReceived); err != nil {
		t.Fatal(err)
	}
	again, err := h.st.ClaimReceived(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 || again[0].ID != ids[0] || again[1].ID != ids[2] {
		t.Fatalf("reclaim %#v", idsOf(again))
	}
	for _, row := range again {
		if err := h.st.MarkDelivery(ctx, row.ID, store.DeliveryMatched); err != nil {
			t.Fatal(err)
		}
	}
	none, err := h.st.ClaimReceived(ctx, 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("matched rows reclaimed %#v err %v", idsOf(none), err)
	}
}

func TestDeliveryClaimRace(t *testing.T) {
	h := newHarness(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	factoryID, _ := h.seed(t)
	const n = 16
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		if _, _, err := h.st.InsertDelivery(ctx, store.Delivery{
			FactoryID:  factoryID,
			Provider:   store.ProviderGitHub,
			DeliveryID: fmt.Sprintf("race-%d", i),
			ReceivedAt: base.Add(time.Duration(i) * time.Millisecond),
			ExpiresAt:  base.Add(7 * 24 * time.Hour),
			Body:       []byte("x"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	const workers = 4
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}
	var errs []error
	start := make(chan struct{})
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			for {
				claimed, err := h.st.ClaimReceived(ctx, 5)
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				if len(claimed) == 0 {
					return
				}
				for _, row := range claimed {
					mu.Lock()
					counts[row.ID]++
					mu.Unlock()
					if err := h.st.MarkDelivery(ctx, row.ID, store.DeliveryMatched); err != nil {
						mu.Lock()
						errs = append(errs, err)
						mu.Unlock()
						return
					}
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		t.Error(err)
	}
	if len(counts) != n {
		t.Fatalf("claimed ids %d", len(counts))
	}
	for id, count := range counts {
		if count != 1 {
			t.Errorf("%s claimed %d times", id, count)
		}
	}
}

func idsOf(rows []store.Delivery) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].ID
	}
	return out
}

func TestScheduleUpsertReplaceDueAndFire(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	start := time.Date(2026, 5, 1, 6, 0, 0, 0, time.UTC)
	weekly, err := h.st.UpsertSchedule(ctx, store.Schedule{
		FactoryID:  factoryID,
		Automation: "audit",
		Name:       "weekly",
		Cron:       "0 6 * * 1",
		NextFireAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	fired := start.Add(time.Minute)
	next := start.Add(7 * 24 * time.Hour)
	if err := h.st.MarkFired(ctx, weekly.ID, fired, next); err != nil {
		t.Fatal(err)
	}
	updated, err := h.st.UpsertSchedule(ctx, store.Schedule{
		FactoryID:  factoryID,
		Automation: "audit",
		Name:       "weekly",
		Cron:       "0 7 * * 1",
		NextFireAt: next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != weekly.ID || updated.Cron != "0 7 * * 1" || updated.LastFiredAt == nil || !updated.LastFiredAt.Equal(fired) {
		t.Fatalf("upsert %+v", updated)
	}
	daily, err := h.st.ReplaceSchedule(ctx, "weekly", store.Schedule{
		FactoryID:  factoryID,
		Automation: "audit",
		Name:       "daily",
		Cron:       "0 6 * * *",
		NextFireAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetSchedule(ctx, weekly.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old schedule err %v", err)
	}
	if daily.LastFiredAt != nil {
		t.Fatal("replaced schedule kept last fire")
	}
	paused, err := h.st.UpsertSchedule(ctx, store.Schedule{
		FactoryID:  factoryID,
		Automation: "audit",
		Name:       "paused",
		Cron:       "@hourly",
		NextFireAt: start,
		Paused:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	due, err := h.st.DueSchedules(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != daily.ID {
		t.Fatalf("due %+v", due)
	}
	if err := h.st.SetSchedulePaused(ctx, paused.ID, false); err != nil {
		t.Fatal(err)
	}
	due, err = h.st.DueSchedules(ctx, start.Add(-time.Second))
	if err != nil || len(due) != 0 {
		t.Fatalf("early due %+v %v", due, err)
	}
	if err := h.st.MarkFired(ctx, "sch_missing", fired, next); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing fire %v", err)
	}
}

func TestScheduleDelete(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	start := time.Date(2026, 5, 1, 6, 0, 0, 0, time.UTC)
	weekly, err := h.st.UpsertSchedule(ctx, store.Schedule{
		FactoryID:  factoryID,
		Automation: "audit",
		Name:       "weekly",
		Cron:       "0 6 * * 1",
		NextFireAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	unnamed, err := h.st.UpsertSchedule(ctx, store.Schedule{
		FactoryID:  factoryID,
		Automation: "audit",
		Name:       "",
		Cron:       "@daily",
		NextFireAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.st.UpsertSchedule(ctx, store.Schedule{
		FactoryID:  factoryID,
		Automation: "report",
		Name:       "weekly",
		Cron:       "@hourly",
		NextFireAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.st.DeleteSchedule(ctx, factoryID, "audit", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing name %v", err)
	}
	if err := h.st.DeleteScheduleByID(ctx, "sch_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing id %v", err)
	}
	if _, err := h.st.GetSchedule(ctx, weekly.ID); err != nil {
		t.Fatalf("missing delete removed weekly: %v", err)
	}

	if err := h.st.DeleteScheduleByID(ctx, weekly.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetSchedule(ctx, weekly.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted id err %v", err)
	}
	if err := h.st.DeleteScheduleByID(ctx, weekly.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second id delete %v", err)
	}
	if _, err := h.st.GetSchedule(ctx, other.ID); err != nil {
		t.Fatalf("id delete removed other schedule: %v", err)
	}

	if err := h.st.DeleteSchedule(ctx, "fac_other", "audit", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong factory %v", err)
	}
	if err := h.st.DeleteSchedule(ctx, factoryID, "audit", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetSchedule(ctx, unnamed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted unnamed err %v", err)
	}
	if err := h.st.DeleteSchedule(ctx, factoryID, "audit", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second name delete %v", err)
	}
	if _, err := h.st.GetSchedule(ctx, other.ID); err != nil {
		t.Fatalf("name delete removed other automation: %v", err)
	}

	if err := h.st.DeleteSchedule(ctx, factoryID, "report", "weekly"); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListSchedules(ctx, factoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("schedules left %+v", rows)
	}
}

func TestLeaseAcquireOnEmptyTable(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	var rows int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM leader_leases`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("leader_leases rows %d", rows)
	}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ttl := 15 * time.Second
	ok, err := h.st.AcquireLease(ctx, "cron", "holder-a", ttl, now)
	if err != nil || !ok {
		t.Fatalf("first grant %v %v", ok, err)
	}
	var holder string
	var expires time.Time
	if err := h.raw.QueryRow(ctx, `
		SELECT holder, expires_at FROM leader_leases WHERE name = 'cron'`,
	).Scan(&holder, &expires); err != nil {
		t.Fatal(err)
	}
	if holder != "holder-a" || !expires.Equal(now.Add(ttl)) {
		t.Fatalf("stored holder %s expires %s", holder, expires)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM leader_leases`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("leader_leases rows %d", rows)
	}
}

func TestLeaseAcquireRenewAndSteal(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ttl := 15 * time.Second
	ok, err := h.st.AcquireLease(ctx, "cron", "a", ttl, now)
	if err != nil || !ok {
		t.Fatalf("first %v %v", ok, err)
	}
	ok, err = h.st.AcquireLease(ctx, "cron", "b", ttl, now.Add(time.Second))
	if err != nil || ok {
		t.Fatalf("contender %v %v", ok, err)
	}
	ok, err = h.st.AcquireLease(ctx, "cron", "a", ttl, now.Add(5*time.Second))
	if err != nil || !ok {
		t.Fatalf("renew %v %v", ok, err)
	}
	ok, err = h.st.AcquireLease(ctx, "cron", "b", ttl, now.Add(5*time.Second+ttl+time.Millisecond))
	if err != nil || !ok {
		t.Fatalf("steal %v %v", ok, err)
	}
	ok, err = h.st.AcquireLease(ctx, "cron", "a", ttl, now.Add(5*time.Second+ttl+time.Millisecond))
	if err != nil || ok {
		t.Fatalf("loser after steal %v %v", ok, err)
	}
	if _, err := h.st.AcquireLease(ctx, "cron", "a", 0, now); err == nil {
		t.Fatal("accepted zero ttl")
	}

	const n = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	won := make([]bool, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := h.st.AcquireLease(ctx, "race", string(rune('a'+i)), ttl, now)
			if err != nil {
				t.Errorf("race %d: %v", i, err)
				return
			}
			won[i] = ok
		}(i)
	}
	close(start)
	wg.Wait()
	winners := 0
	for _, ok := range won {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("lease winners %d", winners)
	}
}

func TestExternalRefBindIsRaceSafe(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	first := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "first"})
	second := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "second"})
	got, created, err := h.st.BindExternalRef(ctx, store.ProviderGitHub, "acme/repo#1", first.ID, first.ConversationID)
	if err != nil || !created || got.WorkItemID != first.ID {
		t.Fatalf("created %v %+v %v", created, got, err)
	}
	again, created, err := h.st.BindExternalRef(ctx, store.ProviderGitHub, "acme/repo#1", second.ID, second.ConversationID)
	if err != nil || created || again.WorkItemID != first.ID || again.ConversationID != first.ConversationID {
		t.Fatalf("again created %v %+v %v", created, again, err)
	}

	const n = 16
	items := make([]store.WorkItem, n)
	for i := range items {
		items[i] = createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "race"})
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	gotID := make([]string, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			ref, _, err := h.st.BindExternalRef(ctx, store.ProviderSlack, "slack:T/C/1", items[i].ID, items[i].ConversationID)
			if err != nil {
				t.Errorf("bind %d: %v", i, err)
				return
			}
			gotID[i] = ref.WorkItemID
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range gotID {
		if gotID[i] == "" || gotID[i] != gotID[0] {
			t.Fatalf("refs %#v", gotID)
		}
	}
	var rows int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM external_refs WHERE external_ref = 'slack:T/C/1'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows %d", rows)
	}
	thread, err := h.st.UpsertOutboundThread(ctx, store.OutboundThread{
		WorkItemID: first.ID,
		Provider:   store.ProviderSlack,
		Channel:    "C123",
		RootRef:    "111.000",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread.StatusMsgRef = "222.000"
	thread, err = h.st.UpsertOutboundThread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := h.st.GetOutboundThread(ctx, first.ID, store.ProviderSlack)
	if err != nil || loaded.StatusMsgRef != "222.000" || loaded.RootRef != "111.000" {
		t.Fatalf("thread %+v %v", loaded, err)
	}
}

func TestWebhookRegistrationImmutable(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	sum := sha256.Sum256([]byte("url-token-value"))
	reg, err := h.st.UpsertWebhookRegistration(ctx, store.WebhookRegistration{
		UID:              "whk_ci",
		FactoryID:        factoryID,
		Name:             "ci",
		AuthMode:         store.WebhookAuthSignature,
		SignatureScheme:  "github",
		SecretRef:        "CI_WEBHOOK_SECRET",
		URLTokenHash:     sum[:],
		DeliveryIDHeader: "X-CI-Run-Id",
		Enabled:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	reg.Enabled = false
	reg.SecretRef = "CI_WEBHOOK_SECRET_ROTATED"
	saved, err := h.st.UpsertWebhookRegistration(ctx, reg)
	if err != nil || saved.Enabled || saved.SecretRef != "CI_WEBHOOK_SECRET_ROTATED" {
		t.Fatalf("rotate %+v %v", saved, err)
	}
	reg.AuthMode = store.WebhookAuthToken
	if _, err := h.st.UpsertWebhookRegistration(ctx, reg); !errors.Is(err, store.ErrImmutable) {
		t.Fatalf("auth change %v", err)
	}
	reg.AuthMode = store.WebhookAuthSignature
	reg.SignatureScheme = "stripe"
	if _, err := h.st.UpsertWebhookRegistration(ctx, reg); !errors.Is(err, store.ErrImmutable) {
		t.Fatalf("scheme change %v", err)
	}
	still, err := h.st.GetWebhookRegistration(ctx, "whk_ci")
	if err != nil || still.AuthMode != store.WebhookAuthSignature || still.SignatureScheme != "github" {
		t.Fatalf("row changed %+v %v", still, err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE webhook_registrations SET auth_mode = 'token' WHERE uid = 'whk_ci'`); err == nil {
		t.Fatal("trigger allowed auth change")
	}
	byHash, err := h.st.GetWebhookRegistrationByTokenHash(ctx, sum[:])
	if err != nil || byHash.UID != "whk_ci" {
		t.Fatalf("hash %+v %v", byHash, err)
	}
	otherSum := sha256.Sum256([]byte("other"))
	second, err := h.st.UpsertWebhookRegistration(ctx, store.WebhookRegistration{
		FactoryID:    factoryID,
		Name:         "other",
		AuthMode:     store.WebhookAuthURLToken,
		URLTokenHash: otherSum[:],
		Enabled:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplyWebhooks(ctx, factoryID, []store.WebhookRegistration{still}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetWebhookRegistration(ctx, second.UID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("omitted webhook err %v", err)
	}
	bad := still
	bad.AuthMode = store.WebhookAuthToken
	if err := h.st.ApplyWebhooks(ctx, factoryID, []store.WebhookRegistration{bad}); !errors.Is(err, store.ErrImmutable) {
		t.Fatalf("apply auth %v", err)
	}
	if row, err := h.st.GetWebhookRegistration(ctx, "whk_ci"); err != nil || row.AuthMode != store.WebhookAuthSignature {
		t.Fatalf("apply wrote %+v %v", row, err)
	}
	if err := h.st.ApplyWebhooks(ctx, factoryID, nil); err != nil {
		t.Fatal(err)
	}
	if rows, err := h.st.ListWebhookRegistrations(ctx, factoryID); err != nil || len(rows) != 0 {
		t.Fatalf("cleared %#v %v", rows, err)
	}
	if err := h.st.UpsertInstallation(ctx, store.ProviderGitHub, "42", factoryID); err != nil {
		t.Fatal(err)
	}
	gotFactory, err := h.st.GetInstallation(ctx, store.ProviderGitHub, "42")
	if err != nil || gotFactory != factoryID {
		t.Fatalf("installation %s %v", gotFactory, err)
	}
}

func TestInputRequestAnswerIsIdempotent(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "ask"})
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityInteractive)
	if _, err := h.st.CreateInputRequest(ctx, item.ID, run.ID, "exec_1", ""); err == nil {
		t.Fatal("accepted empty question")
	}
	req, err := h.st.CreateInputRequest(ctx, item.ID, run.ID, "exec_1", "which name?")
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.st.CreateInputRequest(ctx, item.ID, run.ID, "exec_1", "which name?")
	if err != nil || again.ID != req.ID {
		t.Fatalf("repeat %+v %v", again, err)
	}
	if _, err := h.st.CreateInputRequest(ctx, item.ID, run.ID, "exec_1", "other?"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second question %v", err)
	}
	pending, err := h.st.PendingInputRequest(ctx, item.ID)
	if err != nil || pending.Question != "which name?" || pending.AnsweredAt != nil {
		t.Fatalf("pending %+v %v", pending, err)
	}
	loaded, err := h.st.GetWorkItem(ctx, item.ID)
	if err != nil || loaded.Status != store.WorkItemAwaitingInput || loaded.SuspendedRunID != run.ID || loaded.SuspendedExecutionID != "exec_1" || loaded.SuspendedAt == nil {
		t.Fatalf("suspended %+v %v", loaded, err)
	}
	ids, err := h.st.ListSuspendedRunIDs(ctx)
	if err != nil || len(ids) != 1 || ids[0] != run.ID {
		t.Fatalf("suspended ids %#v %v", ids, err)
	}
	answered, err := h.st.AnswerInputRequest(ctx, item.ID, "the empty one", "peter")
	if err != nil || answered.Answer != "the empty one" || answered.AnsweredAt == nil || answered.AnsweredBy != "peter" {
		t.Fatalf("answer %+v %v", answered, err)
	}
	same, err := h.st.AnswerInputRequest(ctx, item.ID, "the empty one", "someone")
	if err != nil || same.ID != answered.ID || !same.AnsweredAt.Equal(*answered.AnsweredAt) {
		t.Fatalf("replay %+v %v", same, err)
	}
	if _, err := h.st.AnswerInputRequest(ctx, item.ID, "a different answer", "peter"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflict %v", err)
	}
	if _, err := h.st.PendingInputRequest(ctx, item.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pending after answer %v", err)
	}
	loaded, err = h.st.GetWorkItem(ctx, item.ID)
	if err != nil || loaded.Status != store.WorkItemOpen || loaded.SuspendedRunID != "" || loaded.SuspendedAt != nil {
		t.Fatalf("resumed %+v %v", loaded, err)
	}
	if ids, err = h.st.ListSuspendedRunIDs(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("still suspended %#v %v", ids, err)
	}
	if _, err := h.st.CreateInputRequest(ctx, item.ID, run.ID, "exec_2", "again?"); err != nil {
		t.Fatal(err)
	}
}
