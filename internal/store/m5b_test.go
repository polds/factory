package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/polds/factory/internal/store"
)

func TestM5bMigrationIdempotent(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	if err := h.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var dataType string
	if err := h.raw.QueryRow(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'deliveries'
		  AND column_name = 'normalized_json'`).Scan(&dataType); err != nil {
		t.Fatal(err)
	}
	if dataType != "jsonb" {
		t.Fatalf("column type %s", dataType)
	}
	body, err := os.ReadFile(filepath.Join("migrations", "0008_m5_api.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestM5bMigrationFrom0006(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	for _, name := range embeddedMigrations(t) {
		body, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	body, err := os.ReadFile(filepath.Join("migrations", "0008_m5_api.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestM5bDeliveryNormalizedAndSteers(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	older := time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	first, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, WebhookUID: "wh_a",
		DeliveryID: "a", ReceivedAt: older, Body: []byte(`{"a":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, WebhookUID: "wh_a",
		DeliveryID: "b", ReceivedAt: newer, Body: []byte(`{"b":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, WebhookUID: "wh_b",
		DeliveryID: "c", ReceivedAt: newer, Body: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if first.Normalized != nil {
		t.Fatal("insert stored normalized events")
	}
	if err := h.st.SetDeliveryNormalized(ctx, second.ID, []byte(`[{"event":"webhook/received"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetDeliveryNormalized(ctx, second.ID, []byte(`[{"event":"webhook/received","n":2}]`)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetDeliveryNormalized(ctx, first.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetDeliveryNormalized(ctx, first.ID, []byte(`{"event":"nope"}`)); err == nil {
		t.Fatal("object was accepted")
	}
	rows, err := h.st.ListWebhookDeliveries(ctx, factoryID, "wh_a", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != second.ID || rows[1].ID != first.ID {
		t.Fatalf("order %+v", rows)
	}
	if string(rows[0].Normalized) == "" || string(rows[1].Normalized) != "[]" {
		t.Fatalf("normalized %s %s", rows[0].Normalized, rows[1].Normalized)
	}

	run := h.createRun(t, factoryID, agentID, "default", store.PriorityInteractive)
	early, err := h.st.EnqueueSteer(ctx, run.ID, store.SteerInterrupt, "", "api-key")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	late, err := h.st.EnqueueSteer(ctx, run.ID, store.SteerFollowup, "next", "api-key")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := h.st.NextSteers(ctx, run.ID, "", time.Now())
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claimed %+v %v", claimed, err)
	}
	listed, err := h.st.ListSteers(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != early.ID || listed[1].ID != late.ID {
		t.Fatalf("list %+v", listed)
	}
	if listed[0].DeliveredAt != nil || listed[1].DeliveredAt != nil {
		t.Fatal("list marked delivered")
	}
	again, err := h.st.NextSteers(ctx, run.ID, "", time.Now())
	if err != nil || len(again) != 0 {
		t.Fatalf("list stole the claim %+v %v", again, err)
	}
}
