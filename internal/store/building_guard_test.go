package store_test

import (
	"context"
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
