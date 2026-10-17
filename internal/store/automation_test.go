package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/store"
)

func TestAutomationIntake(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, created, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#1", newAutoItem(factoryID, "acme/repo#1", "title"))
	if err != nil || !created || item.ID == "" || item.ConversationID == "" || item.Source != store.ProviderGitHub {
		t.Fatalf("open %+v created %v err %v", item, created, err)
	}
	ref, err := a.GetExternalRef(ctx, store.ProviderGitHub, "acme/repo#1")
	if err != nil || ref.WorkItemID != item.ID || ref.ConversationID != item.ConversationID {
		t.Fatalf("ref %+v %v", ref, err)
	}

	first, err := a.EnsureIntake(ctx, item.ID, "dlv_1", []automation.IntakeSlot{
		{Automation: "beta", Run: autoRun(factoryID, "beta")},
		{Automation: "alpha", Run: nil},
	})
	if err != nil || first.Continuation || len(first.Slots) != 2 {
		t.Fatalf("first %+v %v", first, err)
	}
	if !first.Slots[0].Inserted || first.Slots[0].Run.ID == "" || first.Slots[0].Automation != "beta" {
		t.Fatalf("beta slot %+v", first.Slots[0])
	}
	if !first.Slots[1].Inserted || first.Slots[1].Run.ID != "" || first.Slots[1].Automation != "alpha" {
		t.Fatalf("alpha slot %+v", first.Slots[1])
	}
	owners, err := a.Owners(ctx, item.ID)
	if err != nil || strings.Join(owners, ",") != "alpha,beta" {
		t.Fatalf("owners %#v %v", owners, err)
	}
	if n := countRows(t, h, `SELECT count(*) FROM runs WHERE work_item_id = $1`, item.ID); n != 1 {
		t.Fatalf("runs %d", n)
	}

	again, err := a.EnsureIntake(ctx, item.ID, "dlv_1", []automation.IntakeSlot{
		{Automation: "beta", Run: autoRun(factoryID, "beta")},
		{Automation: "alpha"},
		{Automation: "gamma", Run: autoRun(factoryID, "gamma")},
	})
	if err != nil || again.Continuation || len(again.Slots) != 3 {
		t.Fatalf("again %+v %v", again, err)
	}
	if again.Slots[0].Inserted || again.Slots[0].Run.ID != first.Slots[0].Run.ID {
		t.Fatalf("beta replay %+v", again.Slots[0])
	}
	if again.Slots[1].Inserted || again.Slots[2].Inserted == false || again.Slots[2].Automation != "gamma" {
		t.Fatalf("fill %+v", again.Slots)
	}
	if n := countRows(t, h, `SELECT count(*) FROM runs WHERE work_item_id = $1`, item.ID); n != 2 {
		t.Fatalf("runs after fill %d", n)
	}

	cont, err := a.EnsureIntake(ctx, item.ID, "dlv_2", []automation.IntakeSlot{
		{Automation: "beta", Run: autoRun(factoryID, "beta")},
		{Automation: "other", Run: autoRun(factoryID, "other")},
	})
	if err != nil || !cont.Continuation || len(cont.Slots) != 0 || cont.Item.ID != item.ID {
		t.Fatalf("continuation %+v %v", cont, err)
	}
	if n := countRows(t, h, `SELECT count(*) FROM intake_slots WHERE work_item_id = $1`, item.ID); n != 3 {
		t.Fatalf("slots after continuation %d", n)
	}

	fresh, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#2", newAutoItem(factoryID, "acme/repo#2", "other"))
	if err != nil {
		t.Fatal(err)
	}
	h.st.SetIntakeHookForTest(func(context.Context) error { return errors.New("boom") })
	t.Cleanup(func() { h.st.SetIntakeHookForTest(nil) })
	_, err = a.EnsureIntake(ctx, fresh.ID, "dlv_fail", []automation.IntakeSlot{
		{Automation: "one", Run: autoRun(factoryID, "implementer")},
		{Automation: "two", Run: autoRun(factoryID, "implementer")},
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("hook err %v", err)
	}
	if n := countRows(t, h, `SELECT count(*) FROM intake_slots WHERE work_item_id = $1`, fresh.ID); n != 0 {
		t.Fatalf("slots after rollback %d", n)
	}
	if n := countRows(t, h, `SELECT count(*) FROM runs WHERE work_item_id = $1`, fresh.ID); n != 0 {
		t.Fatalf("runs after rollback %d", n)
	}
	if _, err := a.Owners(ctx, fresh.ID); err != nil {
		t.Fatalf("owners after rollback %v", err)
	}
}

func TestAutomationIntakeRace(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, _, err := a.OpenWorkItem(ctx, store.ProviderSlack, "slack:T/C/1", newAutoItem(factoryID, "slack:T/C/1", "thread"))
	if err != nil {
		t.Fatal(err)
	}
	const n = 2
	type got struct {
		res automation.IntakeResult
		err error
	}
	out := make([]got, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := a.EnsureIntake(ctx, item.ID, fmt.Sprintf("dlv_race_%d", i), []automation.IntakeSlot{
				{Automation: "labeled", Run: autoRun(factoryID, "implementer")},
				{Automation: "mention", Run: autoRun(factoryID, "implementer")},
			})
			out[i] = got{res, err}
		}(i)
	}
	close(start)
	wg.Wait()
	inserted, cont := 0, 0
	for _, g := range out {
		if g.err != nil {
			t.Fatalf("race err %v", g.err)
		}
		if g.res.Continuation {
			cont++
			if len(g.res.Slots) != 0 || g.res.Item.ID != item.ID {
				t.Fatalf("continuation %+v", g.res)
			}
			continue
		}
		inserted++
		if len(g.res.Slots) != 2 || !g.res.Slots[0].Inserted || !g.res.Slots[1].Inserted {
			t.Fatalf("winner %+v", g.res)
		}
	}
	if inserted != 1 || cont != 1 {
		t.Fatalf("inserted %d continuation %d", inserted, cont)
	}
	if n := countRows(t, h, `SELECT count(*) FROM intake_slots WHERE work_item_id = $1`, item.ID); n != 2 {
		t.Fatalf("slots %d", n)
	}
	if n := countRows(t, h, `SELECT count(*) FROM runs WHERE work_item_id = $1`, item.ID); n != 2 {
		t.Fatalf("runs %d", n)
	}
	owners, err := a.Owners(ctx, item.ID)
	if err != nil || strings.Join(owners, ",") != "labeled,mention" {
		t.Fatalf("owners %#v %v", owners, err)
	}
}

