package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func TestFactoryBudgetChargeDoesNotCrossFactories(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryA, invocationA, _ := seedNativeInvocation(t, h, "budget-a")
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ('fac_budget_b', 'budget-b', 'v1alpha1', 'test')`); err != nil {
		t.Fatal(err)
	}
	factoryB := "fac_budget_b"
	invocationB := insertNativeInvocation(t, h, factoryB, "budget-b")
	if err := h.st.UpsertFactoryBudget(ctx, factoryA, "", 5000); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpsertFactoryBudget(ctx, factoryB, "", 5000); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ReserveAncestors(ctx, store.ReserveInput{
		InvocationID:   invocationB,
		IdempotencyKey: "charge-b",
		Cents:          40,
		ScopeIDs:       store.AncestorScopes(factoryB, invocationB, "lead", "worker"),
		CeilingCents:   1000,
	}); err != nil {
		t.Fatal(err)
	}
	a, err := h.st.FactoryBudget(ctx, factoryA, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.st.FactoryBudget(ctx, factoryB, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.SpentCents != 0 || a.ReservedCents != 0 {
		t.Fatalf("factory A changed spent=%d reserved=%d", a.SpentCents, a.ReservedCents)
	}
	if b.ReservedCents != 40 || b.SpentCents != 0 {
		t.Fatalf("factory B reserved=%d spent=%d", b.ReservedCents, b.SpentCents)
	}
	if _, err := h.raw.Exec(ctx, `
		DELETE FROM budget_reservation_lines
		WHERE scope_id IN (SELECT scope_id FROM budget_scopes WHERE invocation_id = $1)`, invocationB); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `DELETE FROM budget_reservations WHERE invocation_id = $1`, invocationB); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `DELETE FROM invocations WHERE id = $1`, invocationB); err != nil {
		t.Fatal(err)
	}
	left, err := h.st.FactoryBudget(ctx, factoryB, "")
	if err != nil {
		t.Fatal(err)
	}
	if left.CeilingCents != 5000 || left.ReservedCents != 40 {
		t.Fatalf("cap row removed or reset: %+v", left)
	}
	if _, err := h.raw.Exec(ctx, `SELECT id FROM invocations WHERE id = $1`, invocationA); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReturnsStoredPolicyFields(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID := seedNativeInvocation(t, h, "policy-fields")
	tree := proofTree()
	tree.Synthetic = false
	tree.Implementer = "specialist-a"
	tree.Reviewer = "lead"
	tree.RosterPath = "team/roster.yaml"
	tree.PathTemplate = "team/roles/{slug}/{file}"
	tree.SeatFiles = []string{"ROLE.md"}
	opened, _, err := h.st.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationID, AgentID: agentID,
		Tree: tree, Slots: 1, MaxLive: 4, CeilingCents: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := h.st.GetNativePolicy(ctx, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Implementer != reloaded.Implementer || opened.Reviewer != reloaded.Reviewer || opened.RosterPath != reloaded.RosterPath || opened.PathTemplate != reloaded.PathTemplate || strings.Join(opened.SeatFiles, ",") != strings.Join(reloaded.SeatFiles, ",") || opened.Synthetic != reloaded.Synthetic || opened.SyntheticStored != reloaded.SyntheticStored {
		t.Fatalf("open %+v reload %+v", opened, reloaded)
	}
	if opened.Implementer != "specialist-a" || opened.Reviewer != "lead" || opened.RosterPath != "team/roster.yaml" || !opened.SyntheticStored || opened.Synthetic {
		t.Fatalf("first open dropped declaration fields: %+v", opened)
	}
	_ = definition.BundleModeRoleFiles
}

func TestMigrationDeletesOnlyRetiredEvidenceRows(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	retiredDB := retiredPrefix() + strings.Repeat("ab", 16)
	retiredRole := retiredPrefix() + strings.Repeat("cd", 16)
	keptDB := "factory_ev_" + strings.Repeat("ef", 16)
	keptRole := "factory_ev_" + strings.Repeat("12", 16)
	mixedDB := retiredPrefix() + strings.Repeat("11", 16)
	mixedRole := "factory_ev_" + strings.Repeat("34", 16)
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO disposable_databases (run_id, database_name, role_name, state)
		VALUES
		  ('run_old', $1, $2, 'ready'),
		  ('run_new', $3, $4, 'ready'),
		  ('run_mixed', $5, $6, 'ready')`,
		retiredDB, retiredRole, keptDB, keptRole, mixedDB, mixedRole); err != nil {
		t.Fatal(err)
	}
	body, err := store.MigrationFS.ReadFile("migrations/0020_factory_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(body)
	if !strings.Contains(sqlText, retiredPrefix()) {
		t.Fatal("migration does not name the retired prefix")
	}
	if strings.Contains(sqlText, "DROP DATABASE") && !strings.Contains(sqlText, retiredPrefix()) {
		t.Fatal("migration drops a database outside the retired prefix")
	}
	stmt := sqlText[strings.LastIndex(sqlText, "DELETE FROM disposable_databases"):]
	if _, err := h.raw.Exec(ctx, stmt); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM disposable_databases WHERE run_id = 'run_old'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("retired row remains %d %v", n, err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM disposable_databases WHERE run_id IN ('run_new', 'run_mixed')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("kept rows %d %v", n, err)
	}
}

func TestDropRetiredEvidenceLeavesOtherNames(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	retired := retiredPrefix() + strings.Repeat("a", 32)
	canary := "factory_ev_canary_name_not_hex"
	if _, err := h.raw.Exec(ctx, `CREATE DATABASE `+quoteTestIdent(retired)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.raw.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoteTestIdent(retired))
	})
	if _, err := h.raw.Exec(ctx, `CREATE DATABASE `+quoteTestIdent(canary)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.raw.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoteTestIdent(canary))
	})
	if _, err := h.raw.Exec(ctx, `CREATE ROLE `+quoteTestIdent(retired)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.raw.Exec(context.Background(), `DROP ROLE IF EXISTS `+quoteTestIdent(retired))
	})
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO disposable_databases (run_id, database_name, role_name, state)
		VALUES ('run_drop', $1, $1, 'ready')`, retired); err != nil {
		t.Fatal(err)
	}
	if err := store.DropRetiredEvidenceForTest(ctx, h.raw); err != nil {
		t.Fatal(err)
	}
	var databases int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, retired).Scan(&databases); err != nil || databases != 0 {
		t.Fatalf("retired database remains %d %v", databases, err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, canary).Scan(&databases); err != nil || databases != 1 {
		t.Fatalf("canary database %d %v", databases, err)
	}
}

func retiredPrefix() string {
	return string([]byte{100, 114, 101, 97, 109, 100}) + "_"
}

func quoteTestIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
