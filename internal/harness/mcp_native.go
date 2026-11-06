package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/polds/factory/internal/client"
)

// nativeClient is the production surface NativeTools calls.
// Delegate, activate, complete, park, join, and wake-run are included.
type nativeClient interface {
	CreateNativeTask(ctx context.Context, factoryUID string, req client.NativeTaskRequest) (client.NativeTask, error)
	GetNativeTask(ctx context.Context, factoryUID, taskID string) (client.NativeTask, error)
	CommentNativeTask(ctx context.Context, factoryUID, taskID string, req client.NativeCommentRequest) (client.NativeEventResult, error)
	AssignNativeTask(ctx context.Context, factoryUID, taskID, assignee string) (client.NativeEventResult, error)
	ReadNativeInbox(ctx context.Context, factoryUID, assignee, cursor string, limit int, wait time.Duration) (client.NativeInboxPage, error)
	ApplyNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (client.NativeApplyResult, error)
	AckNativeDelivery(ctx context.Context, factoryUID, taskID, deliveryID string) (client.NativeAckResult, error)
	RedeliverNativeMessage(ctx context.Context, factoryUID, taskID, messageID string) (client.NativeRedelivery, error)
	ClaimNativeInbox(ctx context.Context, factoryUID, taskID, holder string) (client.NativeClaim, error)
	GetNativeWake(ctx context.Context, factoryUID, taskID string) (client.NativeWake, error)
	DelegateNative(ctx context.Context, factoryUID, taskID string, req client.NativeDelegateRequest) (client.NativeDelegateResult, error)
	ActivateNative(ctx context.Context, factoryUID, taskID string, req client.NativeActivateRequest) (client.NativeActivation, error)
	CompleteNative(ctx context.Context, factoryUID, taskID string, req client.NativeCompleteRequest) (client.NativeActivation, error)
	ParkNative(ctx context.Context, factoryUID, taskID string, req client.NativeParkRequest) (client.NativeActivation, error)
	JoinNative(ctx context.Context, factoryUID, taskID string) (client.NativeJoinResult, error)
	WakeNativeRun(ctx context.Context, factoryUID, taskID string, req client.NativeWakeRunRequest) (client.NativeWakeRunResult, error)
}

// Compile-time check that the real client is the production implementation.
var _ nativeClient = (*client.Client)(nil)

const (
	// EvidenceProduction is a call that hit client.Client.
	EvidenceProduction = "production"
	// EvidenceContract is the delegate/wait conformance fake.
	// Production dispatch leaves NativeTools.Contract false.
	EvidenceContract = "contract"
)

// ToolResult is one MCP tool response. Payload is redacted.
type ToolResult struct {
	Evidence string `json:"evidence"`
	Payload  string `json:"payload"`
}

// NativeTools dispatches MCP tools.
// Contract keeps delegate and wait on the conformance fake. Tests set it.
// Production dispatch leaves it false and calls the client.
type NativeTools struct {
	API      nativeClient
	Needles  []string
	Contract bool
}