func TestAutomationSameDeliveryRace(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#8", newAutoItem(factoryID, "acme/repo#8", "dup"))
	if err != nil {
		t.Fatal(err)
	}
	const n = 2
	type got struct {
		res automation.IntakeResult
		err error
	}
	out := make([]got, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := a.EnsureIntake(ctx, item.ID, "dlv_same", []automation.IntakeSlot{
				{Automation: "labeled", Run: autoRun(factoryID, "implementer")},
			})
			out[i] = got{res, err}
		}(i)
	}
	close(start)
	wg.Wait()
	inserted := 0
	var runID string
	for _, g := range out {
		if g.err != nil || g.res.Continuation || len(g.res.Slots) != 1 {
			t.Fatalf("same delivery %+v %v", g.res, g.err)
		}
		if g.res.Slots[0].Inserted {
			inserted++
			runID = g.res.Slots[0].Run.ID
		} else if g.res.Slots[0].Run.ID == "" {
			t.Fatalf("missing run %+v", g.res.Slots[0])
		}
	}
	if inserted != 1 || runID == "" {
		t.Fatalf("inserted %d run %s", inserted, runID)
	}
	for _, g := range out {
		if g.res.Slots[0].Run.ID != runID {
			t.Fatalf("run ids %s %s", g.res.Slots[0].Run.ID, runID)
		}
	}
	if rows := countRows(t, h, `SELECT count(*) FROM runs WHERE work_item_id = $1`, item.ID); rows != 1 {
		t.Fatalf("runs %d", rows)
	}
}

func TestAutomationOpenWorkItemRace(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	const n = 2
	type got struct {
		item    automation.WorkItem
		created bool
		err     error
	}
	out := make([]got, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			item, created, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#9", automation.NewWorkItem{
				FactoryID: factoryID,
				Source:    store.ProviderGitHub,
				SourceRef: "acme/repo#9",
				Title:     fmt.Sprintf("title-%d", i),
				Body:      "fence",
				Stage:     store.StageTriage,
				Status:    store.WorkItemOpen,
				CreatedBy: "pat",
			})
			out[i] = got{item, created, err}
		}(i)
	}
	close(start)
	wg.Wait()
	createdN := 0
	var winner automation.WorkItem
	for _, g := range out {
		if g.err != nil {
			t.Fatalf("open %v", g.err)
		}
		if g.created {
			createdN++
			winner = g.item
		}
	}
	if createdN != 1 || winner.ID == "" || winner.ConversationID == "" {
		t.Fatalf("created %d winner %+v", createdN, winner)
	}
	for _, g := range out {
		if g.item.ID != winner.ID || g.item.Title != winner.Title || g.item.ConversationID != winner.ConversationID {
			t.Fatalf("loser %+v winner %+v", g.item, winner)
		}
	}
	if n := countRows(t, h, `SELECT count(*) FROM work_items`); n != 1 {
		t.Fatalf("work items %d", n)
	}
	if n := countRows(t, h, `SELECT count(*) FROM external_refs`); n != 1 {
		t.Fatalf("refs %d", n)
	}

	_, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/missing", automation.NewWorkItem{
		FactoryID: "fac_missing",
		Source:    store.ProviderGitHub,
		SourceRef: "acme/missing",
		Title:     "nope",
	})
	if err == nil {
		t.Fatal("missing factory inserted")
	}
	if n := countRows(t, h, `SELECT count(*) FROM external_refs WHERE external_ref = 'acme/missing'`); n != 0 {
		t.Fatalf("rolled-back ref rows %d", n)
	}
}

