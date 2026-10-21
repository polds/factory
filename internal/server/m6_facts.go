package server

import (
	"context"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/store"
)

// notePRFact records pull request lifecycle on pr_facts and reviews the
// matching improvement batch when a pull request merges or closes.
// Push events have no pull request number. The number is taken from the
// improvement batch whose branch is improve/<batch id>. An unresolved
// number is skipped.
func (s *Server) notePRFact(ctx context.Context, ev event.Event) {
	if ev.Provider != event.ProviderGitHub {
		return
	}
	switch ev.Event {
	case "pull_request_opened", "pull_request_closed", "pull_request_merged", "pull_request_review_submitted", "push":
	default:
		return
	}
	snap := s.snapshot()
	if snap.factory.ID == "" {
		return
	}
	repo := strings.Trim(ev.Subject.Repo.Owner+"/"+ev.Subject.Repo.Name, "/")
	if ev.Subject.Repo.Owner == "" || ev.Subject.Repo.Name == "" {
		return
	}
	number := ev.Subject.Number
	branch := ""
	if ev.Subject.Branch != nil {
		branch = *ev.Subject.Branch
	}
	batch, matched := s.batchForEvent(ctx, snap.factory.ID, number, ev.Subject.URL, branch)
	if number <= 0 && matched && batch.PRNumber > 0 {
		number = batch.PRNumber
	}
	if number <= 0 {
		return
	}
	when := ev.OccurredAt
	if when.IsZero() {
		when = s.now()
	}
	human := humanActor(ev.Actor.Handle)
	fact := store.PRFact{
		FactoryID: snap.factory.ID,
		Repo:      repo,
		Number:    number,
	}
	switch ev.Event {
	case "pull_request_opened":
		fact.OpenedAt = &when
		fact.HumanPushSeen = &human
	case "pull_request_merged":
		fact.MergedAt = &when
	case "pull_request_closed":
		fact.ClosedAt = &when
	case "pull_request_review_submitted":
		fact.FirstReviewAt = &when
	case "push":
		if human {
			fact.HumanPushSeen = &human
		}
	}
	if matched {
		yes := true
		fact.IsImprovement = &yes
	}
	if itemID := s.workItemForPullRequest(ctx, ev); itemID != "" {
		fact.WorkItemID = itemID
	}
	if _, err := s.store.UpsertPRFact(ctx, fact); err != nil {
		s.log.Error("pr fact", "err", err)
	}
	if !matched {
		return
	}
	switch ev.Event {
	case "pull_request_merged":
		if err := s.store.MarkBatchReviewed(ctx, batch.ID, api.BatchMerged, ""); err != nil {
			s.log.Error("batch merged", "batch", batch.ID, "err", err)
		}
	case "pull_request_closed":
		note := strings.TrimSpace(ev.Subject.Text)
		if note == "" {
			note = "closed without merge"
		}
		if err := s.store.MarkBatchReviewed(ctx, batch.ID, api.BatchClosed, note); err != nil {
			s.log.Error("batch closed", "batch", batch.ID, "err", err)
		}
	}
}

func humanActor(handle string) bool {
	handle = strings.TrimSpace(handle)
	return handle != "" && !strings.Contains(handle, "[bot]")
}

func (s *Server) batchForEvent(ctx context.Context, factoryID string, number int, prURL, branch string) (store.ImprovementBatch, bool) {
	batches, err := s.store.ImprovementBatches(ctx, factoryID)
	if err != nil {
		s.log.Error("pr batches", "err", err)
		return store.ImprovementBatch{}, false
	}
	for _, batch := range batches {
		if number > 0 && batch.PRNumber == number {
			return batch, true
		}
		if prURL != "" && batch.PRURL != "" && batch.PRURL == prURL {
			return batch, true
		}
		if branch != "" && branch == improveBranchPrefix+batch.ID {
			return batch, true
		}
	}
	return store.ImprovementBatch{}, false
}
