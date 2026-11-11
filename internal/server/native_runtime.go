package server

import (
	"context"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func (s *Server) OpenNativeTree(ctx context.Context, factoryUID, invocationID string, req api.NativeTreeRequest) (api.NativeTreeOpened, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeTreeOpened{}, err
	}
	if _, err := s.store.GetInvocation(ctx, fac.ID, invocationID); err != nil {
		return api.NativeTreeOpened{}, nativeStoreErr(err)
	}
	slots := req.Slots
	if slots == 0 {
		slots = 1
	}
	maxLive := req.MaxLive
	if maxLive == 0 {
		maxLive = 4
	}
	personas := make([]definition.NativePersona, len(req.Personas))
	for i, p := range req.Personas {
		files := make([]definition.RoleFile, len(p.Files))
		for j, file := range p.Files {
			files[j] = definition.RoleFile{
				Name: file.Name, SHA256: file.SHA256, GitBlob: file.GitBlob,
				Bytes: append([]byte{}, file.Bytes...),
			}
		}
		personas[i] = definition.NativePersona{
			Name: p.Name, ReportsTo: p.ReportsTo, SpawnableBy: p.SpawnableBy, Role: p.Role,
			SourceReportsTo: p.SourceReportsTo, SourceDottedTo: p.SourceDottedTo, Files: files,
		}
	}
	tree := definition.NativeTree{
		NativeHierarchy:       req.NativeHierarchy,
		Personas:              personas,
		MaxDepth:              req.MaxDepth,
		AbsoluteMaxDepth:      req.AbsoluteMaxDepth,
		AllowAncestorShortcut: req.AllowAncestorShortcut,
		Delegation:            req.Delegation,
		Synthetic:             req.Synthetic,
		SourceCommit:          req.SourceCommit,
		RosterSHA256:          req.RosterSHA256,
		RosterGitBlob:         req.RosterGitBlob,
		BundleMode:            req.BundleMode,
	}
	if err := s.attestIfCommit(ctx, invocationID, tree); err != nil {
		return api.NativeTreeOpened{}, err
	}
	policy, task, err := coordination.OpenTree(ctx, s.store, store.OpenTreeInput{
		FactoryID:    fac.ID,
		InvocationID: invocationID,
		AgentID:      req.AgentID,
		WorkItemID:   req.WorkItemID,
		Stage:        req.Stage,
		CeilingCents: req.CeilingCents,
		Slots:        slots,
		MaxLive:      maxLive,
		SharedKey:    req.SharedKey,
		Tree:         tree,
	})
	if err != nil {
		return api.NativeTreeOpened{}, nativeStoreErr(err)
	}
	return api.NativeTreeOpened{Digest: policy.Digest, StageOwner: policy.StageOwner, RootTaskID: task.ID, InvocationID: invocationID}, nil
}

func (s *Server) DelegateNative(ctx context.Context, factoryUID, taskID string, req api.NativeDelegateRequest) (api.NativeDelegateResult, error) {
	fac, task, err := s.nativeHead(ctx, factoryUID, taskID)
	if err != nil {
		return api.NativeDelegateResult{}, err
	}
	copied, err := s.reviewerHead(ctx, task.InvocationID, req.Assignee)
	if err != nil {
		return api.NativeDelegateResult{}, err
	}
	out, err := coordination.Delegate(ctx, s.store, store.DelegateInput{
		FactoryID:      fac.ID,
		InvocationID:   task.InvocationID,
		ParentTaskID:   task.ID,
		Assignee:       req.Assignee,
		IdempotencyKey: req.IdempotencyKey,
		AgentID:        req.AgentID,
		WorkItemID:     req.WorkItemID,
		Stage:          req.Stage,
		JoinPolicy:     req.JoinPolicy,
		ReserveCents:   req.ReserveCents,
		UsageDeadline:  req.UsageDeadline,
	})
	if err != nil {
		return api.NativeDelegateResult{}, nativeStoreErr(err)
	}
	if copied.HeadSHA != "" {
		copied.TaskID = out.Child.ID
		if err := s.store.PutProducedHead(ctx, copied); err != nil {
			return api.NativeDelegateResult{}, err
		}
	}
	return api.NativeDelegateResult{
		ChildID: out.Child.ID, ReservationID: out.ReservationID,
		ReservationCents: out.ReservationCents, Replay: out.Replay,
	}, nil
}

