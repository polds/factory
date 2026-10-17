package store_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/store"
)

func TestBeginBuildingRunOneInFlight(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source: store.SourceIssue,
		Title:  "one building run",
	})
	other := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source: store.SourceIssue,
		Title:  "other item",
	})

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	type got struct {
		run     store.Run
		created bool
		err     error
	}
	out := make([]got, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			run, created, err := h.st.BeginBuildingRun(ctx, buildingInput(factoryID, agentID, item.ID, "build"))
			out[i] = got{run, created, err}
		}(i)
	}
	close(start)
	wg.Wait()
	createdN := 0
	var id string
	for _, g := range out {
		if g.err != nil {
			t.Fatalf("begin %v", g.err)
		}
		if g.run.ID == "" || g.run.Stage != store.StageBuilding || g.run.Status != store.StatusQueued {
			t.Fatalf("run %+v", g.run)
		}
		if id == "" {
			id = g.run.ID
		}
		if g.run.ID != id {
			t.Fatalf("run ids %s %s", g.run.ID, id)
		}
		if g.created {
			createdN++
		}
	}
	if createdN != 1 {
		t.Fatalf("created %d", createdN)
	}
	if n := countRows(t, h, `
		SELECT count(*) FROM runs
		WHERE work_item_id = $1 AND stage = 'building'
		  AND status IN ('queued', 'assigned', 'running')`, item.ID); n != 1 {
		t.Fatalf("in flight %d", n)
	}

	second, created, err := h.st.BeginBuildingRun(ctx, buildingInput(factoryID, agentID, other.ID, "other"))
	if err != nil || !created || second.ID == id {
		t.Fatalf("other item %+v created %v err %v", second, created, err)
	}

	if _, err := h.raw.Exec(ctx, `
		UPDATE runs SET status = 'succeeded', ended_at = clock_timestamp() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	again, created, err := h.st.BeginBuildingRun(ctx, buildingInput(factoryID, agentID, item.ID, "revision"))
	if err != nil || !created || again.ID == id {
		t.Fatalf("after terminal %+v created %v err %v", again, created, err)
	}
}

func TestClaimOpenPullRequestKeepsFirst(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source: store.SourceIssue,
		Title:  "one pull request",
	})
	const (
		first  = "https://github.com/acme/payments/pull/84"
		second = "https://github.com/acme/payments/pull/85"
	)
	start := make(chan struct{})
	var wg sync.WaitGroup
	type got struct {
		claimed bool
		stored  string
		err     error
	}
	urls := []string{first, second}
	out := make([]got, len(urls))
	wg.Add(len(urls))
	for i, url := range urls {
		go func(i int, url string) {
			defer wg.Done()
			<-start
			claimed, stored, err := h.st.ClaimOpenPullRequest(ctx, item.ID, url)
			out[i] = got{claimed, stored, err}
		}(i, url)
	}
	close(start)
	wg.Wait()
	claimedN := 0
	var stored string
	for _, g := range out {
		if g.err != nil {
			t.Fatalf("claim %v", g.err)
		}
		if stored == "" {
			stored = g.stored
		}
		if g.stored != stored {
			t.Fatalf("stored %s %s", g.stored, stored)
		}
		if g.claimed {
			claimedN++
			if g.stored != first && g.stored != second {
				t.Fatalf("stored %s", g.stored)
			}
		}
	}
	if claimedN != 1 || (stored != first && stored != second) {
		t.Fatalf("claimed %d stored %s", claimedN, stored)
	}
	claimed, later, err := h.st.ClaimOpenPullRequest(ctx, item.ID, "https://github.com/acme/payments/pull/99")
	if err != nil || claimed || later != stored {
		t.Fatalf("later claim %v %q err %v", claimed, later, err)
	}
	claimed, later, err = h.st.ClaimOpenPullRequest(ctx, item.ID, stored)
	if err != nil || claimed || later != stored {
		t.Fatalf("same url %v %q err %v", claimed, later, err)
	}
}

func TestBuildingIntakeSharesOneRun(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#83", automation.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.ProviderGitHub,
		SourceRef: "acme/repo#83",
		Title:     "building intake",
		Stage:     store.StageBuilding,
		Status:    store.WorkItemOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := func(prompt string) *automation.NewRun {
		return &automation.NewRun{
			FactoryID: factoryID,
			Agent:     "implementer",
			Pool:      "default",
			Priority:  automation.PriorityPipeline,
			Prompt:    prompt,
			Title:     "building intake",
			Stage:     store.StageBuilding,
		}
	}
	got, err := a.EnsureIntake(ctx, item.ID, "dlv_build", []automation.IntakeSlot{
		{Automation: "left", Run: run("left")},
		{Automation: "right", Run: run("right")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Slots) != 2 || !got.Slots[0].Inserted || !got.Slots[1].Inserted {
		t.Fatalf("slots %+v", got.Slots)
	}
	if got.Slots[0].Run.ID == "" || got.Slots[0].Run.ID != got.Slots[1].Run.ID {
		t.Fatalf("runs %s %s", got.Slots[0].Run.ID, got.Slots[1].Run.ID)
	}
	if n := countRows(t, h, `SELECT count(*) FROM runs WHERE work_item_id = $1`, item.ID); n != 1 {
		t.Fatalf("runs %d", n)
	}
}

func TestFollowUpDoesNotStackBuildingRuns(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#84", automation.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.ProviderGitHub,
		SourceRef: "acme/repo#84",
		Title:     "follow",
		Stage:     store.StageBuilding,
		Status:    store.WorkItemOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	follow := automation.NewRun{
		FactoryID:      factoryID,
		Agent:          "implementer",
		Pool:           "default",
		Priority:       automation.PriorityPipeline,
		Prompt:         "follow up",
		Title:          item.Title,
		WorkItemID:     item.ID,
		ConversationID: item.ConversationID,
		Stage:          store.StageBuilding,
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	type got struct {
		id      string
		created bool
		err     error
	}
	ids := []string{"dlv_a", "dlv_b"}
	out := make([]got, len(ids))
	wg.Add(len(ids))
	for i, id := range ids {
		go func(i int, id string) {
			defer wg.Done()
			<-start
			run, created, err := a.FollowUp(ctx, id, follow, store.StageBuilding, store.WorkItemOpen)
			out[i] = got{run.ID, created, err}
		}(i, id)
	}
	close(start)
	wg.Wait()
	createdN := 0
	var runID string
	for _, g := range out {
		if g.err != nil {
			t.Fatalf("follow %v", g.err)
		}
		if runID == "" {
			runID = g.id
		}
		if g.id != runID {
			t.Fatalf("run ids %s %s", g.id, runID)
		}
		if g.created {
			createdN++
		}
	}
	if createdN != 1 || runID == "" {
		t.Fatalf("created %d run %s", createdN, runID)
	}
	if n := countRows(t, h, `
		SELECT count(*) FROM runs
		WHERE work_item_id = $1 AND stage = 'building'
		  AND status IN ('queued', 'assigned', 'running')`, item.ID); n != 1 {
		t.Fatalf("in flight %d", n)
	}
}

func TestMigrateSupersedesDuplicateBuildingRuns(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source: store.SourceIssue,
		Title:  "duplicates",
	})
	other := createWorkItem(t, h, factoryID, store.NewWorkItem{
		Source: store.SourceIssue,
		Title:  "single",
	})
	if _, err := h.raw.Exec(ctx, `DROP INDEX runs_one_building_inflight`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		DELETE FROM schema_migrations WHERE filename = '0005_building_guard.sql'`); err != nil {
		t.Fatal(err)
	}
	const (
		olderID = "run_older_building"
		newerID = "run_newer_building"
		keptID  = "run_kept_building"
		doneID  = "run_done_building"
		execID  = "exec_older_building"
	)
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO runs (
		    id, factory_id, agent_id, status, priority, pool, prompt, title,
		    work_item_id, stage, created_at)
		VALUES
		    ($1, $2, $3, 'running', 2, 'default', 'older prompt', 'duplicates',
		     $4, 'building', '2026-10-17T02:56:00Z'),
		    ($5, $2, $3, 'queued', 2, 'default', 'newer prompt', 'duplicates',
		     $4, 'building', '2026-10-17T02:56:30Z'),
		    ($6, $2, $3, 'queued', 2, 'default', 'only prompt', 'single',
		     $7, 'building', '2026-10-17T02:56:10Z'),
		    ($8, $2, $3, 'succeeded', 2, 'default', 'done', 'duplicates',
		     $4, 'building', '2026-10-17T02:50:00Z')`,
		olderID, factoryID, agentID, item.ID, newerID, keptID, other.ID, doneID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO executions (
		    id, run_id, attempt, worker_id, state, run_token_sha256)
		VALUES ($1, $2, 1, 'worker-1', 'running', $3)`,
		execID, olderID, tokenHash("older")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO run_queue (run_id, factory_id, pool, priority, enqueued_at)
		VALUES
		    ($1, $2, 'default', 2, '2026-10-17T02:56:00Z'),
		    ($3, $2, 'default', 2, '2026-10-17T02:56:30Z'),
		    ($4, $2, 'default', 2, '2026-10-17T02:56:10Z')`,
		olderID, factoryID, newerID, keptID); err != nil {
		t.Fatal(err)
	}

	if err := h.st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var status, reason string
	var ended bool
	if err := h.raw.QueryRow(ctx, `
		SELECT status, COALESCE(failure_reason, ''), ended_at IS NOT NULL
		FROM runs WHERE id = $1`, olderID).Scan(&status, &reason, &ended); err != nil {
		t.Fatal(err)
	}
	if status != store.StatusFailed || reason != "superseded_duplicate_building_run" || !ended {
		t.Fatalf("older run status %s reason %s ended %v", status, reason, ended)
	}
	var execState, execReason string
	var execEnded bool
	if err := h.raw.QueryRow(ctx, `
		SELECT state, COALESCE(failure_reason, ''), ended_at IS NOT NULL
		FROM executions WHERE id = $1`, execID).Scan(&execState, &execReason, &execEnded); err != nil {
		t.Fatal(err)
	}
	if execState != "failed" || execReason != "superseded_duplicate_building_run" || !execEnded {
		t.Fatalf("execution state %s reason %s ended %v", execState, execReason, execEnded)
	}
	if n := countRows(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, olderID); n != 0 {
		t.Fatalf("older queue rows %d", n)
	}
	if err := h.raw.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, newerID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != store.StatusQueued {
		t.Fatalf("newest status %s", status)
	}
	if n := countRows(t, h, `
		SELECT count(*) FROM runs
		WHERE work_item_id = $1 AND stage = 'building'
		  AND status IN ('queued', 'assigned', 'running')`, item.ID); n != 1 {
		t.Fatalf("in flight %d", n)
	}
	if err := h.raw.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, keptID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != store.StatusQueued {
		t.Fatalf("other item status %s", status)
	}
	if err := h.raw.QueryRow(ctx, `
		SELECT status, COALESCE(failure_reason, '') FROM runs WHERE id = $1`, doneID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != store.StatusSucceeded || reason != "" {
		t.Fatalf("finished run status %s reason %s", status, reason)
	}
}

func TestFollowUpLoserKeepsReply(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#85", automation.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.ProviderGitHub,
		SourceRef: "acme/repo#85",
		Title:     "follow",
		Body:      "original request\n",
		Stage:     store.StageReviewing,
		Status:    store.WorkItemOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := followWithPrompt(factoryID, item, "build the feature\n")
	winner, created, err := a.FollowUp(ctx, "dlv_win", first, store.StageBuilding, store.WorkItemOpen)
	if err != nil || !created {
		t.Fatalf("winner %+v created %v err %v", winner, created, err)
	}
	const reply = "also add a test for an empty name\n"
	loser, created, err := a.FollowUp(ctx, "dlv_lose", followWithPrompt(factoryID, item, reply), store.StageBuilding, store.WorkItemOpen)
	if err != nil || created || loser.ID != winner.ID {
		t.Fatalf("loser %+v created %v err %v", loser, created, err)
	}
	if !strings.Contains(loser.Prompt, "build the feature") || !strings.Contains(loser.Prompt, reply) {
		t.Fatalf("in-flight prompt dropped the reply:\n%s", loser.Prompt)
	}
	loaded, err := h.st.GetWorkItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(loaded.Body, reply) != 1 {
		t.Fatalf("body %q", loaded.Body)
	}
	if n := countRows(t, h, `
		SELECT count(*) FROM runs
		WHERE work_item_id = $1 AND stage = 'building'
		  AND status IN ('queued', 'assigned', 'running')`, item.ID); n != 1 {
		t.Fatalf("in flight %d", n)
	}
	again, created, err := a.FollowUp(ctx, "dlv_lose", followWithPrompt(factoryID, item, reply), store.StageBuilding, store.WorkItemOpen)
	if err != nil || created || again.ID != winner.ID {
		t.Fatalf("replay %+v created %v err %v", again, created, err)
	}
	loaded, err = h.st.GetWorkItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(loaded.Body, reply) != 1 || strings.Count(again.Prompt, reply) != 1 {
		t.Fatalf("replay duplicated the reply body %q prompt %q", loaded.Body, again.Prompt)
	}

	if _, err := h.raw.Exec(ctx, `
		UPDATE runs SET status = 'succeeded', ended_at = clock_timestamp() WHERE id = $1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = h.st.GetWorkItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(loaded.Body, reply) != 1 {
		t.Fatalf("body lost the reply after the in-flight run finished: %q", loaded.Body)
	}
	next, created, err := a.FollowUp(ctx, "dlv_next", followWithPrompt(factoryID, item, loaded.Body), store.StageBuilding, store.WorkItemOpen)
	if err != nil || !created || next.ID == winner.ID {
		t.Fatalf("next %+v created %v err %v", next, created, err)
	}
	if !strings.Contains(next.Prompt, reply) {
		t.Fatalf("next building run missed the reply:\n%s", next.Prompt)
	}
	if n := countRows(t, h, `
		SELECT count(*) FROM runs
		WHERE work_item_id = $1 AND stage = 'building'
		  AND status IN ('queued', 'assigned', 'running')`, item.ID); n != 1 {
		t.Fatalf("in flight %d", n)
	}
}

func followWithPrompt(factoryID string, item automation.WorkItem, prompt string) automation.NewRun {
	return automation.NewRun{
		FactoryID:      factoryID,
		Agent:          "implementer",
		Pool:           "default",
		Priority:       automation.PriorityPipeline,
		Prompt:         prompt,
		Title:          item.Title,
		WorkItemID:     item.ID,
		ConversationID: item.ConversationID,
		Stage:          store.StageBuilding,
	}
}

func buildingInput(factoryID, agentID, itemID, prompt string) store.NewRun {
	return store.NewRun{
		FactoryID:  factoryID,
		AgentID:    agentID,
		Pool:       "default",
		Priority:   store.PriorityPipeline,
		Prompt:     prompt,
		Title:      "work",
		WorkItemID: itemID,
		Stage:      store.StageBuilding,
	}
}
