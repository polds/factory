package invocation_test

import (
	"errors"
	"testing"

	"github.com/polds/factory/internal/invocation"
)

func TestINVProfileRejectsInjectedFields(t *testing.T) {
	raw := map[string]any{
		"sourceOccurrenceKey": "k1",
		"intent":              "new",
		"priority":            "interactive",
		"profile": map[string]any{
			"name":               "scan",
			"budgetCeilingCents": 100,
		},
	}
	err := invocation.ValidateRequest(raw)
	var rej invocation.Reject
	if err == nil {
		t.Fatal("expected reject")
	}
	if !errorsAs(err, &rej) || rej.Code != invocation.CodeProfileRejected {
		t.Fatalf("err %v", err)
	}
}

func errorsAs(err error, target *invocation.Reject) bool {
	var rej invocation.Reject
	if !errors.As(err, &rej) {
		return false
	}
	*target = rej
	return true
}