func (s *Server) ActivateNative(ctx context.Context, factoryUID, taskID string, req api.NativeActivateRequest) (api.NativeActivation, error) {
	if _, _, err := s.nativeHead(ctx, factoryUID, taskID); err != nil {
		return api.NativeActivation{}, err
	}
	act, err := coordination.ClaimActivation(ctx, s.store, taskID, store.ClaimInput{
		AgentID: req.AgentID, WorkItemID: req.WorkItemID, Stage: req.Stage,
	})
	if err != nil {
		return api.NativeActivation{}, nativeStoreErr(err)
	}
	return wireActivation(act), nil
}

func (s *Server) ParkNative(ctx context.Context, factoryUID, taskID string, req api.NativeParkRequest) (api.NativeActivation, error) {
	if _, _, err := s.nativeHead(ctx, factoryUID, taskID); err != nil {
		return api.NativeActivation{}, err
	}
	act, err := coordination.Park(ctx, s.store, store.ParkInput{TaskID: taskID, BlockerIDs: req.BlockerIDs})
	if err != nil {
		return api.NativeActivation{}, nativeStoreErr(err)
	}
	return wireActivation(act), nil
}

func (s *Server) WakeNativeRun(ctx context.Context, factoryUID, taskID string, req api.NativeWakeRunRequest) (api.NativeWakeRunResult, error) {
	if _, _, err := s.nativeHead(ctx, factoryUID, taskID); err != nil {
		return api.NativeWakeRunResult{}, err
	}
	out, err := coordination.Wake(ctx, s.store, store.WakeInput{TaskID: taskID, NegotiatedSteer: req.NegotiatedSteer})
	if err != nil {
		return api.NativeWakeRunResult{}, nativeStoreErr(err)
	}
	return api.NativeWakeRunResult{
		Delivery: out.Delivery, Activation: wireActivation(out.Activation),
		PriorRunID: out.PriorRunID, PriorStatus: out.PriorStatus, LinkedRunID: out.LinkedRunID,
	}, nil
}

func (s *Server) JoinNative(ctx context.Context, factoryUID, taskID string) (api.NativeJoinResult, error) {
	_, parent, err := s.nativeHead(ctx, factoryUID, taskID)
	if err != nil {
		return api.NativeJoinResult{}, err
	}
	out, err := coordination.Join(ctx, s.store, store.JoinInput{TaskID: taskID})
	if err != nil {
		return api.NativeJoinResult{}, nativeStoreErr(err)
	}
	if err := s.requireReadyImplementer(ctx, parent); err != nil {
		return api.NativeJoinResult{}, err
	}
	if out.StageOwner && out.RunID != "" && out.Status == store.NativeFailed {
		s.advanceAfterTerminal(ctx, out.RunID)
	}
	return api.NativeJoinResult{
		ParentTaskID: out.ParentTaskID, Policy: out.Policy, Status: out.Status,
		Synthesized: out.Synthesized, SpawnedReplacement: out.SpawnedReplacement,
		Unresolved: out.Unresolved, RunID: out.RunID, StageOwner: out.StageOwner,
	}, nil
}

func (s *Server) CancelNativeSubtree(ctx context.Context, factoryUID, taskID string, pause bool) (api.NativeSubtreeResult, error) {
	if _, _, err := s.nativeHead(ctx, factoryUID, taskID); err != nil {
		return api.NativeSubtreeResult{}, err
	}
	var out store.SubtreeResult
	var err error
	if pause {
		out, err = coordination.PauseSubtree(ctx, s.store, taskID)
	} else {
		out, err = coordination.CancelSubtree(ctx, s.store, taskID)
	}
	if err != nil {
		return api.NativeSubtreeResult{}, nativeStoreErr(err)
	}
	return api.NativeSubtreeResult{Root: out.Root, Descendants: out.Descendants, State: out.State, LaterSpawn: out.LaterSpawn}, nil
}