// Dispatch runs one native tool. Delegate calls DelegateNative.
// Wait parks a running task, joins a parked task, and wakes a completed task.
func (t NativeTools) Dispatch(ctx context.Context, tok RunToken, name string, args map[string]string) (ToolResult, error) {
	switch name {
	case "native_delegate":
		if t.Contract {
			return t.contract(tok, GrantRosterDispatch, args)
		}
		return t.delegate(ctx, tok, args)
	case "native_wait":
		if t.Contract {
			if err := Authorize(tok, GrantInboxRead, args["taskId"], args["invocationId"]); err != nil {
				return ToolResult{}, err
			}
			return t.redacted(EvidenceContract, "wait contract")
		}
		return t.wait(ctx, tok, args)
	case "native_activate":
		return t.activate(ctx, tok, args)
	case "native_complete":
		return t.complete(ctx, tok, args)
	case "native_task_create":
		if err := t.need(tok, GrantTaskWrite, "", args["invocationId"]); err != nil {
			return ToolResult{}, err
		}
		if t.API == nil {
			return ToolResult{}, errors.New("harness: native client is not configured")
		}
		task, err := t.API.CreateNativeTask(ctx, tok.FactoryID(), client.NativeTaskRequest{
			InvocationID: args["invocationId"],
			ParentTaskID: tok.TaskID(),
			Assignee:     args["assignee"],
		})
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, task.ID)
	case "native_task_get":
		if err := t.need(tok, GrantTaskRead, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		task, err := t.API.GetNativeTask(ctx, tok.FactoryID(), args["taskId"])
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, task.ID)
	case "native_comment":
		if err := t.need(tok, GrantCommentWrite, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		ev, err := t.API.CommentNativeTask(ctx, tok.FactoryID(), args["taskId"], client.NativeCommentRequest{Body: args["body"]})
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, ev.MessageID)
	case "native_assign":
		if err := t.need(tok, GrantTaskWrite, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		ev, err := t.API.AssignNativeTask(ctx, tok.FactoryID(), args["taskId"], args["assignee"])
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, ev.MessageID)
	case "native_inbox_read":
		if err := t.need(tok, GrantInboxRead, "", ""); err != nil {
			return ToolResult{}, err
		}
		page, err := t.API.ReadNativeInbox(ctx, tok.FactoryID(), args["assignee"], args["cursor"], 0, 0)
		if err != nil {
			return ToolResult{}, err
		}
		var ids []string
		for _, row := range page.Deliveries {
			if row.InvocationID != "" && row.InvocationID != tok.InvocationID() {
				continue
			}
			ids = append(ids, row.DeliveryID)
		}
		return t.redacted(EvidenceProduction, strings.Join(ids, ","))
	case "native_apply":
		if err := t.need(tok, GrantAckWrite, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		res, err := t.API.ApplyNativeMessage(ctx, tok.FactoryID(), args["taskId"], args["messageId"])
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, res.MessageID)
	case "native_ack":
		if err := t.need(tok, GrantAckWrite, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		res, err := t.API.AckNativeDelivery(ctx, tok.FactoryID(), args["taskId"], args["deliveryId"])
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, res.DeliveryID)
	case "native_redeliver":
		if err := t.need(tok, GrantAckWrite, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		res, err := t.API.RedeliverNativeMessage(ctx, tok.FactoryID(), args["taskId"], args["messageId"])
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, res.DeliveryID)
	case "native_claim":
		if err := t.need(tok, GrantInboxRead, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		res, err := t.API.ClaimNativeInbox(ctx, tok.FactoryID(), args["taskId"], args["holder"])
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, res.Holder)
	case "native_wake":
		if err := t.need(tok, GrantInboxRead, args["taskId"], ""); err != nil {
			return ToolResult{}, err
		}
		res, err := t.API.GetNativeWake(ctx, tok.FactoryID(), args["taskId"])
		if err != nil {
			return ToolResult{}, err
		}
		return t.redacted(EvidenceProduction, res.TaskID)
	default:
		return ToolResult{}, errors.New("harness: unknown native tool")
	}
}

func (t NativeTools) need(tok RunToken, grant, taskID, invocationID string) error {
	if t.API == nil {
		return errors.New("harness: native client is not configured")
	}
	return Authorize(tok, grant, taskID, invocationID)
}

func (t NativeTools) contract(tok RunToken, grant string, args map[string]string) (ToolResult, error) {
	if err := Authorize(tok, grant, args["taskId"], args["invocationId"]); err != nil {
		return ToolResult{}, err
	}
	return t.redacted(EvidenceContract, "delegate contract")
}

func (t NativeTools) redacted(evidence, payload string) (ToolResult, error) {
	body, err := json.Marshal(ToolResult{Evidence: evidence, Payload: Redact(payload, t.Needles)})
	if err != nil {
		return ToolResult{}, err
	}
	var out ToolResult
	if err := json.Unmarshal(body, &out); err != nil {
		return ToolResult{}, err
	}
	return out, nil
}
