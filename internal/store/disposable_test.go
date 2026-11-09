package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polds/factory/internal/store"
)

func TestDisposableLedgerReusesOneRow(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	name := "dreamd_" + strings.Repeat("ab", 16)
	row, created, err := h.st.InsertDisposable(ctx, "run_d4", name, name)
	if err != nil || !created || row.State != store.DisposableCreating || row.DatabaseName != name || row.RoleName != name {
		t.Fatalf("insert created %v state %s err %v", created, row.State, err)
	}
	again, created, err := h.st.InsertDisposable(ctx, "run_d4", "dreamd_"+strings.Repeat("cd", 16), "dreamd_"+strings.Repeat("cd", 16))
	if err != nil || created || again.DatabaseName != name || again.RoleName != name {
		t.Fatalf("reuse created %v name changed %v err %v", created, again.DatabaseName != name, err)
	}
	if _, _, err := h.st.InsertDisposable(ctx, "run_bad", "factory", "factory"); err == nil {
		t.Fatal("hostile name inserted")
	}
	listed, err := h.st.ListUndroppedDisposables(ctx)
	if err != nil || len(listed) != 1 || listed[0].RunID != "run_d4" {
		t.Fatalf("list %+v %v", listed, err)
	}
	if err := h.st.MarkDisposable(ctx, "run_d4", store.DisposableReady); err != nil {
		t.Fatal(err)
	}
	ready, err := h.st.GetDisposable(ctx, "run_d4")
	if err != nil || ready.State != store.DisposableReady {
		t.Fatalf("ready %+v %v", ready, err)
	}
	if err := h.st.MarkDisposable(ctx, "run_d4", store.DisposableDropped); err != nil {
		t.Fatal(err)
	}
	listed, err = h.st.ListUndroppedDisposables(ctx)
	if err != nil || len(listed) != 0 {
		t.Fatalf("dropped list %+v %v", listed, err)
	}
	exec, err := h.st.RunExecuting(ctx, "run_missing")
	if err != nil || exec {
		t.Fatalf("exec %v %v", exec, err)
	}
	if _, err := h.st.GetDisposable(ctx, "run_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
}