func (s *Server) BoardResumeNative(ctx context.Context, factoryUID, taskID string, req api.NativeActivateRequest) (api.NativeBoardResume, error) {
	if _, _, err := s.nativeHead(ctx, factoryUID, taskID); err != nil {
		return api.NativeBoardResume{}, err
	}
	out, err := coordination.BoardResume(ctx, s.store, store.BoardResumeInput{
		TaskID: taskID, Principal: store.CeilingPrincipal,
		AgentID: req.AgentID, WorkItemID: req.WorkItemID, Stage: req.Stage,
	})
	if err != nil {
		return api.NativeBoardResume{}, nativeStoreErr(err)
	}
	return api.NativeBoardResume{
		TaskID: out.TaskID, PriorRunID: out.PriorRunID, PriorStatus: out.PriorStatus,
		NewRunID: out.NewRunID, AuditID: out.AuditID,
	}, nil
}

func (s *Server) CompleteNative(ctx context.Context, factoryUID, taskID string, failed bool) (api.NativeActivation, error) {
	if _, _, err := s.nativeHead(ctx, factoryUID, taskID); err != nil {
		return api.NativeActivation{}, err
	}
	act, err := coordination.Complete(ctx, s.store, taskID, failed)
	if err != nil {
		return api.NativeActivation{}, nativeStoreErr(err)
	}
	if act.StageOwner {
		s.advanceAfterTerminal(ctx, act.RunID)
	}
	if err := s.finishDraftPublication(ctx, factoryUID, taskID, failed); err != nil {
		return wireActivation(act), err
	}
	return wireActivation(act), nil
}

func (s *Server) ReserveNative(ctx context.Context, factoryUID, invocationID string, req api.NativeReserveRequest) (api.NativeReservation, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeReservation{}, err
	}
	if _, err := s.store.GetInvocation(ctx, fac.ID, invocationID); err != nil {
		return api.NativeReservation{}, nativeStoreErr(err)
	}
	out, err := coordination.ReserveAncestors(ctx, s.store, store.ReserveInput{
		InvocationID: invocationID, TaskID: req.TaskID, IdempotencyKey: req.IdempotencyKey,
		Cents: req.Cents, ScopeIDs: req.ScopeIDs, UsageDeadline: req.UsageDeadline,
		CeilingCents: req.CeilingCents, SharedKey: req.SharedKey,
	})
	if err != nil {
		return api.NativeReservation{}, nativeStoreErr(err)
	}
	return api.NativeReservation{ID: out.ID, Cents: out.Cents, State: out.State, ScopeIDs: out.ScopeIDs, Replay: out.Replay}, nil
}

func (s *Server) SettleNative(ctx context.Context, factoryUID, invocationID string, req api.NativeSettleRequest) (api.NativeSettlement, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeSettlement{}, err
	}
	if _, err := s.store.GetInvocation(ctx, fac.ID, invocationID); err != nil {
		return api.NativeSettlement{}, nativeStoreErr(err)
	}
	out, err := coordination.SettleUsage(ctx, s.store, store.SettleInput{
		InvocationID: invocationID, ReservationID: req.ReservationID,
		CallID: req.CallID, AttemptID: req.AttemptID, Cents: req.Cents, Failed: req.Failed,
	})
	if err != nil {
		return api.NativeSettlement{}, nativeStoreErr(err)
	}
	return api.NativeSettlement{
		CallID: out.CallID, AttemptID: out.AttemptID, CountedCents: out.CountedCents,
		QuarantinedCents: out.QuarantinedCents, Duplicate: out.Duplicate, Paused: out.Paused, Code: out.Code,
	}, nil
}

func (s *Server) AdmitNative(ctx context.Context, factoryUID, invocationID string, req api.NativeAdmitRequest) (api.NativeAdmitResult, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return api.NativeAdmitResult{}, err
	}
	if _, err := s.store.GetInvocation(ctx, fac.ID, invocationID); err != nil {
		return api.NativeAdmitResult{}, nativeStoreErr(err)
	}
	out, err := coordination.Admit(ctx, s.store, store.AdmitInput{
		InvocationID: invocationID, ScopeIDs: req.ScopeIDs, SharedKey: req.SharedKey,
	})
	if err != nil {
		return api.NativeAdmitResult{}, nativeStoreErr(err)
	}
	return api.NativeAdmitResult{Admitted: out.Admitted, OvershootBoundCents: out.OvershootBoundCents, GuaranteesCeiling: out.GuaranteesCeiling}, nil
}

