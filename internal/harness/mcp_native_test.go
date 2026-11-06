package harness

import (
	"context"
	"testing"
)

func TestNativeToolsContractOptIn(t *testing.T) {
	ctx := context.Background()
	lead, err := IssueRunToken(ctx, TokenScope{
		FactoryID:    "fac_1",
		TaskID:       "ntask_1",
		InvocationID: "inv_1",
		Role:         RoleLead,
		Grants:       []string{GrantInboxRead, GrantRosterDispatch},
		BudgetCents:  50,
	}, Attenuation{})
	if err != nil {
		t.Fatal(err)
	}
	tools := NativeTools{Contract: true}
	delegated, err := tools.Dispatch(ctx, lead, "native_delegate", map[string]string{"taskId": "ntask_1", "invocationId": "inv_1"})
	if err != nil || delegated.Evidence != EvidenceContract || delegated.Payload != "delegate contract" {
		t.Fatalf("delegate %+v %v", delegated, err)
	}
	waited, err := tools.Dispatch(ctx, lead, "native_wait", map[string]string{"taskId": "ntask_1", "invocationId": "inv_1"})
	if err != nil || waited.Evidence != EvidenceContract || waited.Payload != "wait contract" {
		t.Fatalf("wait %+v %v", waited, err)
	}
	if _, err := (NativeTools{}).Dispatch(ctx, lead, "native_delegate", map[string]string{"taskId": "ntask_1", "invocationId": "inv_1"}); err == nil {
		t.Fatal("production delegate ran without a client")
	}
	if _, err := (NativeTools{}).Dispatch(ctx, lead, "native_wait", map[string]string{"taskId": "ntask_1", "invocationId": "inv_1"}); err == nil {
		t.Fatal("production wait ran without a client")
	}
}
