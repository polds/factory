package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/store"
)

// TestListErrorText pins the op prefix each migrated list query wraps around
// a failed query. The strings were recorded against the pre-collect loops.
func TestListErrorText(t *testing.T) {
	h := newHarness(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Unix(1_700_000_000, 0).UTC()

	cases := []struct {
		name string
		op   string
		call func() error
	}{
		{"work items", "store: list work items", func() error {
			_, err := h.st.ListWorkItems(ctx, store.WorkItemFilter{})
			return err
		}},
		{"approvals", "store: list approvals", func() error {
			_, err := h.st.ListApprovals(ctx, "fac", false)
			return err
		}},
		{"schedules", "store: list schedules", func() error {
			_, err := h.st.ListSchedules(ctx, "fac")
			return err
		}},
		{"due schedules", "store: due schedules", func() error {
			_, err := h.st.DueSchedules(ctx, now)
			return err
		}},
		{"webhooks", "store: list webhooks", func() error {
			_, err := h.st.ListWebhookRegistrations(ctx, "fac")
			return err
		}},
		{"workers", "store: list workers", func() error {
			_, err := h.st.ListWorkers(ctx, now)
			return err
		}},
		{"pool stats", "store: pool stats", func() error {
			_, err := h.st.PoolStats(ctx, now)
			return err
		}},
		{"next steers", "store: next steers", func() error {
			_, err := h.st.NextSteers(ctx, "run", "", now)
			return err
		}},
		{"list steers", "store: list steers", func() error {
			_, err := h.st.ListSteers(ctx, "run")
			return err
		}},
		{"deliveries", "store: list deliveries", func() error {
			_, err := h.st.ListDeliveries(ctx, "fac", "", 1)
			return err
		}},
		{"webhook deliveries", "store: list webhook deliveries", func() error {
			_, err := h.st.ListWebhookDeliveries(ctx, "fac", "whk", "", 1)
			return err
		}},
		{"factories", "store: list factories", func() error {
			_, err := h.st.ListFactories(ctx)
			return err
		}},
		{"agents", "store: list agents", func() error {
			_, err := h.st.ListAgents(ctx, "fac")
			return err
		}},
		{"suspended runs", "store: list suspended runs", func() error {
			_, err := h.st.ListSuspendedRunIDs(ctx)
			return err
		}},
		{"events", "store: events from", func() error {
			_, err := h.st.EventsFrom(ctx, "run", 0, 1)
			return err
		}},
		{"runs", "store: list runs", func() error {
			_, err := h.st.ListRuns(ctx, store.RunFilter{})
			return err
		}},
		{"lease", "store: lease", func() error {
			_, err := h.st.Lease(ctx, "default", time.Second, 1)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil || !strings.HasPrefix(err.Error(), tc.op+":") {
				t.Fatalf("error %v, want prefix %q", err, tc.op+":")
			}
		})
	}
}

// TestListErrorTextAfterGuard pins list ops that sit behind an earlier query.
// A cancelled context fails that earlier query, so the list relation is broken
// after the guard has succeeded.
func TestListErrorTextAfterGuard(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	item, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.SourceManual,
		Title:     "list error",
	})
	if err != nil {
		t.Fatal(err)
	}

	rename(t, h, "executions", "started_at", "started_at_hidden")
	_, err = h.st.RunTimeline(ctx, run.ID)
	assertListOp(t, err, "store: timeline", "started_at")

	rename(t, h, "run_events", "idx", "idx_hidden")
	_, err = h.st.FactoryEventsFrom(ctx, factoryID, 0, 10)
	assertListOp(t, err, "store: factory events", "idx")

	rename(t, h, "approvals", "gate", "gate_hidden")
	_, err = h.st.Inbox(ctx, factoryID)
	assertListOp(t, err, "store: inbox", "gate")

	rename(t, h, "automations", "triggers_json", "triggers_json_hidden")
	_, err = h.st.AutomationStats(ctx, factoryID)
	assertListOp(t, err, "store: automation stats", "triggers_json")

	rename(t, h, "intake_slots", "automation", "automation_hidden")
	auto := &store.Automation{Store: h.st}
	_, err = auto.Owners(ctx, item.ID)
	assertListOp(t, err, "store: owners", "automation")
	_, err = auto.EnsureIntake(ctx, item.ID, "dlv_list_error", nil)
	assertListOp(t, err, "store: ensure intake", "automation")
}

func rename(t *testing.T, h *harness, table, from, to string) {
	t.Helper()
	if _, err := h.raw.Exec(context.Background(), `ALTER TABLE `+table+` RENAME COLUMN `+from+` TO `+to); err != nil {
		t.Fatal(err)
	}
}

func assertListOp(t *testing.T, err error, op, marker string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), op+":") || !strings.Contains(err.Error(), marker) {
		t.Fatalf("error %v, want prefix %q and %q", err, op+":", marker)
	}
}