func (s *Server) AuthorizeNativeCeiling(ctx context.Context, factoryUID string, req api.NativeCeilingRequest) (api.NativeCeilingAudit, error) {
	if _, err := s.factory(ctx, factoryUID); err != nil {
		return api.NativeCeilingAudit{}, err
	}
	out, err := coordination.AuthorizeCeiling(ctx, s.store, store.CeilingInput{
		ScopeID: req.ScopeID, Principal: store.CeilingPrincipal, NewCeiling: req.NewCeiling,
	})
	if err != nil {
		return api.NativeCeilingAudit{}, nativeStoreErr(err)
	}
	return api.NativeCeilingAudit{
		ID: out.ID, ScopeID: out.ScopeID, Principal: out.Principal,
		PriorCeiling: out.PriorCeiling, NewCeiling: out.NewCeiling, SpentCents: out.SpentCents,
	}, nil
}

func (s *Server) ListNativeTrees(ctx context.Context, factoryUID string) ([]api.NativeTreeView, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return nil, err
	}
	views, err := s.store.ListNativeTrees(ctx, fac.ID)
	if err != nil {
		return nil, nativeStoreErr(err)
	}
	out := make([]api.NativeTreeView, 0, len(views))
	for _, view := range views {
		item := api.NativeTreeView{
			InvocationID: view.InvocationID, StageOwner: view.StageOwner,
			Nodes:    make([]api.NativeTreeNode, 0, len(view.Nodes)),
			Budgets:  make([]api.NativeBudget, 0, len(view.Budgets)),
			Blockers: make([]api.NativeBlockerDTO, 0, len(view.Blockers)),
		}
		for _, node := range view.Nodes {
			item.Nodes = append(item.Nodes, api.NativeTreeNode{
				TaskID: node.TaskID, Assignee: node.Assignee, ParentTaskID: node.ParentTaskID,
				Status: node.Status, Depth: node.Depth, StageOwner: node.StageOwner, RunID: node.RunID,
			})
		}
		for _, scope := range view.Budgets {
			item.Budgets = append(item.Budgets, api.NativeBudget{
				ScopeID: scope.ScopeID, Kind: scope.Kind, CeilingCents: scope.CeilingCents,
				SpentCents: scope.SpentCents, ReservedCents: scope.ReservedCents, Paused: scope.Paused,
				MissingUsage: scope.MissingUsage, GuaranteesCeiling: scope.GuaranteesCeiling,
			})
		}
		for _, edge := range view.Blockers {
			item.Blockers = append(item.Blockers, api.NativeBlockerDTO{
				ParentTaskID: edge.ParentTaskID, ChildTaskID: edge.ChildTaskID,
				Policy: edge.Policy, ChildStatus: edge.ChildStatus,
			})
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Server) nativeHead(ctx context.Context, factoryUID, taskID string) (store.Factory, store.NativeTask, error) {
	fac, err := s.factory(ctx, factoryUID)
	if err != nil {
		return store.Factory{}, store.NativeTask{}, err
	}
	task, err := s.store.GetNativeTask(ctx, fac.ID, taskID)
	if err != nil {
		return store.Factory{}, store.NativeTask{}, nativeStoreErr(err)
	}
	return fac, task, nil
}

func wireActivation(act store.Activation) api.NativeActivation {
	return api.NativeActivation{
		ID: act.ID, TaskID: act.TaskID, RunID: act.RunID, ExecutionID: act.ExecutionID,
		Fence: act.Fence, State: act.State, StageOwner: act.StageOwner,
		CapacityHeld: act.CapacityHeld, PriorRunID: act.PriorRunID,
	}
}

var _ api.NativeRuntime = apiFacade{}
