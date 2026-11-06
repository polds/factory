package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

// AdmitInvocation admits one defined-factory occurrence. API, ingress, and schedules call this directly.
func (s *Server) AdmitInvocation(ctx context.Context, factoryUID string, principal invocation.Principal, req invocation.Request) (invocation.Receipt, error) {
	if !principal.Allow {
		return invocation.Receipt{}, invocation.Reject{Code: invocation.CodePrincipalRejected, Status: 403}
	}
	if err := s.rejectProfileInjection(req); err != nil {
		return invocation.Receipt{}, err
	}
	if req.ExecuteTargetGrant && !s.cfg.ExecuteTargetGrant {
		return invocation.Receipt{}, invocation.Reject{Code: invocation.CodeGrantRejected, Status: 403}
	}
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return invocation.Receipt{}, err
	}
	if req.Target != nil && req.Target.Repository != "" {
		if !repositoryAllowed(s.snapshot().tree, req.Target.Repository) {
			return invocation.Receipt{}, invocation.Reject{Code: invocation.CodeRepositoryRejected, Status: 403}
		}
	}
	digest := invocation.Digest(req)
	profileName := req.Profile.Name
	if req.OverlapPolicy == "forbid" && req.ScheduleID != "" {
		if busy, err := s.scheduleOverlapBusy(ctx, fac.ID, req.ScheduleID); err != nil {
			return invocation.Receipt{}, err
		} else if busy {
			return s.recordSkippedOverlap(ctx, fac, principal, req, digest)
		}
	}
	tree := s.snapshot().tree
	if tree == nil {
		return invocation.Receipt{}, fmt.Errorf("server: definition not loaded")
	}
	defDigest := tree.Digest
	defRevision := req.DefinitionRevision
	if defRevision == "" {
		defRevision = "applied"
	}
	headSHA, baseSHA, err := s.resolveTargetSHAs(ctx, req.Target)
	if err != nil {
		return invocation.Receipt{}, err
	}
	principalJSON, err := json.Marshal(principal)
	if err != nil {
		return invocation.Receipt{}, err
	}
	corrJSON, err := json.Marshal(req.Correlation)
	if err != nil {
		return invocation.Receipt{}, err
	}
	agentName, stage, err := invocationAgent(tree, req.Profile.Name)
	if err != nil {
		return invocation.Receipt{}, err
	}
	ins, err := s.store.InsertInvocation(ctx, store.InsertInvocationParams{
		FactoryID:           fac.ID,
		ProfileName:         profileName,
		SourceOccurrenceKey: req.SourceOccurrenceKey,
		CanonicalDigest:     digest,
		DefinitionRevision:  defRevision,
		DefinitionDigest:    defDigest,
		PrincipalJSON:       principalJSON,
		State:               invocation.StateAccepted,
		CountsAsCompleted:   true,
		Intent:              req.Intent,
		Priority:            req.Priority,
		CorrelationJSON:     corrJSON,
		TargetRepository:    targetRepo(req.Target),
		TargetRef:           targetRef(req.Target),
		TargetPullRequest:   targetPR(req.Target),
		PinnedHeadSHA:       headSHA,
		PinnedBaseSHA:       baseSHA,
		NeutralStage:        true,
	})
	if err != nil {
		return invocation.Receipt{}, err
	}
	if ins.Conflict {
		return invocation.Receipt{}, invocation.ErrConflict
	}
	if !ins.Created && (ins.Row.WorkItemID != "" || ins.Row.State != invocation.StateAccepted) {
		return s.receiptFromRow(fac.Name, ins.Row, true), nil
	}
	if ins.Row.WorkItemID != "" {
		return s.receiptFromRow(fac.Name, ins.Row, !ins.Created), nil
	}
	item, err := s.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID,
		Source:    invocationSource(principal),
		SourceRef: req.SourceOccurrenceKey,
		Title:     invocationTitle(req),
		Body:      invocationBody(req),
		Stage:     stage,
		Status:    store.WorkItemOpen,
		CreatedBy: principal.Actor,
	})
	if err != nil {
		return invocation.Receipt{}, err
	}
	if err := s.store.LinkInvocationWorkItem(ctx, ins.Row.ID, item.ID); err != nil {
		return invocation.Receipt{}, err
	}
	ins.Row.WorkItemID = item.ID
	if _, err := s.startStageRun(ctx, item, stage, agentName, ""); err != nil {
		return invocation.Receipt{}, err
	}
	return s.receiptFromRow(fac.Name, ins.Row, !ins.Created), nil
}

func (s *Server) rejectProfileInjection(req invocation.Request) error {
	for k := range req.Inputs {
		switch k {
		case "budgetCeilingCents", "credentials", "grants", "code":
			return invocation.Reject{Code: invocation.CodeProfileRejected, Status: 403}
		}
	}
	return nil
}

