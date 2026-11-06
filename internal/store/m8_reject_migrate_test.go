package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/polds/factory/internal/store"
)

func TestRequestRejectedMigration(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	applyThrough0005(t, h)
	factoryID, agentID := h.seed(t)
	exitCode := func(n int) *int { return &n }

	noExit := insertPreM5Run(t, h, factoryID, agentID, preM5Run{
		status: "failed", reason: "request_rejected",
	})
	harnessOnly := insertPreM5Run(t, h, factoryID, agentID, preM5Run{
		status: "failed", execState: "failed",
		event: "terminal", payload: `{"status":"request_rejected"}`,
	})
	exit10 := insertPreM5Run(t, h, factoryID, agentID, preM5Run{
		status: "failed", reason: "container_exit", exit: exitCode(10), execState: "failed",
		event: "terminal", payload: `{"status":"request_rejected"}`,
	})
	model := insertPreM5Run(t, h, factoryID, agentID, preM5Run{
		status: "failed", reason: "model_unavailable", execState: "failed",
	})

	body, err := os.ReadFile("migrations/0006_m5.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ('0006_m5.sql')`); err != nil {
		t.Fatal(err)
	}
	assertRawClass(t, h, noExit, "infrastructure", "unknown")
	assertRawClass(t, h, harnessOnly, "infrastructure", "unknown")
	assertRawClass(t, h, exit10, "agent", "agent_reported")
	assertRawClass(t, h, model, "infrastructure", "model_unavailable")

	stored := insertPreM5Run(t, h, factoryID, agentID, preM5Run{
		status: "failed", reason: "request_rejected",
	})
	if _, err := h.raw.Exec(ctx, `
		UPDATE runs
		SET failure_class = 'infrastructure', failure_code = 'unknown'
		WHERE id = $1`, stored); err != nil {
		t.Fatal(err)
	}
	unset := insertPreM5Run(t, h, factoryID, agentID, preM5Run{
		status: "failed", reason: "request_rejected",
	})

	if err := h.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertRawClass(t, h, noExit, "configuration", "config_invalid")
	assertRawClass(t, h, harnessOnly, "configuration", "config_invalid")
	assertRawClass(t, h, stored, "configuration", "config_invalid")
	assertRawClass(t, h, unset, "configuration", "config_invalid")
	assertRawClass(t, h, exit10, "agent", "agent_reported")
	assertRawClass(t, h, model, "infrastructure", "model_unavailable")

	fresh := newHarness(t, true)
	var class, code string
	err = fresh.raw.QueryRow(ctx, `
		SELECT
		    split_part(coalesce(classified.class_code, ''), '/', 1),
		    split_part(coalesce(classified.class_code, ''), '/', 2)
		FROM (
		    SELECT `+store.FailureBackfillCASE+` AS class_code
		    FROM (
		        SELECT
		            'failed'::text AS run_status,
		            'request_rejected'::text AS failure_reason,
		            NULL::int AS exit_code,
		            ''::text AS harness_status,
		            ''::text AS exec_state,
		            NULL::jsonb AS details
		    ) AS shaped
		) AS classified`).Scan(&class, &code)
	if err != nil {
		t.Fatal(err)
	}
	if class != "configuration" || code != "config_invalid" {
		t.Fatalf("fresh no-exit %s/%s", class, code)
	}
	err = fresh.raw.QueryRow(ctx, `
		SELECT
		    split_part(coalesce(classified.class_code, ''), '/', 1),
		    split_part(coalesce(classified.class_code, ''), '/', 2)
		FROM (
		    SELECT `+store.FailureBackfillCASE+` AS class_code
		    FROM (
		        SELECT
		            'failed'::text AS run_status,
		            'container_exit'::text AS failure_reason,
		            10::int AS exit_code,
		            'request_rejected'::text AS harness_status,
		            'failed'::text AS exec_state,
		            NULL::jsonb AS details
		    ) AS shaped
		) AS classified`).Scan(&class, &code)
	if err != nil {
		t.Fatal(err)
	}
	if class != "agent" || code != "agent_reported" {
		t.Fatalf("fresh exit 10 %s/%s", class, code)
	}
}

func assertRawClass(t *testing.T, h *harness, id, class, code string) {
	t.Helper()
	var gotClass, gotCode string
	err := h.raw.QueryRow(context.Background(), `
		SELECT coalesce(failure_class, ''), coalesce(failure_code, '')
		FROM runs WHERE id = $1`, id).Scan(&gotClass, &gotCode)
	if err != nil {
		t.Fatal(err)
	}
	if gotClass != class || gotCode != code {
		t.Fatalf("run %s class %s/%s want %s/%s", id, gotClass, gotCode, class, code)
	}
}
