package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/result"
	"github.com/polds/factory/internal/store"
)

var _ api.InvocationBackend = apiFacade{}

func (f apiFacade) Invoke(ctx context.Context, factoryUID string, req api.InvocationRequest, principal api.InvocationPrincipal) (api.InvocationReceipt, error) {
	invReq := fromAPIRequest(req)
	invReq.ExecuteTargetGrant = executeTargetRequested(req.Inputs)
	receipt, err := f.AdmitInvocation(ctx, factoryUID, invocation.Principal{
		Kind:  principal.Kind,
		Actor: principal.Actor,
		Allow: principal.Actor != "",
	}, invReq)
	if err != nil {
		var rej invocation.Reject
		if errors.As(err, &rej) {
			return api.InvocationReceipt{}, api.ErrInvocationRejected{Code: rej.Code, Status: rej.Status}
		}
		if errors.Is(err, invocation.ErrConflict) {
			return api.InvocationReceipt{}, api.ErrInvocationConflict
		}
		return api.InvocationReceipt{}, err
	}
	return toAPIReceipt(receipt), nil
}

func (f apiFacade) GetInvocation(ctx context.Context, factoryUID, invocationID string) (api.InvocationReceipt, error) {
	fac, err := f.factory(ctx, factoryUID)
	if err != nil {
		return api.InvocationReceipt{}, err
	}
	row, err := f.store.GetInvocation(ctx, fac.ID, invocationID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.InvocationReceipt{}, api.ErrNotFound
		}
		return api.InvocationReceipt{}, err
	}
	return toAPIReceipt(f.receiptFromRow(fac.Name, row, true)), nil
}

func (f apiFacade) GetInvocationResult(ctx context.Context, factoryUID, invocationID string, principal api.InvocationPrincipal) (api.InvocationResult, error) {
	if principal.Actor == "" {
		return api.InvocationResult{}, resultUnauthorized{}
	}
	fac, err := f.factory(ctx, factoryUID)
	if err != nil {
		return api.InvocationResult{}, err
	}
	if _, err := f.store.GetInvocation(ctx, fac.ID, invocationID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.InvocationResult{}, api.ErrNotFound
		}
		return api.InvocationResult{}, err
	}
	row, err := f.store.GetResult(ctx, fac.ID, invocationID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.InvocationResult{}, api.ErrNotFound
		}
		return api.InvocationResult{}, err
	}
	return api.InvocationResult{
		Summary:        row.Summary,
		CoverageStatus: row.CoverageStatus,
		FindingsCount:  row.FindingsCount,
		Report:         string(row.ReportBody),
	}, nil
}

func (f apiFacade) PublishInvocationResult(ctx context.Context, factoryUID, invocationID string, principal api.InvocationPrincipal, rep api.InvocationResultPublish) error {
	if principal.Actor == "" {
		return result.PublishError{Code: result.CodeUnauthorized}
	}
	fac, err := f.factory(ctx, factoryUID)
	if err != nil {
		return err
	}
	pub := result.Report{
		Summary:        rep.Summary,
		CoverageStatus: rep.CoverageStatus,
		FindingsCount:  rep.FindingsCount,
		Body:           []byte(rep.Report),
		ArtifactPaths:  rep.ArtifactPaths,
	}
	if err := result.Publish(true, "", pub); err != nil {
		return err
	}
	return f.store.PutResult(ctx, store.ResultRow{
		InvocationID:   invocationID,
		FactoryID:      fac.ID,
		Summary:        rep.Summary,
		CoverageStatus: rep.CoverageStatus,
		FindingsCount:  rep.FindingsCount,
		ReportBody:     []byte(rep.Report),
		ArtifactPaths:  rep.ArtifactPaths,
		PublishedAt:    time.Now().UTC(),
	})
}

type resultUnauthorized struct{}

func (resultUnauthorized) Error() string { return "result_unauthorized" }

func fromAPIRequest(req api.InvocationRequest) invocation.Request {
	out := invocation.Request{
		DefinitionRevision:  req.DefinitionRevision,
		Inputs:              req.Inputs,
		SourceOccurrenceKey: req.SourceOccurrenceKey,
		Intent:              req.Intent,
		Priority:            req.Priority,
	}
	if req.Profile != nil {
		out.Profile.Name = req.Profile.Name
	}
	if req.Target != nil {
		out.Target = &invocation.Target{
			Repository:  req.Target.Repository,
			Ref:         req.Target.Ref,
			PullRequest: req.Target.PullRequest,
		}
	}
	if req.Correlation != nil {
		out.Correlation = &invocation.Correlation{
			ConversationID: req.Correlation.ConversationID,
			ReplyTo:        req.Correlation.ReplyTo,
			ThreadRef:      req.Correlation.ThreadRef,
		}
	}
	return out
}

func executeTargetRequested(inputs map[string]any) bool {
	if inputs == nil {
		return false
	}
	v, _ := inputs["executeTarget"].(bool)
	return v
}

func toAPIReceipt(r invocation.Receipt) api.InvocationReceipt {
	return api.InvocationReceipt{
		InvocationID:        r.InvocationID,
		WorkItemID:          r.WorkItemID,
		FactoryUID:          r.FactoryUID,
		DefinitionRevision:  r.DefinitionRevision,
		DefinitionDigest:    r.DefinitionDigest,
		ProfileName:         r.ProfileName,
		ProfileDigest:       r.ProfileDigest,
		PolicyDigest:        r.PolicyDigest,
		PinnedHeadSHA:       r.PinnedHeadSHA,
		PinnedBaseSHA:       r.PinnedBaseSHA,
		State:               r.State,
		Outcome:             r.Outcome,
		CountsAsCompleted:   r.CountsAsCompleted,
		SourceOccurrenceKey: r.SourceOccurrenceKey,
		CanonicalDigest:     r.CanonicalDigest,
		Intent:              r.Intent,
		Priority:            r.Priority,
		Links: api.InvocationReceiptLinks{
			Self:   r.Links.Self,
			Result: r.Links.Result,
		},
		Replay: r.Replay,
	}
}

func (f apiFacade) InvokePath(factoryUID string) string {
	return fmt.Sprintf("/api/v1/factory/%s/invocations", factoryUID)
}
