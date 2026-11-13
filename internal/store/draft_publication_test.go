package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/polds/factory/internal/store"
)

func TestDraftPublicationRows(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, first, _ := seedNativeInvocation(t, h, "draft-a")
	second := insertNativeInvocation(t, h, mustFactory(t, h), "draft-b")
	sha := strings.Repeat("a", 40)
	row := store.DraftPublication{
		InvocationID: first,
		FactoryID:    factoryID,
		TargetRepo:   "northstar-ai-labs/dreaming",
		IssueKey:     "dreaming#454",
		HeadSHA:      sha,
		GitRef:       "factory/dreaming-454-inv_abcd",
	}
	got, err := h.st.InsertDraftPending(ctx, row)
	if err != nil || got.State != store.DraftPending || got.PRNumber != 0 || got.Draft {
		t.Fatalf("insert %+v %v", got, err)
	}
	again, err := h.st.InsertDraftPending(ctx, row)
	if err != nil || again.State != store.DraftPending || again.InvocationID != first {
		t.Fatalf("replay %+v %v", again, err)
	}
	var columns []string
	rows, err := h.raw.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'draft_publications'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
		switch name {
		case "token", "secret", "bytes", "bundle", "body", "private_key":
			t.Fatalf("column %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !containsAll(columns, "invocation_id", "target_repo", "issue_key", "state", "head_sha", "git_ref", "pr_number", "pr_url", "draft") {
		t.Fatalf("columns %v", columns)
	}
	if _, err := h.st.InsertDraftPending(ctx, store.DraftPublication{
		InvocationID: first, FactoryID: factoryID, TargetRepo: row.TargetRepo, IssueKey: row.IssueKey,
		HeadSHA: sha, GitRef: row.GitRef, Detail: "ghs_supersecretvalue",
	}); err == nil {
		t.Fatal("detail accepted a token")
	}
	pushed, err := h.st.AdvanceDraft(ctx, first, store.DraftPending, store.DraftPushed, store.DraftAdvance{GitRef: row.GitRef})
	if err != nil || pushed.State != store.DraftPushed {
		t.Fatalf("push %+v %v", pushed, err)
	}
	if _, err := h.st.AdvanceDraft(ctx, first, store.DraftPending, store.DraftFailed, store.DraftAdvance{Detail: "late"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	opened, err := h.st.AdvanceDraft(ctx, first, store.DraftPushed, store.DraftOpened, store.DraftAdvance{
		GitRef: row.GitRef, PRNumber: 4, PRURL: "https://github.com/northstar-ai-labs/dreaming/pull/4", Draft: true,
	})
	if err != nil || !opened.Draft || opened.PRNumber != 4 || opened.State != store.DraftOpened {
		t.Fatalf("open %+v %v", opened, err)
	}
	if _, err := h.st.AdvanceDraft(ctx, first, store.DraftOpened, store.DraftPushed, store.DraftAdvance{}); err == nil {
		t.Fatal("opened advanced")
	}
	var won, blocked int
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, id := range []string{second, insertNativeInvocation(t, h, mustFactory(t, h), "draft-c")} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			_, err := h.st.InsertDraftPending(ctx, store.DraftPublication{
				InvocationID: id, FactoryID: factoryID, TargetRepo: "other/repo", IssueKey: "dreaming#454",
				HeadSHA: sha, GitRef: "factory/dreaming-454-" + id[:8],
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
				return
			}
			if errors.Is(err, store.ErrConflict) {
				blocked++
			}
		}(id)
	}
	close(start)
	wg.Wait()
	if won != 1 || blocked != 1 {
		t.Fatalf("won %d blocked %d", won, blocked)
	}
}

func mustFactory(t *testing.T, h *harness) string {
	t.Helper()
	var id string
	if err := h.raw.QueryRow(context.Background(), `SELECT id FROM factories LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func containsAll(got []string, want ...string) bool {
	have := map[string]bool{}
	for _, name := range got {
		have[name] = true
	}
	for _, name := range want {
		if !have[name] {
			return false
		}
	}
	return true
}