func TestAutomationFollowUp(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#3", newAutoItem(factoryID, "acme/repo#3", "pr"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnsureIntake(ctx, item.ID, "dlv_open", []automation.IntakeSlot{
		{Automation: "labeled", Run: autoRun(factoryID, "implementer")},
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := a.LatestRun(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	follow := followRun(factoryID, item)
	follow.ParentRunID = latest.ID
	run, created, err := a.FollowUp(ctx, "dlv_fu", follow, store.StageBuilding, store.WorkItemOpen)
	if err != nil || !created || run.ID == "" || run.WorkItemID != item.ID || run.ConversationID != item.ConversationID {
		t.Fatalf("follow %+v %v %v", run, created, err)
	}
	if run.ParentRunID != latest.ID || run.Agent != "implementer" {
		t.Fatalf("parent %s agent %s", run.ParentRunID, run.Agent)
	}
	loaded, err := a.GetWorkItem(ctx, item.ID)
	if err != nil || loaded.Stage != store.StageBuilding || loaded.Status != store.WorkItemOpen {
		t.Fatalf("stage %+v %v", loaded, err)
	}
	again, created, err := a.FollowUp(ctx, "dlv_fu", followRun(factoryID, item), store.StageBuilding, store.WorkItemOpen)
	if err != nil || created || again.ID != run.ID || again.Prompt != run.Prompt {
		t.Fatalf("replay %+v created %v err %v", again, created, err)
	}
	if n := countRows(t, h, `SELECT count(*) FROM runs WHERE work_item_id = $1`, item.ID); n != 2 {
		t.Fatalf("runs %d", n)
	}
	if n := countRows(t, h, `SELECT count(*) FROM delivery_effects WHERE delivery_id = 'dlv_fu'`); n != 1 {
		t.Fatalf("effects %d", n)
	}
	// The building run has to finish before another follow-up may start.
	// A second delivery while it is queued is a no-op.
	if _, err := h.raw.Exec(ctx, `
		UPDATE runs SET status = 'succeeded', ended_at = clock_timestamp() WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	type got struct {
		run     automation.Run
		created bool
		err     error
	}
	out := make([]got, 2)
	wg.Add(2)
	for i := range 2 {
		go func(i int) {
			defer wg.Done()
			<-start
			run, created, err := a.FollowUp(ctx, "dlv_fu_race", followRun(factoryID, item), store.StageBuilding, store.WorkItemOpen)
			out[i] = got{run, created, err}
		}(i)
	}
	close(start)
	wg.Wait()
	createdN := 0
	var id string
	for _, g := range out {
		if g.err != nil {
			t.Fatalf("race %v", g.err)
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
	if n := countRows(t, h, `SELECT count(*) FROM delivery_effects WHERE delivery_id = 'dlv_fu_race'`); n != 1 {
		t.Fatalf("race effects %d", n)
	}
}

func TestAutomationAppendContext(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, _, err := a.OpenWorkItem(ctx, store.ProviderSlack, "slack:T/C/2", newAutoItem(factoryID, "slack:T/C/2", "fence-title"))
	if err != nil {
		t.Fatal(err)
	}
	item.Body = "FENCE"
	if _, err := h.raw.Exec(ctx, `UPDATE work_items SET body = 'FENCE' WHERE id = $1`, item.ID); err != nil {
		t.Fatal(err)
	}
	applied, err := a.AppendContext(ctx, item.ID, "dlv_n1", "one")
	if err != nil || !applied {
		t.Fatalf("first %v %v", applied, err)
	}
	applied, err = a.AppendContext(ctx, item.ID, "dlv_n2", "two")
	if err != nil || !applied {
		t.Fatalf("second %v %v", applied, err)
	}
	applied, err = a.AppendContext(ctx, item.ID, "dlv_n1", "one-again")
	if err != nil || applied {
		t.Fatalf("replay %v %v", applied, err)
	}
	notes, err := a.Context(ctx, item.ID)
	if err != nil || notes != "onetwo" {
		t.Fatalf("context %q %v", notes, err)
	}
	loaded, err := a.GetWorkItem(ctx, item.ID)
	if err != nil || loaded.Body != "FENCE"+notes {
		t.Fatalf("body %q %v", loaded.Body, err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	const racers = 2
	errs := make([]error, racers)
	wg.Add(racers)
	for i := range racers {
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = a.AppendContext(ctx, item.ID, fmt.Sprintf("dlv_race_%d", i), fmt.Sprintf("N%d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	notes, err = a.Context(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(notes, "N0") != 1 || strings.Count(notes, "N1") != 1 {
		t.Fatalf("lost notes %q", notes)
	}
	loaded, err = a.GetWorkItem(ctx, item.ID)
	if err != nil || loaded.Body != "FENCE"+notes {
		t.Fatalf("body %q notes %q err %v", loaded.Body, notes, err)
	}
}

func TestAutomationSentinels(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	a := &store.Automation{Store: h.st}
	_, err := a.GetExternalRef(ctx, store.ProviderGitHub, "missing")
	if !errors.Is(err, automation.ErrNotFound) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("external ref %v", err)
	}
	_, err = a.LatestRun(ctx, "task_missing")
	if !errors.Is(err, automation.ErrNotFound) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("latest %v", err)
	}
	_, err = a.GetWorkItem(ctx, "task_missing")
	if !errors.Is(err, automation.ErrNotFound) {
		t.Fatalf("work item %v", err)
	}
	if _, err := a.Owners(ctx, "task_missing"); !errors.Is(err, automation.ErrNotFound) {
		t.Fatalf("owners %v", err)
	}

	item := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "ask"})
	if _, err := h.st.AnswerInputRequest(ctx, item.ID, "yes", "pat"); !errors.Is(err, automation.ErrNotFound) || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("answer missing %v", err)
	}
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityPipeline)
	if _, err := h.st.CreateInputRequest(ctx, item.ID, run.ID, "exec_1", "which?"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AnswerInputRequest(ctx, item.ID, "left", "pat"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AnswerInputRequest(ctx, item.ID, "right", "pat"); !errors.Is(err, automation.ErrConflict) || !errors.Is(err, store.ErrConflict) {
		t.Fatalf("answer conflict %v", err)
	}

	opened, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#4", newAutoItem(factoryID, "acme/repo#4", "latest"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.EnsureIntake(ctx, opened.ID, "dlv_latest", []automation.IntakeSlot{
		{Automation: "labeled", Run: autoRun(factoryID, "implementer")},
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := a.LatestRun(ctx, opened.ID)
	if err != nil || latest.ID != got.Slots[0].Run.ID {
		t.Fatalf("latest %+v slot %s err %v", latest, got.Slots[0].Run.ID, err)
	}

	url, ok, err := a.OpenPullRequest(ctx, opened.ID)
	if err != nil || ok || url != "" {
		t.Fatalf("pr %q %v %v", url, ok, err)
	}
	if err := h.st.SetOpenPullRequest(ctx, opened.ID, "https://github.com/acme/repo/pull/4"); err != nil {
		t.Fatal(err)
	}
	url, ok, err = a.OpenPullRequest(ctx, opened.ID)
	if err != nil || !ok || url != "https://github.com/acme/repo/pull/4" {
		t.Fatalf("pr set %q %v %v", url, ok, err)
	}
	if _, ok, err := a.OpenPullRequest(ctx, "task_missing"); err != nil || ok {
		t.Fatalf("missing pr %v %v", ok, err)
	}
}

func newAutoItem(factoryID, ref, title string) automation.NewWorkItem {
	return automation.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.ProviderGitHub,
		SourceRef: ref,
		Title:     title,
		Body:      "FENCE",
		Stage:     store.StageTriage,
		Status:    store.WorkItemOpen,
		CreatedBy: "pat",
	}
}

func autoRun(factoryID, agent string) *automation.NewRun {
	return &automation.NewRun{
		FactoryID: factoryID,
		Agent:     agent,
		Pool:      "default",
		Priority:  automation.PriorityPipeline,
		Prompt:    "do the work",
		Title:     "work",
		Stage:     store.StageTriage,
		ModelID:   "model",
	}
}

func followRun(factoryID string, item automation.WorkItem) automation.NewRun {
	return automation.NewRun{
		FactoryID:      factoryID,
		Agent:          "implementer",
		Pool:           "default",
		Priority:       automation.PriorityPipeline,
		Prompt:         "follow up",
		Title:          item.Title,
		WorkItemID:     item.ID,
		ConversationID: item.ConversationID,
		Stage:          store.StageBuilding,
		ModelID:        "model",
	}
}

func countRows(t *testing.T, h *harness, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.raw.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
