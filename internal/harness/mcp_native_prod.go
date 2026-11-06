package harness

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/client"
)

const (
	nativeStatusRunning   = "running"
	nativeStatusParked    = "parked"
	nativeStatusCompleted = "completed"
)

func (t NativeTools) delegate(ctx context.Context, tok RunToken, args map[string]string) (ToolResult, error) {
	if err := t.need(tok, GrantRosterDispatch, args["taskId"], args["invocationId"]); err != nil {
		return ToolResult{}, err
	}
	cents, err := nativeCents(args["reserveCents"])
	if err != nil {
		return ToolResult{}, err
	}
	out, err := t.API.DelegateNative(ctx, tok.FactoryID(), args["taskId"], client.NativeDelegateRequest{
		Assignee:       args["assignee"],
		IdempotencyKey: args["idempotencyKey"],
		JoinPolicy:     args["joinPolicy"],
		ReserveCents:   cents,
	})
	if err != nil {
		return ToolResult{}, err
	}
	return t.redacted(EvidenceProduction, out.ChildID)
}

func (t NativeTools) activate(ctx context.Context, tok RunToken, args map[string]string) (ToolResult, error) {
	if err := t.need(tok, GrantTaskWrite, args["taskId"], args["invocationId"]); err != nil {
		return ToolResult{}, err
	}
	out, err := t.API.ActivateNative(ctx, tok.FactoryID(), args["taskId"], client.NativeActivateRequest{})
	if err != nil {
		return ToolResult{}, err
	}
	return t.redacted(EvidenceProduction, out.RunID)
}

func (t NativeTools) complete(ctx context.Context, tok RunToken, args map[string]string) (ToolResult, error) {
	if err := t.need(tok, GrantTaskWrite, args["taskId"], args["invocationId"]); err != nil {
		return ToolResult{}, err
	}
	out, err := t.API.CompleteNative(ctx, tok.FactoryID(), args["taskId"], client.NativeCompleteRequest{
		Failed: args["failed"] == "true",
	})
	if err != nil {
		return ToolResult{}, err
	}
	return t.redacted(EvidenceProduction, out.RunID)
}

// wait is park-until-join, or a completed-task wake.
// A running task parks, then joins. A parked task joins, which resumes it
// when every required child is finished. A completed task calls wake-run
// and opens a linked run. There is no separate wait route.
func (t NativeTools) wait(ctx context.Context, tok RunToken, args map[string]string) (ToolResult, error) {
	if err := t.need(tok, GrantInboxRead, args["taskId"], args["invocationId"]); err != nil {
		return ToolResult{}, err
	}
	task, err := t.API.GetNativeTask(ctx, tok.FactoryID(), args["taskId"])
	if err != nil {
		return ToolResult{}, err
	}
	status := task.Status
	if status == "" {
		// A client double may omit status. Park then join still runs.
		status = nativeStatusRunning
	}
	switch status {
	case nativeStatusCompleted:
		return t.wakeCompleted(ctx, tok, args["taskId"])
	case nativeStatusParked:
		return t.joinParked(ctx, tok, args["taskId"])
	case nativeStatusRunning:
		if _, err := t.API.ParkNative(ctx, tok.FactoryID(), args["taskId"], client.NativeParkRequest{
			BlockerIDs: splitIDs(args["blockerIds"]),
		}); err != nil {
			return ToolResult{}, err
		}
		return t.joinParked(ctx, tok, args["taskId"])
	default:
		return ToolResult{}, fmt.Errorf("harness: native wait status %s", status)
	}
}

func (t NativeTools) joinParked(ctx context.Context, tok RunToken, taskID string) (ToolResult, error) {
	joined, err := t.API.JoinNative(ctx, tok.FactoryID(), taskID)
	if err != nil {
		return ToolResult{}, err
	}
	payload := fmt.Sprintf("joined status=%s run=%s unresolved=%d", joined.Status, joined.RunID, len(joined.Unresolved))
	return t.redacted(EvidenceProduction, payload)
}

func (t NativeTools) wakeCompleted(ctx context.Context, tok RunToken, taskID string) (ToolResult, error) {
	out, err := t.API.WakeNativeRun(ctx, tok.FactoryID(), taskID, client.NativeWakeRunRequest{})
	if err != nil {
		return ToolResult{}, err
	}
	return t.redacted(EvidenceProduction, "linked "+out.LinkedRunID)
}

func nativeCents(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("harness: reserve cents: %w", err)
	}
	return n, nil
}

func splitIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