func (s *Server) scheduleOverlapBusy(ctx context.Context, factoryID, scheduleID string) (bool, error) {
	row, err := s.store.ActiveInvocationForSchedule(ctx, factoryID, scheduleID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.WorkItemID == "" {
		return false, nil
	}
	item, err := s.store.GetWorkItem(ctx, row.WorkItemID)
	if err != nil {
		return false, err
	}
	return item.Status == store.WorkItemOpen, nil
}

func (s *Server) recordSkippedOverlap(ctx context.Context, fac store.Factory, principal invocation.Principal, req invocation.Request, digest string) (invocation.Receipt, error) {
	principalJSON, _ := json.Marshal(principal)
	ins, err := s.store.InsertInvocation(ctx, store.InsertInvocationParams{
		FactoryID:           fac.ID,
		ProfileName:         req.Profile.Name,
		SourceOccurrenceKey: req.SourceOccurrenceKey,
		CanonicalDigest:     digest,
		DefinitionRevision:  req.DefinitionRevision,
		DefinitionDigest:    s.snapshot().tree.Digest,
		PrincipalJSON:       principalJSON,
		State:               invocation.StateSkippedOverlap,
		Outcome:             invocation.OutcomeSkippedOverlap,
		CountsAsCompleted:   false,
		Intent:              req.Intent,
		Priority:            req.Priority,
	})
	if err != nil {
		return invocation.Receipt{}, err
	}
	if ins.Conflict {
		return invocation.Receipt{}, invocation.ErrConflict
	}
	return s.receiptFromRow(fac.Name, ins.Row, !ins.Created), nil
}

func (s *Server) receiptFromRow(factoryUID string, row store.InvocationRow, replay bool) invocation.Receipt {
	links := invocation.ReceiptLinks{
		Self:   fmt.Sprintf("/api/v1/factory/%s/invocations/%s", factoryUID, row.ID),
		Result: fmt.Sprintf("/api/v1/factory/%s/invocations/%s/result", factoryUID, row.ID),
	}
	return invocation.Receipt{
		InvocationID:        row.ID,
		WorkItemID:          row.WorkItemID,
		FactoryUID:          factoryUID,
		DefinitionRevision:  row.DefinitionRevision,
		DefinitionDigest:    row.DefinitionDigest,
		ProfileName:         row.ProfileName,
		ProfileDigest:       row.ProfileDigest,
		PolicyDigest:        row.PolicyDigest,
		PinnedHeadSHA:       row.PinnedHeadSHA,
		PinnedBaseSHA:       row.PinnedBaseSHA,
		State:               row.State,
		Outcome:             row.Outcome,
		CountsAsCompleted:   row.CountsAsCompleted,
		SourceOccurrenceKey: row.SourceOccurrenceKey,
		CanonicalDigest:     row.CanonicalDigest,
		Intent:              row.Intent,
		Priority:            row.Priority,
		Links:               links,
		Replay:              replay,
	}
}

func repositoryAllowed(tree *definition.Tree, repo string) bool {
	if tree == nil || len(tree.Factory.Repositories) == 0 {
		return true
	}
	owner, name, ok := splitRepo(repo)
	if !ok {
		return false
	}
	for _, r := range tree.Factory.Repositories {
		if strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}

func splitRepo(repo string) (owner, name string, ok bool) {
	before, after, found := strings.Cut(repo, "/")
	if !found || before == "" || after == "" {
		return "", "", false
	}
	return before, after, true
}

func invocationAgent(tree *definition.Tree, profileName string) (agentName, stage string, err error) {
	if profileName != "" {
		if _, ok := tree.Agents[profileName]; ok {
			stage, err := stageForAgent(tree, profileName)
			return profileName, stage, err
		}
	}
	if tree.Factory.Pipeline != nil && len(tree.Factory.Pipeline.Stages) > 0 {
		stage = tree.Factory.Pipeline.Stages[0]
		if agent, ok := tree.Factory.Pipeline.Agents[stage]; ok {
			return agent, stage, nil
		}
	}
	return "", "", fmt.Errorf("server: no agent for profile %q", profileName)
}

func stageForAgent(tree *definition.Tree, agentName string) (string, error) {
	if tree.Factory.Pipeline == nil {
		return store.StageTriage, nil
	}
	for stage, name := range tree.Factory.Pipeline.Agents {
		if name == agentName {
			return stage, nil
		}
	}
	return store.StageTriage, nil
}

func invocationSource(principal invocation.Principal) string {
	switch principal.Kind {
	case "schedule":
		return api.TaskSourceIssue
	default:
		return api.TaskSourceAPI
	}
}

func invocationTitle(req invocation.Request) string {
	return "Invocation " + req.SourceOccurrenceKey
}

func invocationBody(req invocation.Request) string {
	if instr, ok := req.Inputs["instruction"].(string); ok {
		return instr
	}
	return ""
}

func targetRepo(t *invocation.Target) string {
	if t == nil {
		return ""
	}
	return t.Repository
}

func targetRef(t *invocation.Target) string {
	if t == nil {
		return ""
	}
	return t.Ref
}

func targetPR(t *invocation.Target) *int {
	if t == nil || t.PullRequest <= 0 {
		return nil
	}
	v := t.PullRequest
	return &v
}

func (s *Server) resolveTargetSHAs(ctx context.Context, target *invocation.Target) (head, base string, err error) {
	if target == nil || target.Repository == "" {
		return "", "", nil
	}
	if s.PinTarget != nil {
		return s.PinTarget(ctx, target)
	}
	return s.pinTargetProduction(ctx, target)
}

// pinTargetProduction resolves a pull request or ref to commit SHAs once.
// It does not mint a forge token when the request has no repository.
func (s *Server) pinTargetProduction(ctx context.Context, target *invocation.Target) (head, base string, err error) {
	if target == nil || target.Repository == "" {
		return "", "", nil
	}
	if isCommitSHA(target.Ref) {
		return target.Ref, "", nil
	}
	if target.PullRequest > 0 {
		return s.pinGitHubPull(ctx, target.Repository, target.PullRequest)
	}
	if target.Ref != "" {
		return s.pinGitHubRef(ctx, target.Repository, target.Ref)
	}
	return "", "", fmt.Errorf("server: target %s has no ref or pull request to pin", target.Repository)
}

// PinTarget overrides target resolution in tests and offline drivers.
